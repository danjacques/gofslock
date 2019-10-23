// Copyright 2017 by Dan Jacques. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fslock

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var logSubprocess = flag.Bool("test.logsubprocess", false, "Enable verbose subprocess logging.")

const logSubprocessEnv = "_FSLOCK_LOG_SUBPROCESS"

func logf(fmt string, args ...interface{}) {
	if v := os.Getenv(logSubprocessEnv); v != "" {
		log.Printf(fmt, args...)
	}
}

func withTempDir(t *testing.T, prefix string, fn func(string)) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	tdir, err := ioutil.TempDir(wd, prefix)
	if err != nil {
		t.Fatalf("failed to create temporary directory in [%s]: %v", wd, err)
	}
	defer func() {
		if err := os.RemoveAll(tdir); err != nil {
			t.Logf("failed to clean up temporary directory [%s]: %v", tdir, err)
		}
	}()
	fn(tdir)
}

// TestConcurrent tests file locking within the same process using concurrency
// (via goroutines).
//
// For this to really be effective, the test should be run with "-race", since
// it's *possible* that all of the goroutines end up cooperating in spite of a
// bug.
func TestConcurrent(t *testing.T) {
	t.Parallel()

	withTempDir(t, "concurrent", func(tdir string) {
		value := 0
		lock := filepath.Join(tdir, "lock")

		const count = 100
		startC := make(chan struct{})
		doneC := make(chan error, count)

		// Individual test function, run per goroutine.
		blocker := func() error {
			time.Sleep(time.Millisecond)
			return nil
		}
		doTest := func() error {
			return WithBlocking(lock, blocker, func() error {
				value++
				return nil
			})
		}

		for i := 0; i < count; i++ {
			go func() {
				var err error
				defer func() {
					doneC <- err
				}()

				// Wait for the start signal, then run the test.
				<-startC
				err = doTest()
			}()
		}

		// Start our test.
		close(startC)

		// Reap errors.
		errs := make([]error, 0, count)
		for i := 0; i < count; i++ {
			if err := <-doneC; err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			errList := make([]string, len(errs))
			for i, err := range errs {
				errList[i] = err.Error()
			}
			t.Fatalf("encountered %d error(s):\n%s", len(errs), strings.Join(errList, "\n"))
		}
		if value != count {
			t.Fatalf("value doesn't match expected (%d != %d)", value, count)
		}
	})
}

