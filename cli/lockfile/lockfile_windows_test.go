//go:build windows

package lockfile_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/lockfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAcquire_ProcessExitReleases verifies kernel auto-release on
// process death. The child re-execs this binary with LOCKFILE_TEST_CHILD=1,
// which acquires the lock and exits — the kernel releases the LockFileEx
// when the handle closes. The parent then re-acquires the same path.
func TestAcquire_ProcessExitReleases(t *testing.T) {
	t.Parallel()

	if os.Getenv("LOCKFILE_TEST_CHILD") == "1" {
		path := os.Getenv("LOCKFILE_TEST_PATH")
		_, err := lockfile.Acquire(path)
		if err != nil {
			os.Stderr.WriteString("CHILD_FAIL: " + err.Error())
			os.Exit(1)
		}
		os.Stderr.WriteString("CHILD_ACQUIRED")
		os.Exit(0)
	}

	path := filepath.Join(t.TempDir(), "test.lock")

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestAcquire_ProcessExitReleases", "-test.v")
	cmd.Env = append(
		os.Environ(),
		"LOCKFILE_TEST_CHILD=1",
		"LOCKFILE_TEST_PATH="+path,
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "child failed: %s", out)
	require.Contains(t, string(out), "CHILD_ACQUIRED", "child must have acquired; got: %s", out)

	lk, err := lockfile.Acquire(path)
	require.NoError(t, err, "parent must acquire after child exit; child output: %s", out)
	t.Cleanup(func() { _ = lk.Release() }) //nolint:errcheck // test cleanup

	assert.Equal(t, os.Getpid(), lockfile.ReadHolderPID(path),
		"PID file should now hold parent's PID, not child's")
}

// TestAcquire_ConcurrentContention verifies that multiple goroutines
// competing for the same lock on Windows serialize correctly: only one
// holder at a time, all others receive ErrLocked, and the lock remains
// healthy after contention.
func TestAcquire_ConcurrentContention(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lock")

	// Hold the lock from the test goroutine.
	lk, err := lockfile.Acquire(path)
	require.NoError(t, err)

	const N = 5
	var (
		wg       sync.WaitGroup
		errCount atomic.Int32
	)

	for i := range N {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			lk2, acquireErr := lockfile.Acquire(path)
			if acquireErr != nil {
				if assert.ErrorIs(t, acquireErr, lockfile.ErrLocked) {
					errCount.Add(1)
				}
				return
			}
			t.Errorf("goroutine %d acquired lock while it was still held", idx)
			_ = lk2.Release() //nolint:errcheck // test cleanup
		}(i)
	}

	wg.Wait()
	assert.Equal(t, int32(N), errCount.Load(),
		"all %d goroutines should have received ErrLocked", N)

	// Release and verify the lock is still healthy.
	require.NoError(t, lk.Release())

	lk3, err := lockfile.Acquire(path)
	require.NoError(t, err, "lock must be acquirable after contention")
	t.Cleanup(func() { _ = lk3.Release() }) //nolint:errcheck // test cleanup
}

// TestWithTimeout_ConcurrentContentionOnWindows verifies that
// WithTimeout on Windows correctly retries and serializes access
// when multiple goroutines compete for the same lock.
func TestWithTimeout_ConcurrentContentionOnWindows(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lock")

	// Hold the lock from the test goroutine.
	lk, err := lockfile.Acquire(path)
	require.NoError(t, err)

	var (
		wg      sync.WaitGroup
		success atomic.Int32
	)

	const N = 3
	for i := range N {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			timeoutErr := lockfile.WithTimeout(
				context.Background(), path, 500*time.Millisecond,
				func() error {
					success.Add(1)
					return nil
				},
			)
			// Should fail because we hold the lock past 500ms.
			if timeoutErr != nil {
				assert.ErrorIs(t, timeoutErr, lockfile.ErrLocked)
			}
		}(i)
	}

	wg.Wait()
	assert.Equal(t, int32(0), success.Load(),
		"no goroutine should have succeeded while lock was held")

	require.NoError(t, lk.Release())

	// Now release and verify WithTimeout succeeds.
	require.NoError(t, lockfile.WithTimeout(
		context.Background(), path, time.Second,
		func() error {
			assert.Equal(t, os.Getpid(), lockfile.ReadHolderPID(path))
			return nil
		},
	))
}

// TestWithTimeout_ContextCancellationOnWindows verifies that cancelling
// the context causes WithTimeout to return promptly on Windows.
func TestWithTimeout_ContextCancellationOnWindows(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lock")

	lk, err := lockfile.Acquire(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lk.Release() }) //nolint:errcheck // test cleanup

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- lockfile.WithTimeout(ctx, path, 10*time.Second, func() error {
			return nil
		})
	}()

	// Cancel while WithTimeout is waiting.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("WithTimeout did not return after context cancellation")
	}
}

// TestReadHolderPID_StalePID verifies that ReadHolderPID returns the
// PID written in the file even when the process no longer exists. The
// PID is advisory/diagnostic only — callers must check process liveness
// themselves.
func TestReadHolderPID_StalePID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lock")

	// Write a PID that is almost certainly not running.
	stalePID := 999999999
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("%d\n", stalePID)), 0o600))

	assert.Equal(t, stalePID, lockfile.ReadHolderPID(path),
		"ReadHolderPID should return the written PID regardless of process liveness")
}
