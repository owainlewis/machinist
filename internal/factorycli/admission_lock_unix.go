//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package factorycli

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var errAdmissionLockBusy = errors.New("admission is already in progress")

func lockAdmissionFile(file *os.File, nonblocking bool) error {
	operation := unix.LOCK_EX
	if nonblocking {
		operation |= unix.LOCK_NB
	}
	if err := unix.Flock(int(file.Fd()), operation); err != nil {
		if nonblocking && (errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)) {
			return errAdmissionLockBusy
		}
		return err
	}
	return nil
}

func unlockAdmissionFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func syncAdmissionDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