// TestMultiProcessing tests access from multiple separate processes.
//
// The main process creates an output file, seeded with the value "0". It then
// spawns a number of subprocesses (re-executions of this test program with
// a "you're a subprocess" enviornment variable set). Each subprocess acquires
// the lock, reads the output file, increments its value by 1, and writes the
// result.
//
// To maximize contention, we spawn all of our subprocesses first, having each
// block on a signal. When each spawns, it will signal that it's ready. Then,
// the main process will signal that it should start.
//
// Success is if all of the subprocesses succeeded and the output file has the
// correct value.
//
// NOTE: We don't run this test in parallel because it can tax CI resources.
func TestMultiProcessing(t *testing.T) {
	getFiles := func(tdir string) (lock, out string) {
		lock = filepath.Join(tdir, "lock")
		out = filepath.Join(tdir, "out")
		return
	}

	// Are we a testing process instance, or the main process?
	const envSentinel = "_FSLOCK_TEST_WORKDIR"
	if path := os.Getenv(envSentinel); path != "" {
		// Resolve our signal files.
		signalR := os.Stdin
		respW := os.Stdout

		lock, out := getFiles(path)
		rv := testMultiProcessingSubprocess(lock, out, respW, signalR)
		if _, err := respW.Write([]byte{rv}); err != nil {
			// Raise an error in the parent process on Wait().
			fmt.Printf("failed to write result (%d): %v\n", rv, err)
			os.Exit(1)
		}
		os.Exit(0)
		return
	}

	// TODO: Replace with os.Executable for Go 1.8.
	executable := os.Args[0]

	// This pipe will be used to signal that the processes should start the test.
	signalR, signalW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create signal IPC pipe: %v", err)
	}
	defer signalR.Close()
	defer signalW.Close()

	respR, respW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create response IPC pipe: %v", err)
	}
	defer respR.Close()
	defer respW.Close()

	withTempDir(t, "multiprocessing", func(tdir string) {
		// Seed our initial file.
		_, out := getFiles(tdir)
		if err := ioutil.WriteFile(out, []byte("0"), 0664); err != nil {
			t.Fatalf("failed to write initial output file value: %v", err)
		}
		t.Logf("wrote initial output file to [%s]", out)

		const count = 100
		cmds := make([]*exec.Cmd, count)

		// Kill all of our processes on cleanup, regardless of success/failure.
		defer func() {
			for _, cmd := range cmds {
				if cmd != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}
		}()

		for i := range cmds {
			env := append(os.Environ(), fmt.Sprintf("%s=%s", envSentinel, tdir))
			if *logSubprocess {
				env = append(env, fmt.Sprintf("%s=1", logSubprocessEnv))
			}

			cmd := exec.Command(executable, "-test.run", "^TestMultiProcessing$")
			cmd.Env = env
			cmd.Stdin = signalR
			cmd.Stdout = respW
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatalf("failed to start subprocess: %v", err)
			}
			cmds[i] = cmd
		}

		// Close our child-side pipe ends.
		signalR.Close()
		respW.Close()

		// Wait for all of thr processes to signal that they're ready.
		for i := 0; i < count; i++ {
			buf := []byte{0}
			switch n, err := respR.Read(buf[:]); {
			case err != nil:
				t.Fatalf("failed to read ready signal: %v", err)
			case n != 1:
				t.Fatal("failed to read ready signal byte")
			}
		}

		// Signal our subprocesses to start!
		if err := signalW.Close(); err != nil {
			t.Fatalf("failed to signal processes to start: %v", err)
		}

		// Consume our responses. Each subprocess will write one byte to "respW"
		// when they finish. That byte will be zero for success, non-zero for
		// failure.
		failures := 0
		for i := 0; i < count; i++ {
			buf := []byte{0}
			switch n, err := respR.Read(buf[:]); {
			case err != nil:
				t.Fatalf("failed to read response: %v", err)
			case n != 1:
				t.Fatal("failed to read response byte")

			default:
				if buf[0] != 0 {
					failures++
				}
			}
		}

		// Wait for our processes to actually exit.
		for _, cmd := range cmds {
			if err := cmd.Wait(); err != nil {
				t.Fatalf("failed to wait for process: %v", err)
			}
		}

		// Report the failure.
		if failures > 0 {
			t.Fatalf("subprocesses reported %d failure(s)", failures)
		}

		// Our "out" file should be "count".
		buf, err := ioutil.ReadFile(out)
		if err != nil {
			t.Fatalf("failed to read output file: %v", err)
		}
		if exp := strconv.Itoa(count); string(buf) != exp {
			t.Fatalf("output file doesn't match expected (%s != %s)", buf, exp)
		}
	})
}

func testMultiProcessingSubprocess(lock, out string, respW io.Writer, signalR io.Reader) byte {
	// Signal that we're ready to start.
	if _, err := respW.Write([]byte{0}); err != nil {
		logf("failed to send ready signal: %v", err)
		return 1
	}

	// Wait for our signal (signalR closing).
	if _, err := ioutil.ReadAll(signalR); err != nil {
		logf("failed to wait for signal: %v", err)
		return 2
	}

	blocker := func() error {
		time.Sleep(time.Millisecond)
		return nil
	}

	var rc byte = 255
	err := WithBlocking(lock, blocker, func() error {
		// We hold the lock. Update our "out" file value by reading/writing  a new
		// number.
		d, err := ioutil.ReadFile(out)
		if err != nil {
			rc = 4
			return fmt.Errorf("failed to read output file: %v\n", err)
		}
		v, err := strconv.Atoi(string(d))
		if err != nil {
			rc = 5
			return fmt.Errorf("invalid number value (%s): %v\n", d, err)
		}
		if err := ioutil.WriteFile(out, []byte(strconv.Itoa(v+1)), 0664); err != nil {
			rc = 6
			return fmt.Errorf("failed to write updated value: %v\n", err)
		}
		return nil
	})
	if err != nil {
		logf("encountered error: %s", err)
		return rc
	}
	return 0
}

