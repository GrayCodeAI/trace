package strategy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyPushOutput(t *testing.T) {
	t.Parallel()

	t.Run("protected-ref wins over 'rejected' keyword", func(t *testing.T) {
		t.Parallel()
		output := "remote: error: GH013\n! [remote rejected] v1 -> v1"

		var perr *protectedRefError
		require.ErrorAs(t, classifyPushOutput(output), &perr)
		assert.Equal(t, output, perr.output)
	})

	t.Run("non-fast-forward maps to NFF error", func(t *testing.T) {
		t.Parallel()
		err := classifyPushOutput("! [rejected] v1 -> v1 (non-fast-forward)")

		var perr *protectedRefError
		assert.NotErrorAs(t, err, &perr)
		require.ErrorIs(t, err, errNonFastForward)
		assert.EqualError(t, err, "non-fast-forward")
	})

	t.Run("fetch-first maps to NFF error", func(t *testing.T) {
		t.Parallel()
		err := classifyPushOutput("!\trefs/heads/main:refs/heads/main\t[rejected] (fetch first)")

		assert.ErrorIs(t, err, errNonFastForward)
	})

	t.Run("generic rejected output stays generic", func(t *testing.T) {
		t.Parallel()
		err := classifyPushOutput("remote: rejected credentials")

		require.Error(t, err)
		require.NotErrorIs(t, err, errNonFastForward)
		assert.ErrorContains(t, err, "push failed: remote: rejected credentials")
	})

	t.Run("other output is wrapped as push failed", func(t *testing.T) {
		t.Parallel()
		err := classifyPushOutput("fatal: Could not resolve host")
		assert.ErrorContains(t, err, "push failed: fatal: Could not resolve host")
	})

	t.Run("empty output preserves push error", func(t *testing.T) {
		t.Parallel()
		pushErr := errors.New("exit status 128")
		err := classifyPushFailure(context.Background(), "", pushErr)

		require.Error(t, err)
		require.ErrorIs(t, err, pushErr)
		assert.ErrorContains(t, err, "push failed")
	})
}

func TestPrintProtectedRefBlock(t *testing.T) {
	t.Parallel()

	t.Run("remote-name target", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		printProtectedRefBlock(&buf, "trace/checkpoints/v1", "origin")

		out := buf.String()
		for _, want := range []string{"BLOCKED", "trace/checkpoints/v1", "e.g. GH013", "trace/*", "checkpoints are saved locally", "checkpoint_remote"} {
			assert.Contains(t, out, want)
		}
		banner := strings.Repeat("=", 20)
		assert.GreaterOrEqual(t, strings.Count(out, banner), 2, "block must be bracketed by banner lines")
	})

	t.Run("URL target is masked", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		printProtectedRefBlock(&buf, "trace/checkpoints/v1", "git@github.com:org/repo.git")

		out := buf.String()
		assert.Contains(t, out, displayPushTarget("git@github.com:org/repo.git"))
		assert.NotContains(t, out, "git@github.com:org/repo.git")
	})
}
