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

// lockFileImpl is an implementation of lock using POSIX locks via flock().
//
// flock() locks are released when *any* file handle to the locked file is
// closed. To address this, we hold actual file handles globally. Attempts to
// acquire a file lock first check to see if there is already a global entity
// holding the lock (fail), then attempt to acquire the lock at a filesystem
// level.
func lockFileImpl(path string) (Lock, error) {
	return globalPosixLockState.lockFileImpl(path)
}

// posixLockState maintains an internal state of filesystem locks.
//
// For runtime usage, this is maintained in the global variable,
// globalPosixLockState.
type posixLockState struct {
	sync.RWMutex
	held map[uint64]*os.File
}

func (pls *posixLockState) lockFileImpl(path string) (Lock, error) {
	fd, err := getOrCreateLockFile(path)
	if err != nil {
		return nil, err
	}

	st, err := fd.Stat()
	if err != nil {
		fd.Close()
		return nil, err
	}
	stat := st.Sys().(*syscall.Stat_t)

	// Do we already have a lock on this file?

	pls.RLock()
	has := pls.held[stat.Ino]
	pls.RUnlock()

	if has != nil {
		// Some other code path within our process already holds the lock.
		fd.Close()
		return nil, ErrLockFailed
	}

	// Attempt to register the lock.
	pls.Lock()
	defer pls.Unlock()

	// Check again, with write lock held.
	if has := pls.held[stat.Ino]; has != nil {
		fd.Close()
		return nil, ErrLockFailed
	}

	if pls.held == nil {
		pls.held = make(map[uint64]*os.File)
	}
	pls.held[stat.Ino] = fd
	return &posixLock{pls, stat.Ino}, nil
}

func getOrCreateLockFile(path string) (*os.File, error) {
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
				// Successfully created the new file.
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

type posixLock struct {
	pls *posixLockState
	ino uint64
}

func (l *posixLock) Unlock() error {
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