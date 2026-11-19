package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// machinistBin holds a machinist built from this tree, first on PATH for every
// test, so a step that calls machinist agent-log runs the real command.
var machinistBin string

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "machinist-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(directory, "machinist"), "github.com/owainlewis/machinist/cmd/machinist")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build machinist:", err)
		os.Exit(1)
	}
	machinistBin = directory
	code := m.Run()
	_ = os.RemoveAll(directory)
	os.Exit(code)
}
