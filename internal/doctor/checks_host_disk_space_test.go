package doctor

import (
	"errors"
	"strings"
	"testing"
)

func TestHostDiskSpaceCheck_LowSpaceNamesLargestConsumer(t *testing.T) {
	check := &HostDiskSpaceCheck{
		rootPath:     "/",
		minFreeBytes: 40 << 30,
		freeBytes: func(path string) (int64, error) {
			if path != "/" {
				t.Fatalf("statfs path = %q, want host root", path)
			}
			return 23 << 30, nil
		},
		consumers: []hostDiskConsumer{
			{label: "GOCACHE", path: "/home/test/.cache/go-build"},
			{label: "GOTMPDIR", path: "/var/tmp"},
		},
		measureDir: func(path string) (int64, bool, error) {
			switch path {
			case "/home/test/.cache/go-build":
				return 514 << 30, true, nil
			case "/var/tmp":
				return 30 << 30, true, nil
			default:
				return 0, false, errors.New("unexpected path")
			}
		},
	}

	result := check.Run(&CheckContext{CityPath: "/city-on-another-filesystem"})
	if result.Status != StatusError {
		t.Fatalf("status = %v, want StatusError", result.Status)
	}
	for _, want := range []string{"23.00 GB free on /", "GOCACHE", "/home/test/.cache/go-build", "514.00 GB"} {
		if !strings.Contains(result.Message, want) {
			t.Errorf("message = %q, want %q", result.Message, want)
		}
	}
	if strings.Contains(result.Message, "GOTMPDIR") {
		t.Errorf("message names smaller consumer: %q", result.Message)
	}
}

func TestHostDiskSpaceCheck_UsesHostRootRatherThanCityPath(t *testing.T) {
	check := &HostDiskSpaceCheck{
		rootPath:     "/",
		minFreeBytes: 40 << 30,
		freeBytes: func(path string) (int64, error) {
			if path != "/" {
				t.Fatalf("statfs path = %q, want /", path)
			}
			return 100 << 30, nil
		},
	}

	result := check.Run(&CheckContext{CityPath: "/mnt/city"})
	if result.Status != StatusOK {
		t.Fatalf("status = %v, want StatusOK (%s)", result.Status, result.Message)
	}
}
