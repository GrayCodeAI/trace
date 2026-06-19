package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateCheckpointsV2_PreservesPromptAttributions(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)
	ctx := context.Background()

	cpID := id.MustCheckpointID("aabb22334455")
	promptAttrs := json.RawMessage(`[{"prompt_index":0,"user_lines":["main.go:10"]}]`)

	err := v1Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID:           cpID,
		SessionID:              "session-pa-001",
		Strategy:               "manual-commit",
		Transcript:             redact.AlreadyRedacted([]byte("{\"type\":\"assistant\",\"message\":\"pa test\"}\n")),
		Prompts:                []string{"test prompt"},
		PromptAttributionsJSON: promptAttrs,
		AuthorName:             "Test",
		AuthorEmail:            "test@test.com",
	})
	require.NoError(t, err)

	// Verify v1 has prompt_attributions
	v1Content, err := v1Store.ReadSessionContent(ctx, cpID, 0)
	require.NoError(t, err)
	require.NotNil(t, v1Content.Metadata.PromptAttributions, "v1 should have prompt_attributions")

	// Migrate
	var stdout bytes.Buffer
	result, err := migrateCheckpointsV2(ctx, repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.migrated)

	// Read v2 session metadata from /main ref and verify prompt_attributions preserved
	v2MainRef, err := repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	require.NoError(t, err)
	v2MainCommit, err := repo.CommitObject(v2MainRef.Hash())
	require.NoError(t, err)
	v2MainTree, err := v2MainCommit.Tree()
	require.NoError(t, err)

	metadataFile, err := v2MainTree.File(cpID.Path() + "/0/" + paths.MetadataFileName)
	require.NoError(t, err)
	metadataContent, err := metadataFile.Contents()
	require.NoError(t, err)

	var metadata checkpoint.CommittedMetadata
	require.NoError(t, json.Unmarshal([]byte(metadataContent), &metadata))
	assert.JSONEq(t, string(promptAttrs), string(metadata.PromptAttributions),
		"v2 session metadata should preserve prompt_attributions from v1")
}

func TestMigrateCheckpointsV2_PreservesCombinedAttribution(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)
	ctx := context.Background()

	cpID := id.MustCheckpointID("ccdd55667788")

	// Write two sessions so combined attribution is meaningful
	writeV1Checkpoint(
		t, v1Store, cpID, "session-ca-001",
		[]byte("{\"type\":\"assistant\",\"message\":\"session 1\"}\n"),
		[]string{"prompt 1"},
	)
	writeV1Checkpoint(
		t, v1Store, cpID, "session-ca-002",
		[]byte("{\"type\":\"assistant\",\"message\":\"session 2\"}\n"),
		[]string{"prompt 2"},
	)

	// Inject CombinedAttribution into v1 root summary
	combined := &checkpoint.InitialAttribution{
		CalculatedAt:      time.Date(2026, 4, 15, 0, 18, 47, 0, time.UTC),
		AgentLines:        119,
		AgentRemoved:      94,
		HumanAdded:        3,
		HumanModified:     0,
		HumanRemoved:      1,
		TotalCommitted:    122,
		TotalLinesChanged: 217,
		AgentPercentage:   98.15668202764977,
		MetricVersion:     2,
	}
	err := v1Store.UpdateCheckpointSummary(ctx, cpID, combined)
	require.NoError(t, err)

	// Verify v1 root summary has CombinedAttribution
	v1Summary, err := v1Store.ReadCommitted(ctx, cpID)
	require.NoError(t, err)
	require.NotNil(t, v1Summary.CombinedAttribution, "v1 should have combined_attribution")

	// Migrate
	var stdout bytes.Buffer
	result, err := migrateCheckpointsV2(ctx, repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.migrated)

	// Read v2 root summary and verify CombinedAttribution preserved
	v2Summary, err := v2Store.ReadCommitted(ctx, cpID)
	require.NoError(t, err)
	require.NotNil(t, v2Summary)
	require.NotNil(t, v2Summary.CombinedAttribution,
		"v2 root summary should preserve combined_attribution from v1")
	assert.Equal(t, combined.CalculatedAt, v2Summary.CombinedAttribution.CalculatedAt)
	assert.Equal(t, combined.AgentLines, v2Summary.CombinedAttribution.AgentLines)
	assert.Equal(t, combined.AgentRemoved, v2Summary.CombinedAttribution.AgentRemoved)
	assert.Equal(t, combined.HumanAdded, v2Summary.CombinedAttribution.HumanAdded)
	assert.Equal(t, combined.HumanModified, v2Summary.CombinedAttribution.HumanModified)
	assert.Equal(t, combined.HumanRemoved, v2Summary.CombinedAttribution.HumanRemoved)
	assert.Equal(t, combined.TotalCommitted, v2Summary.CombinedAttribution.TotalCommitted)
	assert.Equal(t, combined.TotalLinesChanged, v2Summary.CombinedAttribution.TotalLinesChanged)
	assert.InDelta(t, combined.AgentPercentage, v2Summary.CombinedAttribution.AgentPercentage, 0.001)
	assert.Equal(t, combined.MetricVersion, v2Summary.CombinedAttribution.MetricVersion)
}