// TestBlockingAndContent tests L's Block and Content fields.
//
// It does this by creating one lock goroutine, writing Content to it, then
func TestBlockingAndContent(t *testing.T) {
	t.Parallel()

	withTempDir(t, "content", func(tdir string) {
		lock := filepath.Join(tdir, "lock")
		heldC := make(chan struct{})
		blockedC := make(chan struct{})
		errC := make(chan error)

		// Blocking goroutine: test blocking, try and write content, should not
		// write because first has already done it.
		go func(blockedC chan<- struct{}) {
			// Wait for the first to signal that it has the lock.
			<-heldC

			l := L{
				Path:    lock,
				Content: []byte("Second"),
				Block: func() error {
					// Notify that we've tried and failed to acquire the lock.
					if blockedC != nil {
						close(blockedC)
						blockedC = nil
					}
					time.Sleep(time.Millisecond)
					return nil
				},
			}
			errC <- l.With(func() error { return nil })
		}(blockedC)

		// Acquire lock, write content.
		const expected = "First"
		l := L{
			Path:    lock,
			Content: []byte(expected),
		}
		err := l.With(func() error {
			// Signal that we're holding the lock.
			close(heldC)

			// Wait for our other goroutine to signal that it has tried and failed
			// to acquire the lock.
			<-blockedC

			// Release the lock.
			return nil
		})
		if err != nil {
			t.Fatalf("failed to create lock: %v", err)
		}

		// Wait for our blocker goroutine to finish.
		if err := <-errC; err != nil {
			t.Fatalf("goroutine error: %v", err)
		}

		// Confirm that the content is written, and that it is the first
		// goroutine's content.
		content, err := ioutil.ReadFile(lock)
		if err != nil {
			t.Fatalf("failed to read content: %v", err)
		}
		if !bytes.Equal(content, []byte(expected)) {
			t.Fatalf("content does not match expected (%s != %s)", content, expected)
		}
	})
}

// TestUnlock tests a lock's Unlock function.
func TestUnlock(t *testing.T) {
	t.Parallel()

	withTempDir(t, "unlock", func(tdir string) {
		lock := filepath.Join(tdir, "lock")
		h, err := Lock(lock)
		if err != nil {
			t.Fatalf("failed to acquire lock: %v", err)
		}
		if h == nil {
			t.Fatal("lock did not return a Handle")
		}
		if h.LockFile() == nil {
			t.Error("lock returned a nil file handle")
		}

		if err := h.Unlock(); err != nil {
			t.Fatalf("Unlock returned an error: %v", err)
		}

		var panicVal interface{}
		err = func() error {
			defer func() {
				panicVal = recover()
			}()
			return h.Unlock()
		}()
		if err != nil {
			t.Fatalf("second Unlock returned an error: %v", err)
		}
		if panicVal == nil {
			t.Fatal("second Unlock did not panic")
		}
		t.Logf("panicked with: %v", panicVal)
	})
}

