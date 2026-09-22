//go:build darwin || linux

package runner

import (
	"errors"
	"runtime"
	"syscall"
	"testing"
)

func TestIgnorableProcessTreeTerminationError(t *testing.T) {
	probeCalled := false
	probe := func() error {
		probeCalled = true
		return nil
	}
	if !ignorableProcessTreeTerminationError(syscall.ESRCH, probe) {
		t.Fatal("ESRCH should be ignored")
	}
	if probeCalled {
		t.Fatal("ESRCH should not probe the process group")
	}
	for _, test := range []struct {
		name     string
		probeErr error
		want     bool
	}{
		{name: "group gone", probeErr: syscall.ESRCH, want: runtime.GOOS == "darwin"},
		{name: "group accessible", probeErr: nil},
		{name: "group inaccessible", probeErr: syscall.EPERM},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ignorableProcessTreeTerminationError(syscall.EPERM, func() error { return test.probeErr }); got != test.want {
				t.Fatalf("EPERM ignored = %t, want %t on %s", got, test.want, runtime.GOOS)
			}
		})
	}
	if ignorableProcessTreeTerminationError(errors.New("unexpected"), probe) {
		t.Fatal("unexpected error should be preserved")
	}
}

func TestDarwinTerminationWaitsForGroupToDisappear(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin process group cleanup")
	}
	calls := 0
	if !ignorableProcessTreeTerminationError(syscall.EPERM, func() error {
		calls++
		if calls == 1 {
			return syscall.EPERM
		}
		return syscall.ESRCH
	}) {
		t.Fatal("disappeared process group should be ignored")
	}
	if calls != 2 {
		t.Fatalf("probes = %d", calls)
	}
}
