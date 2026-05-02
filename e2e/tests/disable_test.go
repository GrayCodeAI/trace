//go:build e2e

package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/e2e/trace"
	"github.com/GrayCodeAI/trace/e2e/testutil"
	"github.com/stretchr/testify/assert"
)

// TestTraceDisable: after running `trace disable`, commits should not
// produce checkpoints or trailers.
func TestTraceDisable(t *testing.T) {
	testutil.ForEachAgent(t, 1*time.Minute, func(t *testing.T, s *testutil.RepoState, ctx context.Context) {
		trace.Disable(t, s.Dir)

		if err := os.MkdirAll(filepath.Join(s.Dir, "docs"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(s.Dir, "docs", "manual.md"), []byte("# Manual\n"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}

		s.Git(t, "add", "docs/")
		s.Git(t, "commit", "-m", "Commit after disable")

		// Give post-commit hook time to fire (if it were going to).
		time.Sleep(5 * time.Second)

		testutil.AssertCheckpointNotAdvanced(t, s)

		trailer := testutil.GetCheckpointTrailer(t, s.Dir, "HEAD")
		assert.Empty(t, trailer, "disabled repo should not add checkpoint trailer")
	})
}
