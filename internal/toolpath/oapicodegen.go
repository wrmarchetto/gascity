// Package toolpath resolves repository-owned development tools.
package toolpath

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// OAPICodegenPath returns the oapi-codegen executable installed by this
// repository's Makefile, falling back to an executable on PATH.
func OAPICodegenPath() (string, error) {
	return resolveOAPICodegen(goEnvGOPATH, exec.LookPath)
}

func resolveOAPICodegen(gopath func() (string, error), lookup func(string) (string, error)) (string, error) {
	root, gopathErr := gopath()
	if root = strings.TrimSpace(root); root != "" {
		if path, err := lookup(filepath.Join(root, "bin", "oapi-codegen")); err == nil {
			return path, nil
		}
	}
	if path, err := lookup("oapi-codegen"); err == nil {
		return path, nil
	}
	if gopathErr != nil {
		return "", fmt.Errorf("oapi-codegen resolves nowhere: could not run `go env GOPATH` (%w), then checked PATH; install it with `make install-tools` or add $(go env GOPATH)/bin to PATH", gopathErr)
	}
	return "", fmt.Errorf("oapi-codegen resolves nowhere: checked $(go env GOPATH)/bin/oapi-codegen, then PATH; install it with `make install-tools` or add $(go env GOPATH)/bin to PATH")
}

func goEnvGOPATH() (string, error) {
	out, err := exec.Command("go", "env", "GOPATH").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
