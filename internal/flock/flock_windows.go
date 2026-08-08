//go:build windows

package flock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// pollInterval is how often the bounded AcquireContext path retries a
// non-blocking lock attempt when the lock is held by another process.
const pollInterval = 25 * time.Millisecond

// Acquire takes an exclusive lock on path via Windows LockFileEx. The
// returned release unlocks and closes the file. Callers must invoke release
// exactly once.
func Acquire(path string) (release func(), err error) {
	return AcquireContext(context.Background(), path)
}

// AcquireContext behaves like Acquire but honors ctx. When ctx carries a
// deadline it polls a non-blocking lock attempt until the lock is acquired or
// the deadline/cancellation fires, returning a wrapped ctx.Err() on timeout.
// When ctx has no deadline it takes the same blocking kernel path as Acquire.
func AcquireContext(ctx context.Context, path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // caller is responsible for path validation
	if err != nil {
		return nil, fmt.Errorf("open flock: %w", err)
	}

	// Fast path: no deadline -> block in the kernel exactly like the historical
	// Acquire.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		overlapped := new(windows.Overlapped)
		if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock flock: %w", err)
		}
		return func() {
			_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, overlapped)
			_ = f.Close()
		}, nil
	}

	// Bounded path: poll a non-blocking lock until acquired or ctx is done.
	for {
		overlapped := new(windows.Overlapped)
		lockErr := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
		if lockErr == nil {
			return func() {
				_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, overlapped)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(lockErr, windows.ERROR_LOCK_VIOLATION) {
			_ = f.Close()
			return nil, fmt.Errorf("lock flock: %w", lockErr)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("flock: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}
