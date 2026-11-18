//go:build darwin || linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinaryPrintsVersion(t *testing.T) {
	binary := buildBinary(t)
	output, err := exec.Command(binary, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("version: %v: %s", err, output)
	}
	if strings.TrimSpace(string(output)) != "dev" {
		t.Fatalf("version = %q", output)
	}
}

func TestBinaryInitWritesConfiguration(t *testing.T) {
	binary := buildBinary(t)
	directory := t.TempDir()
	command := exec.Command(binary, "init")
	command.Dir = directory
	command.Env = append(os.Environ(), "HOME="+t.TempDir())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init: %v: %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(directory, "config.yaml")); err != nil {
		t.Fatal(err)
	}
}

func buildBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "machinist")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build machinist: %v: %s", err, output)
	}
	return binary
}
