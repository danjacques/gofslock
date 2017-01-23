// Copyright 2017 by Dan Jacques. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fslock

import (
	"os"
	"syscall"
)

const errno_ERROR_SHARING_VIOLATION syscall.Errno = 32

func lockImpl(l *L) (Handle, error) {
	fd, created, err := exclusiveGetOrCreateFile(l.Path)
	if err != nil {
		return nil, err
	}

	// If we just created the file, write Contents to it. If this fails, it is
	// non-fatal.
	if created && len(l.Content) > 0 {
		_, _ = fd.Write(l.Content)
	}

	// We own the lock on virtue of having accessed the file exclusively.
	return winLockHandle{fd}, nil
}

type winLockHandle struct {
	fd *os.File
}

func (h winLockHandle) Unlock() error { return h.fd.Close() }

func exclusiveGetOrCreateFile(path string) (*os.File, bool, error) {
	pathp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}

	fd, err := syscall.CreateFile(
		pathp,
		(syscall.GENERIC_READ | syscall.GENERIC_WRITE),
		0,   // No sharing.
		nil, // No security attributes.
		(syscall.OPEN_ALWAYS),
		syscall.FILE_ATTRIBUTE_NORMAL,
		0, // No template file.
	)
	if err != nil {
		if errno, ok := err.(syscall.Errno); ok {
			switch errno {
			case syscall.ERROR_ALREADY_EXISTS:
				// Opened the file, but did not create it.
				return os.NewFile(uintptr(fd), path), false, nil
			case errno_ERROR_SHARING_VIOLATION:
				// We could not open the file because someone else is holding it.
				return nil, false, ErrLockHeld
			default:
				syscall.CloseHandle(fd)
				return nil, false, err
			}
		}
		return nil, false, err
	}

	// Created a new file.
	return os.NewFile(uintptr(fd), path), true, nil
}
