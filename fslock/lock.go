// Copyright 2017 by Dan Jacques. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fslock

import (
	"errors"
)

// ErrLockFailed is a sentinel error returned when the lock could not be
// acquired.
var ErrLockFailed = errors.New("failed to get lock")

// Lock is a held lock. It must be released via Unlock when finished.
type Lock interface {
	// Unlock releases the held lock.
	//
	// This can error if the underlying filesystem operations fail. This should
	// not happen unless something has gone externally wrong, or the lock was
	// mishandled.
	Unlock() error
}

func LockFile(path string) (Lock, error) { return fakeLock{}, nil }

type fakeLock struct{}

func (fl fakeLock) Unlock() error { return nil }
