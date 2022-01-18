// Copyright 2017 by Dan Jacques. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin || freebsd || netbsd || openbsd || solaris || aix
// +build darwin freebsd netbsd openbsd solaris aix

package fslock

import (
	"os"

	"golang.org/x/sys/unix"
)

// flockLock is an implementation of lock using POSIX locks via flock().
//
// flock() locks are released when *any* file handle to the locked file is
// closed. To address this, we hold actual file handles globally. Attempts to
// acquire a file lock first check to see if there is already a global entity
// holding the lock (fail), then attempt to acquire the lock at a filesystem
// level.
func flockLock(l *L, fd *os.File) error {
	// Use "flock()" to get a lock on the file.
	//
	lockcmd := unix.F_SETLK
	lockstr := unix.Flock_t{
		Type: unix.F_WRLCK, Start: 0, Len: 0, Whence: 1,
	}

	if l.Shared {
		lockstr.Type = unix.F_RDLCK
	}

	if err := unix.FcntlFlock(fd.Fd(), lockcmd, &lockstr); err != nil {
		if errno, ok := err.(unix.Errno); ok {
			switch errno {
			case unix.EWOULDBLOCK:
				// Someone else holds the lock on this file.
				return ErrLockHeld
			default:
				return err
			}
		}
		return err
	}
	return nil
}
