package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	deviceOf     func(string) (uint64, error)
}

// NewHostDiskSpaceCheck creates a check for the filesystem mounted at / and
// every filesystem a Go build consumer lives on, measured through freeBytes
// (HostRootFreeBytes in production). Doctor must not inspect CityPath: city
// state can reside on one filesystem while Go's build paths fill another.
// declared is the city's [workspace] env, which names where its sessions
// build and which this process need not have inherited.
func NewHostDiskSpaceCheck(freeBytes func(string) (int64, error), declared map[string]string) *HostDiskSpaceCheck {
	return &HostDiskSpaceCheck{
		rootPath:     "/",
		minFreeBytes: hostDiskMinFreeBytes,
		freeBytes:    freeBytes,
		consumers: []hostDiskConsumer{
			{label: "GOCACHE", path: goCachePath(declared)},
			{label: "GOTMPDIR", path: goTempPath(declared)},
		},
		measureDir: duDirBytes,
		deviceOf:   hostPathDevice,
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
		freeBytes = HostRootFreeBytes
	}

	deviceOf := c.deviceOf
	if deviceOf == nil {
		deviceOf = hostPathDevice
	}

	// One reading per filesystem: the root, then each consumer on a device
	// not yet seen. A consumer whose device cannot be read holds nothing yet.
	paths := []string{rootPath}
	devices := map[string]uint64{}
	seen := map[uint64]bool{}
	if dev, err := deviceOf(rootPath); err == nil {
		devices[rootPath], seen[dev] = dev, true
	}
	for _, consumer := range c.consumers {
		if consumer.path == "" {
			continue
		}
		dev, err := deviceOf(consumer.path)
		if err != nil {
			continue
		}
		devices[consumer.path] = dev
		if !seen[dev] {
			seen[dev] = true
			paths = append(paths, consumer.path)
		}
	}

	tightPath, free := "", int64(0)
	var readings []string
	for _, path := range paths {
		got, err := freeBytes(path)
		if err != nil {
			if path == rootPath {
				r.Status = StatusWarning
				r.Message = fmt.Sprintf("measure free space on %s: %v", rootPath, err)
				r.FixHint = "verify host filesystem access, then re-run gc doctor"
				return r
			}
			r.Details = append(r.Details, fmt.Sprintf("measure free space on %s: %v", path, err))
			continue
		}
		readings = append(readings, fmt.Sprintf("%s free on %s", formatGB(got), path))
		if tightPath == "" || got < free {
			tightPath, free = path, got
		}
	}
	if free >= minFreeBytes {
		r.Status = StatusOK
		r.Message = fmt.Sprintf("%s (reserve %s)", strings.Join(readings, ", "), formatGB(minFreeBytes))
		return r
	}

	r.Status = StatusError
	r.Message = fmt.Sprintf("only %s free on %s (minimum %s for one full test gate)", formatGB(free), tightPath, formatGB(minFreeBytes))
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
		tightDev, tightKnown := devices[tightPath]
		if dev, known := devices[consumer.path]; known && tightKnown && dev != tightDev {
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
		r.Message += fmt.Sprintf("; no tracked Go build consumer on %s could be measured", tightPath)
	}
	return r
}

// CanFix is false because Go cache eviction is unsafe while builds may be
// active and cache ownership belongs to the host operator.
func (c *HostDiskSpaceCheck) CanFix() bool { return false }

// Fix is a no-op; see CanFix.
func (c *HostDiskSpaceCheck) Fix(_ *CheckContext) error { return nil }

func goCachePath(declared map[string]string) string {
	if path := os.ExpandEnv(declared["GOCACHE"]); path != "" {
		return path
	}
	if path := os.Getenv("GOCACHE"); path != "" {
		return path
	}
	if base, err := os.UserCacheDir(); err == nil {
		return filepath.Join(base, "go-build")
	}
	return filepath.Join(os.TempDir(), "go-build")
}

func goTempPath(declared map[string]string) string {
	if path := os.ExpandEnv(declared["GOTMPDIR"]); path != "" {
		return path
	}
	if path := os.Getenv("GOTMPDIR"); path != "" {
		return path
	}
	return "/var/tmp"
}
