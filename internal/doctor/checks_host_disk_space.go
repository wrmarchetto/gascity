package doctor

import (
	"fmt"
	"os"
	"path/filepath"
)

// hostDiskMinFreeBytes reserves enough room on the host root filesystem for a
// complete parallel test gate. Below this floor, Go compilation and linking
// can fail as unrelated-looking ENOSPC errors.
const hostDiskMinFreeBytes = int64(40) << 30

// hostDiskConsumer is one directory whose footprint helps explain a low host
// disk result.
type hostDiskConsumer struct {
	label string
	path  string
}

// HostDiskSpaceCheck detects low free space on the host root filesystem. It
// measures the Go build cache and compiler temporary area only after finding
// low capacity, so the ordinary doctor path remains cheap while the failure
// names the largest likely consumer.
type HostDiskSpaceCheck struct {
	rootPath     string
	minFreeBytes int64
	freeBytes    func(string) (int64, error)
	consumers    []hostDiskConsumer
	measureDir   func(string) (int64, bool, error)
}

// NewHostDiskSpaceCheck creates a check for the filesystem mounted at /.
// Doctor must inspect the host root rather than CityPath because city state
// can reside on a different filesystem while Go's host-wide build paths fill
// the root volume.
func NewHostDiskSpaceCheck() *HostDiskSpaceCheck {
	return &HostDiskSpaceCheck{
		rootPath:     "/",
		minFreeBytes: hostDiskMinFreeBytes,
		freeBytes:    hostRootFreeBytes,
		consumers: []hostDiskConsumer{
			{label: "GOCACHE", path: goCachePath()},
			{label: "GOTMPDIR", path: goTempPath()},
		},
		measureDir: duDirBytes,
	}
}

// Name returns the check identifier.
func (c *HostDiskSpaceCheck) Name() string { return "host-disk-space" }

// Run probes host-root capacity and reports the largest measured Go build
// consumer if the gate reserve is no longer available.
func (c *HostDiskSpaceCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name()}
	rootPath := c.rootPath
	if rootPath == "" {
		rootPath = "/"
	}
	minFreeBytes := c.minFreeBytes
	if minFreeBytes == 0 {
		minFreeBytes = hostDiskMinFreeBytes
	}
	freeBytes := c.freeBytes
	if freeBytes == nil {
		freeBytes = hostRootFreeBytes
	}

	free, err := freeBytes(rootPath)
	if err != nil {
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("measure free space on %s: %v", rootPath, err)
		r.FixHint = "verify host filesystem access, then re-run gc doctor"
		return r
	}
	if free >= minFreeBytes {
		r.Status = StatusOK
		r.Message = fmt.Sprintf("%s free on %s (reserve %s)", formatGB(free), rootPath, formatGB(minFreeBytes))
		return r
	}

	r.Status = StatusError
	r.Message = fmt.Sprintf("only %s free on %s (minimum %s for one full test gate)", formatGB(free), rootPath, formatGB(minFreeBytes))
	r.FixHint = "free host disk space before running builds; inspect the named Go build consumer and re-run gc doctor"

	measure := c.measureDir
	if measure == nil {
		measure = duDirBytes
	}
	var largest hostDiskConsumer
	var largestBytes int64
	for _, consumer := range c.consumers {
		if consumer.label == "" || consumer.path == "" {
			continue
		}
		bytes, exists, measureErr := measure(consumer.path)
		if measureErr != nil {
			r.Details = append(r.Details, fmt.Sprintf("could not measure %s (%s): %v", consumer.label, consumer.path, measureErr))
			continue
		}
		if exists && bytes > largestBytes {
			largest = consumer
			largestBytes = bytes
		}
	}
	if largest.label != "" {
		r.Message += fmt.Sprintf("; largest measured consumer: %s (%s) at %s", largest.label, largest.path, formatGB(largestBytes))
	} else {
		r.Message += "; no tracked Go build consumer could be measured"
	}
	return r
}

// CanFix is false because Go cache eviction is unsafe while builds may be
// active and cache ownership belongs to the host operator.
func (c *HostDiskSpaceCheck) CanFix() bool { return false }

// Fix is a no-op; see CanFix.
func (c *HostDiskSpaceCheck) Fix(_ *CheckContext) error { return nil }

func goCachePath() string {
	if path := os.Getenv("GOCACHE"); path != "" {
		return path
	}
	if base, err := os.UserCacheDir(); err == nil {
		return filepath.Join(base, "go-build")
	}
	return filepath.Join(os.TempDir(), "go-build")
}

func goTempPath() string {
	if path := os.Getenv("GOTMPDIR"); path != "" {
		return path
	}
	return "/var/tmp"
}
