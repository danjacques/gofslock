// Copyright 2022 by Dan Jacques. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || android

package fslock

import (
	"os"

	"golang.org/x/sys/unix"
)

// flock_lock is an implementation of lock using Linux flock().
//
// A process may only hold one type of lock (shared or exclusive) on a file.
// Subsequent flock() calls on an already locked file will convert an existing
// lock to the new lock mode. Attempts to acquire a file lock first check to
// see if there is already a global entity holding the lock (fail), then attempt
// to acquire the lock at a filesystem level.
//
// We can't use POSIX FcntlFlock because Linux has a bug in it's implementation
// which different from the documented behaviour. If the process calling execve
// is not the last thread of the process, the lock will be released even without
// FD_CLOEXEC flag.
// See also: crbug.com/1276120
func flock_lock(l *L, fd *os.File) error {
	// Use "flock()" to get a lock on the file.
	//
	// LOCK_EX: Exclusive lock
	// LOCK_NB: Non-blocking.
	flags := unix.LOCK_NB

	if l.Shared {
		flags |= unix.LOCK_SH
	} else {
		flags |= unix.LOCK_EX
	}

	if err := unix.Flock(int(fd.Fd()), flags); err != nil {
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
