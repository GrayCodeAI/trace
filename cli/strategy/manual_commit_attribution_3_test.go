package strategy

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWarnIfAttributionDiverged_MultipleDivergentSessions_FlagsAllOnce verifies that
// when multiple sessions have attribution divergence, the stderr warning is printed
// exactly once per call and the DivergenceNoticeShown flag is persisted on every
// divergent session — not just the first. The previous implementation broke out of the
// loop after flagging the first session, which caused the "show-once" warning to
// re-trigger on later prepare-commit-msg invocations for each additional divergent
// session.
func TestWarnIfAttributionDiverged_MultipleDivergentSessions_FlagsAllOnce(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	s := &ManualCommitStrategy{}

	now := time.Now()
	sessions := []*SessionState{
		{
			SessionID:             "diverged-a",
			BaseCommit:            strings.Repeat("a", 40),
			AttributionBaseCommit: strings.Repeat("b", 40),
			StartedAt:             now,
		},
		{
			SessionID:             "diverged-b",
			BaseCommit:            strings.Repeat("c", 40),
			AttributionBaseCommit: strings.Repeat("d", 40),
			StartedAt:             now,
		},
	}
	for _, sess := range sessions {
		require.NoError(t, s.saveSessionState(context.Background(), sess))
	}

	var buf bytes.Buffer
	oldWriter := stderrWriter
	stderrWriter = &buf
	defer func() { stderrWriter = oldWriter }()

	s.warnIfAttributionDiverged(context.Background(), sessions)

	require.Equal(t, 1, strings.Count(buf.String(), "trace: session attribution diverged"),
		"warning must print exactly once even with multiple divergent sessions, got:\n%s", buf.String())

	for _, sess := range sessions {
		require.True(t, sess.DivergenceNoticeShown,
			"DivergenceNoticeShown must be set on every divergent session (session %s)",
			sess.SessionID)

		// The flag must also be persisted to disk — the whole point of "show-once"
		// is cross-invocation suppression. An in-memory-only mutation would let the
		// warning re-fire on the next prepare-commit-msg.
		reloaded, err := s.loadSessionState(context.Background(), sess.SessionID)
		require.NoError(t, err)
		require.NotNil(t, reloaded, "session %s should be persisted", sess.SessionID)
		require.True(t, reloaded.DivergenceNoticeShown,
			"DivergenceNoticeShown must be persisted to disk for session %s", sess.SessionID)
	}

	// Second call on the same slice must print nothing — flags are already set.
	buf.Reset()
	s.warnIfAttributionDiverged(context.Background(), sessions)
	require.Empty(t, buf.String(),
		"warning must stay silent on subsequent calls once every divergent session has been flagged")
}
