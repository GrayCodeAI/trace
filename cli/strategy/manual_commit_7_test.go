package strategy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/go-git/go-git/v6"
	"github.com/stretchr/testify/require"
)

// TestCondenseSession_V2DualWrite verifies that when checkpoints_v2 is enabled,
// CondenseSession writes to both v1 (trace/checkpoints/v1) and v2 refs
// (refs/trace/checkpoints/v2/main and refs/trace/checkpoints/v2/full/current).
func TestCondenseSession_RedactionFailure_DropsTranscriptButWritesMetadata(t *testing.T) {
	originalRedact := redactSessionJSONLBytes
	redactSessionJSONLBytes = func(_ context.Context, _ []byte) (redact.RedactedBytes, error) {
		return redact.RedactedBytes{}, errors.New("forced redaction failure")
	}
	t.Cleanup(func() {
		redactSessionJSONLBytes = originalRedact
	})

	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, "main.go", "package main")
	testutil.GitAdd(t, dir, "main.go")
	testutil.GitCommit(t, dir, "Initial commit")

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	headRef, err := repo.Head()
	require.NoError(t, err)

	t.Chdir(dir)

	s := &ManualCommitStrategy{}
	sessionID := "2026-04-10-test-redaction-failure"

	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	require.NoError(t, os.MkdirAll(metadataDirAbs, 0o755))

	transcript := "{\"type\":\"human\",\"message\":{\"content\":\"hello\"}}\n"
	require.NoError(t, os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(transcript), 0o644))

	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{"main.go"},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 1",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	require.NoError(t, err)

	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.TranscriptPath = filepath.Join(metadataDirAbs, paths.TranscriptFileName)
	state.BaseCommit = headRef.Hash().String()[:7]
	state.AgentType = agent.AgentTypeClaudeCode
	state.FilesTouched = []string{"main.go"}

	checkpointID := id.MustCheckpointID("aa11bb22cc33")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	require.NoError(t, err, "redaction failure should not abort condensation")
	require.NotNil(t, result)

	stores, err := s.getCheckpointStores(context.Background(), repo)
	require.NoError(t, err)

	committed, err := stores.Persistent.List(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, committed)

	found := false
	for _, c := range committed {
		if c.CheckpointID == checkpointID {
			found = true
			break
		}
	}
	require.True(t, found, "checkpoint metadata should be written even when transcript redaction fails")

	_, err = stores.Persistent.ReadSessionContent(context.Background(), checkpointID, 0)
	require.ErrorIs(t, err, checkpoint.ErrNoTranscript, "transcript should be dropped when redaction fails")
}

func TestCommittedFilesExcludingMetadata(t *testing.T) {
	t.Parallel()

	input := map[string]struct{}{
		"docs/blue.md":          {},
		"docs/red.md":           {},
		".trace/settings.json":  {},
		".trace/.gitignore":     {},
		".claude/settings.json": {},
	}

	result := committedFilesExcludingMetadata(input)

	// .trace/ files should be excluded, everything else kept
	resultSet := make(map[string]struct{}, len(result))
	for _, f := range result {
		resultSet[f] = struct{}{}
	}

	require.Contains(t, resultSet, "docs/blue.md")
	require.Contains(t, resultSet, "docs/red.md")
	require.NotContains(t, resultSet, ".trace/settings.json", ".trace/ should be excluded")
	require.NotContains(t, resultSet, ".trace/.gitignore", ".trace/ should be excluded")
	require.NotContains(t, resultSet, ".claude/settings.json", ".claude/ is a protected agent dir and should be excluded")
	require.Len(t, result, 2)
}

func TestMarshalPromptAttributionsIncludingPending_IncludesPending(t *testing.T) {
	t.Parallel()

	state := &SessionState{
		PromptAttributions: []PromptAttribution{
			{CheckpointNumber: 1, UserLinesAdded: 3},
		},
		PendingPromptAttribution: &PromptAttribution{
			CheckpointNumber: 2, UserLinesAdded: 5,
		},
	}

	raw := marshalPromptAttributionsIncludingPending(state)
	require.NotNil(t, raw)

	var result []PromptAttribution
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result, 2, "should include both committed and pending attributions")
	require.Equal(t, 1, result[0].CheckpointNumber)
	require.Equal(t, 3, result[0].UserLinesAdded)
	require.Equal(t, 2, result[1].CheckpointNumber)
	require.Equal(t, 5, result[1].UserLinesAdded)
}

func TestMarshalPromptAttributionsIncludingPending_NoPending(t *testing.T) {
	t.Parallel()

	state := &SessionState{
		PromptAttributions: []PromptAttribution{
			{CheckpointNumber: 1, UserLinesAdded: 3},
		},
	}

	raw := marshalPromptAttributionsIncludingPending(state)
	require.NotNil(t, raw)

	var result []PromptAttribution
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result, 1)
}

func TestMarshalPromptAttributionsIncludingPending_Empty(t *testing.T) {
	t.Parallel()

	state := &SessionState{}
	raw := marshalPromptAttributionsIncludingPending(state)
	require.Nil(t, raw, "empty state should return nil")
}

func TestMarshalPromptAttributionsIncludingPending_OnlyPending(t *testing.T) {
	t.Parallel()

	state := &SessionState{
		PendingPromptAttribution: &PromptAttribution{
			CheckpointNumber: 1, UserLinesAdded: 7,
		},
	}

	raw := marshalPromptAttributionsIncludingPending(state)
	require.NotNil(t, raw, "pending-only should still produce output")

	var result []PromptAttribution
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result, 1)
	require.Equal(t, 7, result[0].UserLinesAdded)
}

func TestCommittedFilesExcludingMetadata_AllMetadata(t *testing.T) {
	t.Parallel()

	result := committedFilesExcludingMetadata(map[string]struct{}{
		".trace/settings.json": {},
		".trace/.gitignore":    {},
	})
	require.Empty(t, result, "all metadata files should be excluded")
}
