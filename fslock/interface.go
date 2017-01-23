// Copyright 2017 by Dan Jacques. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package fslock is a cross-platform filesystem locking implementation.
//
// fslock aims to implement workable filesystem-based locking semantics. Each
// implementation offers its own nuances, and not all of those nuances are
// addressed by this package.
//
// fslock will work as long as you don't do anything particularly weird, such
// as:
//
//	- Manually forking your Go process.
//	- Circumventing the fslock package and opening and/or manipulating the lock
//	  file directly.
//
// An attempt to take a filesystem lock is non-blocking, and will return
// ErrLockHeld if the lock is already held elsewhere.
package fslock

// Lock acquires a filesystem lock for the given path.
//
// If the lock could not be acquired because it is held by another entity,
// ErrLockHeld will be returned. If an error is encountered while locking,
// that error will be returned.
//
// Lock is a convenience method for L's Lock.
func Lock(path string) (Handle, error) { return LockBlocking(path, nil) }

// LockBlocking acquires a filesystem lock for the given path. If the lock is
// already held, LockBlocking will repeatedly attempt to acquire it using
// the supplied Blocker in between attempts.
//
// If the lock could not be acquired because it is held by another entity,
// ErrLockHeld will be returned. If an error is encountered while locking,
// or an error is returned by b, that error will be returned.
//
// Lock is a convenience method for L's Lock.
func LockBlocking(path string, b Blocker) (Handle, error) {
	l := L{
		Path:  path,
		Block: b,
	}
	return l.Lock()
}

// With is a convenience function to create a lock, execute a function while
// holding that lock, and then release the lock on completion.
//
// See L's With method for details.
func With(path string, fn func() error) error { return WithBlocking(path, nil, fn) }

// WithBlocking is a convenience function to create a lock, execute a function
// while holding that lock, and then release the lock on completion. The
// supplied block function is used to retry (see L's Block field).
//
// See L's With method for details.
func WithBlocking(path string, b Blocker, fn func() error) error {
	l := L{
		Path:  path,
		Block: b,
	}
	return l.With(fn)
}
