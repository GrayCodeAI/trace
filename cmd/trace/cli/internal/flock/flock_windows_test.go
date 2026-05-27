//go:build windows

package flock

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAcquire_BasicAcquireRelease(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	release, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	// File should exist
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Errorf("lock file should exist after Acquire: %v", statErr)
	}

	release()
}

func TestAcquire_BlocksConcurrent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	release1, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	defer release1()

	var acquired atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		// This should block until release1 is called.
		rel2, err := Acquire(lockPath)
		if err != nil {
			t.Errorf("second Acquire() error = %v", err)
			return
		}
		acquired.Store(true)
		rel2()
	}()

	// Give the goroutine time to attempt the lock.
	time.Sleep(50 * time.Millisecond)
	if acquired.Load() {
		t.Error("second Acquire should have blocked while first lock is held")
	}

	release1()
	wg.Wait()

	if !acquired.Load() {
		t.Error("second Acquire should have succeeded after release")
	}
}

func TestAcquire_ReleaseAllowsReacquire(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	rel1, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	rel1()

	// Should be able to acquire again immediately.
	rel2, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("second Acquire() after release error = %v", err)
	}
	rel2()
}

func TestAcquire_InvalidPath(t *testing.T) {
	t.Parallel()
	// Use a path with an invalid character for Windows.
	_, err := Acquire("Z:\\nonexistent\\dir\\lock.file")
	if err == nil {
		t.Error("Acquire() should return error for invalid path")
	}
}

func TestAcquire_MultipleGoroutinesContention(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	// Hold the lock from the test goroutine.
	release, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("initial Acquire() error = %v", err)
	}

	const N = 5
	var (
		wg       sync.WaitGroup
		acquired [N]bool
	)

	for i := range N {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rel, err := Acquire(lockPath)
			if err != nil {
				t.Errorf("goroutine %d: Acquire() error = %v", idx, err)
				return
			}
			acquired[idx] = true
			rel()
		}(i)
	}

	// Give goroutines time to queue on the lock.
	time.Sleep(100 * time.Millisecond)

	// No goroutine should have acquired yet.
	for i := range N {
		if acquired[i] {
			t.Errorf("goroutine %d acquired lock while it was still held", i)
		}
	}

	// Release — all goroutines should drain through sequentially.
	release()
	wg.Wait()

	for i := range N {
		if !acquired[i] {
			t.Errorf("goroutine %d never acquired the lock", i)
		}
	}
}

// TestAcquire_ProcessExitReleases verifies that the Windows kernel releases
// the LockFileEx lock when a child process exits. The child re-execs this
// binary with FLOCK_TEST_CHILD=1, acquires the lock, and exits. The parent
// then re-acquires the same path.
func TestAcquire_ProcessExitReleases(t *testing.T) {
	t.Parallel()

	if os.Getenv("FLOCK_TEST_CHILD") == "1" {
		path := os.Getenv("FLOCK_TEST_PATH")
		rel, err := Acquire(path)
		if err != nil {
			os.Stderr.WriteString("CHILD_FAIL: " + err.Error())
			os.Exit(1)
		}
		_ = rel
		os.Stderr.WriteString("CHILD_ACQUIRED")
		os.Exit(0)
	}

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=TestAcquire_ProcessExitReleases", "-test.v")
	cmd.Env = append(
		os.Environ(),
		"FLOCK_TEST_CHILD=1",
		"FLOCK_TEST_PATH="+lockPath,
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "child failed: %s", out)
	require.Contains(t, string(out), "CHILD_ACQUIRED",
		"child must have acquired; got: %s", out)

	// After the child exits the kernel releases the lock.
	release, err := Acquire(lockPath)
	require.NoError(t, err,
		"parent must acquire after child exit; child output: %s", out)
	release()
}