// TestSharedConcurrent tests file locking within the same process using
// concurrency (via goroutines).
//
// For this to really be effective, the test should be run with "-race", since
// it's *possible* that all of the goroutines end up cooperating in spite of a
// bug.
func TestSharedConcurrent(t *testing.T) {
	t.Parallel()

	withTempDir(t, "shared_concurrent", func(tdir string) {
		lock := filepath.Join(tdir, "lock")

		// Each goroutine will obtain a shared lock simultaneously. Once all
		// goroutines hold the lock, another will attempt to exclusively take the
		// lock. We will ensure that this succeeds only after all of the shared
		// locks have been released.
		const count = 100
		var sharedCounter int32
		hasLockC := make(chan struct{}, count)
		waitForEveryoneC := make(chan struct{})
		sharedErrC := make(chan error, count)

		for i := 0; i < count; i++ {
			go func() {
				sharedErrC <- WithShared(lock, func() error {
					atomic.AddInt32(&sharedCounter, 1)

					// Note that we have the lock.
					hasLockC <- struct{}{}

					// Wait until everyone else does, too.
					<-waitForEveryoneC

					atomic.AddInt32(&sharedCounter, -1)
					return nil
				})
			}()
		}

		// Wait for all of the goroutines to hold their shared lock.
		exclusiveTriedAndFailedC := make(chan struct{})
		exclusiveHasLockC := make(chan int32)
		exclusiveErrC := make(chan error)
		for i := 0; i < count; i++ {
			<-hasLockC

			// After the first goroutine holds the lock, start our exclusive lock
			// goroutine.
			if i == 0 {
				go func() {
					attempt := 0
					exclusiveErrC <- WithBlocking(lock, func() error {
						// (Blocker)
						if attempt == 0 {
							close(exclusiveTriedAndFailedC)
						}
						attempt++
						time.Sleep(time.Millisecond)
						return nil
					}, func() error {
						exclusiveHasLockC <- sharedCounter
						return nil
					})
				}()
			}
		}

		// Our shared lock is still being held, waiting for "waitForEveryoneC".
		// Snapshot our shared counter, which should not be in contention.
		if v := int(sharedCounter); v != count {
			t.Errorf("Shared counter has unexpected value: %d", v)
		}

		// Wait for our exclusive lock to try and fail.
		<-exclusiveTriedAndFailedC

		// Let all of our shared locks release.
		close(waitForEveryoneC)
		for i := 0; i < count; i++ {
			if err := <-sharedErrC; err != nil {
				t.Errorf("Shared lock returned error: %s", err)
			}
		}
		close(sharedErrC)

		// Wait for our exclusive lock to finish.
		if v := <-exclusiveHasLockC; v != 0 {
			t.Errorf("Exclusive lock reported non-zero shared counter value: %d", v)
		}
		if err := <-exclusiveErrC; err != nil {
			t.Errorf("Exclusive lock reported error: %s", err)
		}
	})
}

