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

func TestHostDiskSpaceCheck_TheTightestFilesystemDecides(t *testing.T) {
	devices := map[string]uint64{"/": 1, "/var/tmp": 1, "/data/go-build": 2, "/data/gotmp": 2}
	sizes := map[string]int64{"/data/go-build": 5 << 30, "/data/gotmp": 1 << 30, "/var/tmp": 90 << 30}
	for _, tc := range []struct {
		name               string
		rootFree, dataFree int64
		want, notWant      []string
	}{
		{
			"off root", 100 << 30, 10 << 30,
			[]string{"10.00 GB free on /data/go-build", "GOCACHE"},
			[]string{"TMP ("},
		},
		{
			"on root", 10 << 30, 100 << 30,
			[]string{"10.00 GB free on /", "TMP (/var/tmp)"},
			[]string{"GOCACHE"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := &HostDiskSpaceCheck{
				rootPath:     "/",
				minFreeBytes: 40 << 30,
				freeBytes: func(path string) (int64, error) {
					switch path {
					case "/":
						return tc.rootFree, nil
					case "/data/go-build":
						return tc.dataFree, nil
					default:
						t.Fatalf("statfs path = %q, want one call per filesystem", path)
						return 0, nil
					}
				},
				deviceOf: func(path string) (uint64, error) {
					if dev, ok := devices[path]; ok {
						return dev, nil
					}
					return 0, errors.New("no such path")
				},
				consumers: []hostDiskConsumer{
					{label: "GOCACHE", path: "/data/go-build"},
					{label: "GOTMPDIR", path: "/data/gotmp"},
					{label: "TMP", path: "/var/tmp"},
				},
				measureDir: func(path string) (int64, bool, error) {
					return sizes[path], true, nil
				},
			}

			result := check.Run(&CheckContext{CityPath: "/city"})
			if result.Status != StatusError {
				t.Fatalf("status = %v, want StatusError (%s)", result.Status, result.Message)
			}
			for _, want := range tc.want {
				if !strings.Contains(result.Message, want) {
					t.Errorf("message = %q, want %q", result.Message, want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(result.Message, notWant) {
					t.Errorf("message = %q names %q, which is on another filesystem", result.Message, notWant)
				}
			}
		})
	}
}

func TestNewHostDiskSpaceCheck_DeclaredEnvBeatsProcessEnv(t *testing.T) {
	t.Setenv("GOCACHE", "/process/go-build")
	t.Setenv("GOTMPDIR", "/process/gotmp")
	declared := NewHostDiskSpaceCheck(nil, map[string]string{"GOCACHE": "/data/go-build", "GOTMPDIR": "/data/gotmp"})
	inherited := NewHostDiskSpaceCheck(nil, nil)
	for i, want := range []string{"/data/go-build", "/data/gotmp"} {
		if got := declared.consumers[i].path; got != want {
			t.Errorf("declared consumer %d = %q, want %q", i, got, want)
		}
	}
	for i, want := range []string{"/process/go-build", "/process/gotmp"} {
		if got := inherited.consumers[i].path; got != want {
			t.Errorf("inherited consumer %d = %q, want %q", i, got, want)
		}
	}
}