func TestSortMigratableCheckpoints(t *testing.T) {
	t.Parallel()

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		input []checkpoint.CommittedInfo
		want  []id.CheckpointID
	}{
		{
			name: "chronological order",
			input: []checkpoint.CommittedInfo{
				{CheckpointID: id.MustCheckpointID("000000000003"), CreatedAt: t3},
				{CheckpointID: id.MustCheckpointID("000000000001"), CreatedAt: t1},
				{CheckpointID: id.MustCheckpointID("000000000002"), CreatedAt: t2},
			},
			want: []id.CheckpointID{
				id.MustCheckpointID("000000000001"),
				id.MustCheckpointID("000000000002"),
				id.MustCheckpointID("000000000003"),
			},
		},
		{
			name: "ties on CreatedAt break by checkpoint ID",
			input: []checkpoint.CommittedInfo{
				{CheckpointID: id.MustCheckpointID("0000000000bb"), CreatedAt: t1},
				{CheckpointID: id.MustCheckpointID("0000000000aa"), CreatedAt: t1},
				{CheckpointID: id.MustCheckpointID("0000000000cc"), CreatedAt: t1},
			},
			want: []id.CheckpointID{
				id.MustCheckpointID("0000000000aa"),
				id.MustCheckpointID("0000000000bb"),
				id.MustCheckpointID("0000000000cc"),
			},
		},
		{
			name: "zero CreatedAt sorts after non-zero, ties by ID",
			input: []checkpoint.CommittedInfo{
				{CheckpointID: id.MustCheckpointID("0000000000aa")},
				{CheckpointID: id.MustCheckpointID("000000000002"), CreatedAt: t2},
				{CheckpointID: id.MustCheckpointID("0000000000bb")},
				{CheckpointID: id.MustCheckpointID("000000000001"), CreatedAt: t1},
			},
			want: []id.CheckpointID{
				id.MustCheckpointID("000000000001"),
				id.MustCheckpointID("000000000002"),
				id.MustCheckpointID("0000000000aa"),
				id.MustCheckpointID("0000000000bb"),
			},
		},
		{
			name: "all-zero CreatedAt sorts by ID",
			input: []checkpoint.CommittedInfo{
				{CheckpointID: id.MustCheckpointID("0000000000cc")},
				{CheckpointID: id.MustCheckpointID("0000000000aa")},
				{CheckpointID: id.MustCheckpointID("0000000000bb")},
			},
			want: []id.CheckpointID{
				id.MustCheckpointID("0000000000aa"),
				id.MustCheckpointID("0000000000bb"),
				id.MustCheckpointID("0000000000cc"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := make([]checkpoint.CommittedInfo, len(tt.input))
			copy(input, tt.input)
			sortMigratableCheckpoints(input)
			got := make([]id.CheckpointID, len(input))
			for i, c := range input {
				got[i] = c.CheckpointID
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
