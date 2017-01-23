// Copyright 2017 by Dan Jacques. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build darwin linux freebsd netbsd openbsd android

package fslock

import (
	"fmt"
	"os"
	"sync"
	"syscall"
)

var globalPosixLockState posixLockState

// lockImpl is an implementation of lock using POSIX locks via flock().
//
// flock() locks are released when *any* file handle to the locked file is
// closed. To address this, we hold actual file handles globally. Attempts to
// acquire a file lock first check to see if there is already a global entity
// holding the lock (fail), then attempt to acquire the lock at a filesystem
// level.
func lockImpl(l *L) (Handle, error) {
	return globalPosixLockState.lockImpl(l)
}

// posixLockState maintains an internal state of filesystem locks.
//
// For runtime usage, this is maintained in the global variable,
// globalPosixLockState.
type posixLockState struct {
	sync.RWMutex
	held map[uint64]*os.File
}

func (pls *posixLockState) lockImpl(l *L) (Handle, error) {
	fd, err := getOrCreateLockFile(l.Path, l.Content)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Close "fd". On success, we'll clear "fd", so this will become a no-op.
		if fd != nil {
			fd.Close()
		}
	}()

	st, err := fd.Stat()
	if err != nil {
		return nil, err
	}
	stat := st.Sys().(*syscall.Stat_t)

	// Do we already have a lock on this file?
	pls.RLock()
	has := pls.held[stat.Ino]
	pls.RUnlock()

	if has != nil {
		// Some other code path within our process already holds the lock.
		return nil, ErrLockHeld
	}

	// Attempt to register the lock.
	pls.Lock()
	defer pls.Unlock()

	// Check again, with write lock held.
	if has := pls.held[stat.Ino]; has != nil {
		return nil, ErrLockHeld
	}

	// Use "flock()" to get a lock on the file.
	//
	// LOCK_EX: Exclusive lock
	// LOCK_NB: Non-blocking.
	if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errno, ok := err.(syscall.Errno); ok {
			switch errno {
			case syscall.EWOULDBLOCK:
				// Someone else holds the lock on this file.
				return nil, ErrLockHeld
			default:
				return nil, err
			}
		}
		return nil, err
	}

	if pls.held == nil {
		pls.held = make(map[uint64]*os.File)
	}
	pls.held[stat.Ino] = fd
	fd = nil // Don't Close in defer().
	return &posixLockHandle{pls, stat.Ino}, nil
}

func getOrCreateLockFile(path, content string) (*os.File, error) {
	// Loop until we've either created or opened the file.
	for {
		// Attempt to open the file. This will succeed if the file already exists.
		fd, err := os.OpenFile(path, os.O_RDONLY, 0664)
		switch {
		case err == nil:
			// Successfully opened the file, return handle.
			return fd, nil

		case os.IsNotExist(err):
			// The file doesn't exist. Attempt to exclusively create it.
			//
			// If this fails, the file exists, so we will try opening it again.
			fd, err := os.OpenFile(path, (os.O_CREATE | os.O_EXCL | os.O_RDWR), 0664)
			switch {
			case err == nil:
				// Successfully created the new file. If we have content to write, try
				// and write it.
				if content != "" {
					// Failure to write content is non-fatal.
					_, _ = fd.WriteString(content)
				}
				return fd, err

			case os.IsExist(err):
				// Loop, we will try to open the file.

			default:
				return nil, err
			}

		default:
			return nil, err
		}
	}
}

type posixLockHandle struct {
	pls *posixLockState
	ino uint64
}

func (l *posixLockHandle) Unlock() error {
	if l.pls == nil {
		panic("lock is not held")
	}

	l.pls.Lock()
	defer l.pls.Unlock()

	fd := l.pls.held[l.ino]
	if fd == nil {
		panic(fmt.Errorf("lock for inode %d is not held", l.ino))
	}
	if err := fd.Close(); err != nil {
		return err
	}
	delete(l.pls.held, l.ino)
	return nil
}
