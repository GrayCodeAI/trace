package remote

import (
	"context"
	"os/exec"
	"testing"
)

// Not parallel: uses t.Setenv. Clearing TRACE_CHECKPOINT_TOKEN keeps the test
// hermetic — otherwise newCommand spawns git against the ambient repo.
func TestNewCommand_TerminatesOnCancel(t *testing.T) {
	t.Setenv(CheckpointTokenEnvVar, "")

	cmd, cleanup := newCommand(context.Background(), "push", "origin", "main")
	defer cleanup()

	if cmd.WaitDelay != killWaitDelay {
		t.Errorf("WaitDelay = %v; want %v", cmd.WaitDelay, killWaitDelay)
	}
	if cmd.Cancel == nil {
		t.Error("Cancel = nil; want a cancellation handler that terminates the process")
	}
}

func TestTerminateOnCancel_SetsWaitDelay(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(context.Background(), "git", "status")
	terminateOnCancel(cmd)

	if cmd.WaitDelay != killWaitDelay {
		t.Errorf("WaitDelay = %v; want %v", cmd.WaitDelay, killWaitDelay)
	}
}
