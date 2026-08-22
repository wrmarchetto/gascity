package toolpath

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveOAPICodegenPrefersPinnedInstall(t *testing.T) {
	pinned := filepath.Join("/gopath", "bin", "oapi-codegen")
	path, err := resolveOAPICodegen(
		func() (string, error) { return "/gopath", nil },
		func(name string) (string, error) {
			if name == pinned {
				return pinned, nil
			}
			return "", errors.New("PATH lookup must not run after the pinned install resolves")
		},
	)
	if err != nil {
		t.Fatalf("resolve oapi-codegen: %v", err)
	}
	if path != pinned {
		t.Fatalf("resolved path = %q, want pinned %q", path, pinned)
	}
}

func TestResolveOAPICodegenFallsBackToPATH(t *testing.T) {
	path, err := resolveOAPICodegen(
		func() (string, error) { return "/gopath", nil },
		func(name string) (string, error) {
			switch name {
			case filepath.Join("/gopath", "bin", "oapi-codegen"):
				return "", errors.New("not installed at pinned path")
			case "oapi-codegen":
				return "/custom/bin/oapi-codegen", nil
			default:
				return "", errors.New("unexpected lookup")
			}
		},
	)
	if err != nil {
		t.Fatalf("resolve oapi-codegen: %v", err)
	}
	if path != "/custom/bin/oapi-codegen" {
		t.Fatalf("resolved path = %q, want PATH fallback", path)
	}
}

func TestResolveOAPICodegenNamesCauseAndRemedyWhenUnreachable(t *testing.T) {
	_, err := resolveOAPICodegen(
		func() (string, error) { return "/gopath", nil },
		func(string) (string, error) { return "", errors.New("not found") },
	)
	if err == nil {
		t.Fatal("resolve oapi-codegen succeeded when neither lookup could find it")
	}
	for _, want := range []string{"oapi-codegen", "go env GOPATH", "make install-tools", "PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing-tool error = %q, want it to name %q", err, want)
		}
	}
}

func TestOAPICodegenPinnedInstallMatchesMakefile(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	for _, want := range []string{
		"BIN_DIR := $(shell go env GOPATH)/bin",
		"if [ ! -x \"$(BIN_DIR)/oapi-codegen\" ]; then",
		"GOBIN=$(BIN_DIR) go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.6.0",
	} {
		if !strings.Contains(string(makefile), want) {
			t.Errorf("Makefile must declare %q so its oapi-codegen install matches this package's GOPATH/bin resolution", want)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root")
		}
		dir = parent
	}
}
