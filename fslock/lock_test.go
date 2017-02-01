// Copyright 2017 by Dan Jacques. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fslock

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

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

		const count = 1024
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
func TestMultiProcessing(t *testing.T) {
	t.Parallel()

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

		// TODO: Replace with os.Executable for Go 1.8.
		executable := os.Args[0]

		const count = 256
		cmds := make([]*exec.Cmd, count)

		// Kill all of our processes on cleanup, regardless of success/failure.
		defer func() {
			for _, cmd := range cmds {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}()

		for i := range cmds {
			cmd := exec.Command(executable, "-test.run", "^TestMultiProcessing$")
			cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s", envSentinel, tdir))
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
		fmt.Fprintf(os.Stderr, "failed to send ready signal: %v", err)
		return 1
	}

	// Wait for our signal (signalR closing).
	if _, err := ioutil.ReadAll(signalR); err != nil {
		fmt.Fprintf(os.Stderr, "failed to wait for signal: %v", err)
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
		fmt.Fprintln(os.Stderr, err.Error())
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

	withTempDir(t, "content", func(tdir string) {
		lock := filepath.Join(tdir, "lock")
		h, err := Lock(lock)
		if err != nil {
			t.Fatalf("failed to acquire lock: %v", err)
		}
		if h == nil {
			t.Fatal("lock did not return a Handle")
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