// TestSharedMultiProcessing tests access from multiple separate processes.
//
// We open by holding an exclusive lock, then spawning all of our subprocesses.
// Each subprocess will try and fail to acquire the lock, then write a
// "failed_shared" file to note this failure.
//
// After all "failed" files have been confirmed, we release our exclusive lock.
// At this point, each process will acquire its shared lock, write a
// "has_shared_lock" file to provie that it hols a shared lock, and wait for all
// of the other processes' "has_shared_lock" files to show.
//
// Our main process, meanwhile, is scanning for any "has_shared_lock" files.
// Once it sees one, it attempts to obtain an exclusive lock again. After the
// first exclusive lock failure, it will write a "failed_exclusive" file.
//
// Once a process holding the shared lock observes the "failed_exclusive" file,
// they will terminate.
//
// Finally, the exclusive lock will be obtained and the test will complete.
//
// NOTE: We don't run this test in parallel because it can tax CI resources.
func TestSharedMultiProcessing(t *testing.T) {
	getFiles := func(tdir string) (lock string) {
		lock = filepath.Join(tdir, "lock")
		return
	}

	// Are we a testing process instance, or the main process?
	const envSentinel = "_FSLOCK_TEST_WORKDIR"
	if state := os.Getenv(envSentinel); state != "" {
		parts := strings.SplitN(state, ":", 2)
		if len(parts) != 2 {
			os.Exit(1)
		}
		name, path := parts[0], parts[1]

		lock := getFiles(path)
		rv := testSharedMultiProcessingSubprocess(name, lock, path)
		os.Exit(rv)
		return
	}

	// TODO: Replace with os.Executable for Go 1.8.
	executable := os.Args[0]

	withTempDir(t, "shared_multiprocessing", func(tdir string) {
		const count = 128
		const delay = 10 * time.Millisecond

		lock := getFiles(tdir)

		// Start our exclusive lock monitor goroutine.
		exclusiveLockHeldC := make(chan struct{})
		monitor := func() error {
			err := With(lock, func() error {
				t.Logf("monitor: acquired exclusive lock")

				// Notify that we hold the exclusive lock.
				close(exclusiveLockHeldC)

				// Wait for "failed_shared" files.
				if err := waitForFiles(tdir, "failed_shared", count); err != nil {
					return err
				}
				t.Logf("monitor: observed 'failed_shared' files")

				// Release exclusive lock...
				return nil
			})
			if err != nil {
				return err
			}

			// Wait for "has_shared_lock" files.
			if err := waitForFiles(tdir, "has_shared_lock", count); err != nil {
				return err
			}
			t.Logf("monitor: observed 'has_shared_lock' files")

			// Try and get an exclusive lock. When we fail, write "failed_exclusive"
			// file.
			attempts := 0
			return WithBlocking(lock, func() error {
				t.Logf("monitor: failed to re-acquire exclusive lock (%d)", attempts)
				if attempts == 0 {
					if err := writeFileStamp(tdir, "failed_exclusive", "master"); err != nil {
						return err
					}
				}
				attempts++
				time.Sleep(delay)
				return nil
			}, func() error {
				// All shared locks are released, gained exclusive lock.
				t.Logf("monitor: acquired exclusive lock")
				return nil
			})
		}
		errC := make(chan error)
		go func() {
			errC <- monitor()
		}()

		// Wait for our exclusive lock to be held. Then spawn our subprocesses.
		//
		// In defer, kill any spawned processes on cleanup, regardless of
		// success/failure.
		<-exclusiveLockHeldC

		cmds := make([]*exec.Cmd, count)
		defer func() {
			for _, cmd := range cmds {
				if cmd != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}
		}()

		for i := range cmds {
			env := append(os.Environ(), fmt.Sprintf("%s=%d:%s", envSentinel, i, tdir))
			if *logSubprocess {
				env = append(env, fmt.Sprintf("%s=1", logSubprocessEnv))
			}

			cmd := exec.Command(executable, "-test.run", "^TestSharedMultiProcessing$")
			cmd.Env = env
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatalf("failed to start subprocess: %v", err)
			}
			cmds[i] = cmd
		}

		// Reap all of our subprocesses.
		for _, cmd := range cmds {
			if err := cmd.Wait(); err != nil {
				t.Errorf("failed to wait for process: %v", err)
			}
		}

		// Our exclusive lock should have exited without an error.
		if err := <-errC; err != nil {
			t.Errorf("exclusive lock monitor failed with error: %s", err)
		}
	})
}

func testSharedMultiProcessingSubprocess(name, lock, dir string) int {
	const delay = 10 * time.Millisecond

	attempts := 0
	err := WithSharedBlocking(lock, func() error {
		logf("%s: failed to acquire shared lock (%d)", name, attempts)
		if attempts == 0 {
			if err := writeFileStamp(dir, "failed_shared", name); err != nil {
				return err
			}
		}
		attempts++
		time.Sleep(delay)
		return nil
	}, func() error {
		// We received the shared lock. Write our stamp file noting this.
		logf("%s: acquired shared lock", name)
		if err := writeFileStamp(dir, "has_shared_lock", name); err != nil {
			return err
		}

		// Wait for "failed_exclusive" file.
		if err := waitForFiles(dir, "failed_exclusive", 1); err != nil {
			return err
		}
		logf("%s: observed 'failed_exclusive' file", name)

		return nil
	})
	if err != nil {
		logf("%s: terminating with error: %s", name, err)
		return 1
	}

	logf("%s: terminating successfully", name)
	return 0
}

func scanForFiles(dir, prefix string) (int, error) {
	fileInfos, err := ioutil.ReadDir(dir)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, fi := range fileInfos {
		if strings.HasPrefix(fi.Name(), prefix) {
			count++
		}
	}
	return count, nil
}

func waitForFiles(dir, prefix string, count int) error {
	for {
		num, err := scanForFiles(dir, prefix)
		if err != nil {
			return err
		}
		if num >= count {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func writeFileStamp(dir, prefix, name string) error {
	path := filepath.Join(dir, fmt.Sprintf("%s.%s", prefix, name))
	fd, err := os.Create(path)
	if err != nil {
		return err
	}
	return fd.Close()
}
