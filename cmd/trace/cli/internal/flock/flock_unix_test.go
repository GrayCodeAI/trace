//go:build unix

package flock

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	_, err := Acquire("/nonexistent/dir/lock.file")
	if err == nil {
		t.Error("Acquire() should return error for invalid path")
	}
}
