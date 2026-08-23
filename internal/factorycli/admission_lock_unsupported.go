//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package factorycli

import (
	"errors"
	"os"
)

var errAdmissionLockBusy = errors.New("admission is already in progress")

func lockAdmissionFile(*os.File, bool) error {
	return errors.New("durable implicit Build request keys require a Unix platform")
}

func unlockAdmissionFile(*os.File) error { return nil }

func syncAdmissionDirectory(string) error {
	return errors.New("durable implicit Build request keys require a Unix platform")
}
