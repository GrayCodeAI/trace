package strategy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

// TestCondenseSession_V2DualWrite verifies that when checkpoints_v2 is enabled,
// CondenseSession writes to both v1 (trace/checkpoints/v1) and v2 refs
// (refs/trace/checkpoints/v2/main and refs/trace/checkpoints/v2/full/current).
func TestCondenseSession_V2DualWrite(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)

	worktree, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644))
	_, err = worktree.Add("main.go")
	require.NoError(t, err)
	commitHash, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	t.Chdir(dir)

	// Enable checkpoints_v2 via settings
	traceDir := filepath.Join(dir, ".trace")
	require.NoError(t, os.MkdirAll(traceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(testCheckpointsV2SettingsJSON), 0o644))

	s := &ManualCommitStrategy{}
	sessionID := "2025-01-15-test-v2-dual-write"

	// Create metadata directory with transcript
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	require.NoError(t, os.MkdirAll(metadataDirAbs, 0o755))

	secret := "q9Xv2Lm8Rt1Yp4Kd7Wz0Hs6Nc3Bf5Jg"
	transcript := `{"type":"human","message":{"content":"hello secret: ` + secret + `"}}
{"type":"assistant","message":{"content":"hi there"}}
`
	require.NoError(t, os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(transcript), 0o644))

	// SaveStep to create shadow branch
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
	state.BaseCommit = commitHash.String()[:7]
	state.AgentType = agent.AgentTypeClaudeCode

	checkpointID := id.MustCheckpointID("dd11ee22ff33")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	// v1 branch should exist (as before)
	v1Ref, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err, "v1 metadata branch should exist")
	require.NotEqual(t, plumbing.ZeroHash, v1Ref.Hash())

	// v2 /main ref should exist
	v2MainRef, err := repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	require.NoError(t, err, "v2 /main ref should exist")
	require.NotEqual(t, plumbing.ZeroHash, v2MainRef.Hash())

	// v2 /full/current ref should exist (transcript was non-empty)
	v2FullRef, err := repo.Reference(plumbing.ReferenceName(paths.V2FullCurrentRefName), true)
	require.NoError(t, err, "v2 /full/current ref should exist")
	require.NotEqual(t, plumbing.ZeroHash, v2FullRef.Hash())

	// Verify /main has metadata and redacted compact transcript
	v2MainCommit, err := repo.CommitObject(v2MainRef.Hash())
	require.NoError(t, err)
	v2MainTree, err := v2MainCommit.Tree()
	require.NoError(t, err)

	cpPath := checkpointID.Path()
	mainCpTree, err := v2MainTree.Tree(cpPath)
	require.NoError(t, err)

	// Root metadata.json should exist
	_, err = mainCpTree.File(paths.MetadataFileName)
	require.NoError(t, err, "root metadata.json should exist on /main")

	mainSessionTree, err := mainCpTree.Tree("0")
	require.NoError(t, err)
	compactFile, err := mainSessionTree.File(paths.CompactTranscriptFileName)
	require.NoError(t, err, "transcript.jsonl should exist on /main")
	compactContent, err := compactFile.Contents()
	require.NoError(t, err)
	require.NotContains(t, compactContent, secret, "compact transcript on /main must be redacted")

	// Verify /full/current has transcript
	v2FullCommit, err := repo.CommitObject(v2FullRef.Hash())
	require.NoError(t, err)
	v2FullTree, err := v2FullCommit.Tree()
	require.NoError(t, err)

	fullCpTree, err := v2FullTree.Tree(cpPath)
	require.NoError(t, err)
	fullSessionTree, err := fullCpTree.Tree("0")
	require.NoError(t, err)
	_, err = fullSessionTree.File(paths.V2RawTranscriptFileName)
	require.NoError(t, err, "raw_transcript should exist on /full/current")
}

func TestCondenseSession_V2DualWrite_CopiesTaskMetadataToFullCurrent(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, "main.go", "package main")
	testutil.GitAdd(t, dir, "main.go")
	testutil.GitCommit(t, dir, "Initial commit")

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	commitHash := testutil.GetHeadHash(t, dir)

	t.Chdir(dir)

	traceDir := filepath.Join(dir, ".trace")
	require.NoError(t, os.MkdirAll(traceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(testCheckpointsV2SettingsJSON), 0o644))

	s := &ManualCommitStrategy{}
	sessionID := "2025-01-15-test-v2-task-dual-write"

	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	require.NoError(t, os.MkdirAll(metadataDirAbs, 0o755))

	transcript := `{"type":"human","message":{"content":"hello"}}
{"type":"assistant","message":{"content":"hi there"}}
`
	transcriptPath := filepath.Join(metadataDirAbs, paths.TranscriptFileName)
	require.NoError(t, os.WriteFile(transcriptPath, []byte(transcript), 0o644))

	// Create shadow branch/session checkpoint data.
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

	subagentTranscriptPath := filepath.Join(metadataDirAbs, "subagent.jsonl")
	require.NoError(t, os.WriteFile(subagentTranscriptPath, []byte("{\"type\":\"event\",\"message\":\"done\"}\n"), 0o644))

	err = s.SaveTaskStep(context.Background(), TaskStepContext{
		SessionID:              sessionID,
		ToolUseID:              "toolu_01TASK",
		AgentID:                "agent-01",
		ModifiedFiles:          []string{"main.go"},
		TranscriptPath:         transcriptPath,
		SubagentTranscriptPath: subagentTranscriptPath,
		CheckpointUUID:         "uuid-task-001",
		AuthorName:             "Test",
		AuthorEmail:            "test@test.com",
		SubagentType:           "general",
		TaskDescription:        "Implement task",
		AgentType:              agent.AgentTypeClaudeCode,
	})
	require.NoError(t, err)

	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.TranscriptPath = transcriptPath
	state.BaseCommit = commitHash[:12]
	state.AgentType = agent.AgentTypeClaudeCode

	checkpointID := id.MustCheckpointID("ab11cd22ef33")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	v2FullRef, err := repo.Reference(plumbing.ReferenceName(paths.V2FullCurrentRefName), true)
	require.NoError(t, err, "v2 /full/current ref should exist")

	v2FullCommit, err := repo.CommitObject(v2FullRef.Hash())
	require.NoError(t, err)
	v2FullTree, err := v2FullCommit.Tree()
	require.NoError(t, err)

	taskCheckpointPath := checkpointID.Path() + "/0/tasks/toolu_01TASK/checkpoint.json"
	_, err = v2FullTree.File(taskCheckpointPath)
	require.NoError(t, err, "task checkpoint metadata should be copied to v2 /full/current")
}

// TestCondenseSession_V2CompactTranscriptStart verifies v2 /main writes
// checkpoint_transcript_start from compact transcript offset, not full.jsonl offset.
func TestCondenseSession_V2CompactTranscriptStart(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, "main.go", "package main")
	testutil.GitAdd(t, dir, "main.go")
	testutil.GitCommit(t, dir, "Initial commit")

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	commitHash := testutil.GetHeadHash(t, dir)

	t.Chdir(dir)

	// Enable checkpoints_v2 via settings
	traceDir := filepath.Join(dir, ".trace")
	require.NoError(t, os.MkdirAll(traceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(testCheckpointsV2SettingsJSON), 0o644))

	s := &ManualCommitStrategy{}
	sessionID := "2025-01-15-test-v2-compact-start"

	// Create metadata directory with transcript
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	require.NoError(t, os.MkdirAll(metadataDirAbs, 0o755))

	transcript := `{"type":"human","message":{"content":"hello"}}
{"type":"assistant","message":{"content":"hi there"}}
`
	require.NoError(t, os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(transcript), 0o644))

	// SaveStep to create shadow branch
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
	state.BaseCommit = commitHash[:7]
	state.AgentType = agent.AgentTypeClaudeCode

	// First condensation starts at compact offset 0.
	checkpointID := id.MustCheckpointID("cc11dd22ee33")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	// v2 /main should have checkpoint_transcript_start = 0 for first checkpoint.
	v2MainRef, err := repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	require.NoError(t, err)
	v2MainCommit, err := repo.CommitObject(v2MainRef.Hash())
	require.NoError(t, err)
	v2MainTree, err := v2MainCommit.Tree()
	require.NoError(t, err)

	cpPath := checkpointID.Path()
	sessionTree, err := v2MainTree.Tree(cpPath + "/0")
	require.NoError(t, err)
	metadataFile, err := sessionTree.File(paths.MetadataFileName)
	require.NoError(t, err)
	metadataContent, err := metadataFile.Contents()
	require.NoError(t, err)

	var v2Metadata checkpoint.CommittedMetadata
	require.NoError(t, json.Unmarshal([]byte(metadataContent), &v2Metadata))
	require.Equal(t, 0, v2Metadata.CheckpointTranscriptStart,
		"first checkpoint v2 metadata should have checkpoint_transcript_start=0")

	// Read v1 metadata for comparison.
	v1Ref, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err)
	v1Commit, err := repo.CommitObject(v1Ref.Hash())
	require.NoError(t, err)
	v1Tree, err := v1Commit.Tree()
	require.NoError(t, err)
	v1SessionTree, err := v1Tree.Tree(cpPath + "/0")
	require.NoError(t, err)
	v1MetadataFile, err := v1SessionTree.File(paths.MetadataFileName)
	require.NoError(t, err)
	v1MetadataContent, err := v1MetadataFile.Contents()
	require.NoError(t, err)

	var v1Metadata checkpoint.CommittedMetadata
	require.NoError(t, json.Unmarshal([]byte(v1MetadataContent), &v1Metadata))
	require.Equal(t, 0, v1Metadata.CheckpointTranscriptStart,
		"first checkpoint v1 metadata should also have checkpoint_transcript_start=0")

	// Verify compact transcript lines were counted in the result
	require.Positive(t, result.CompactTranscriptLines,
		"CondenseResult should report compact transcript lines")

	// Read compact transcript.jsonl from v2 /main for the first checkpoint.
	compactFile1, err := sessionTree.File(paths.CompactTranscriptFileName)
	require.NoError(t, err, "transcript.jsonl should exist on v2 /main")
	compactContent1, err := compactFile1.Contents()
	require.NoError(t, err)
	firstCompactLines := bytes.Count([]byte(compactContent1), []byte{'\n'})
	require.Positive(t, firstCompactLines, "first checkpoint compact transcript should have lines")

	// --- Second condensation: add more transcript content ---
	transcript2 := transcript + `{"type":"human","message":{"content":"next question"}}
{"type":"assistant","message":{"content":"next answer"}}
`
	require.NoError(t, os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(transcript2), 0o644))

	// Update state after first condensation (mimic what CondenseSessionByID does)
	state.StepCount = 0
	state.CheckpointTranscriptStart = result.TotalTranscriptLines
	state.CompactTranscriptStart += result.CompactTranscriptLines

	// SaveStep for second checkpoint
	testutil.WriteFile(t, dir, "main.go", "package main\n// v2")
	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{"main.go"},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 2",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	require.NoError(t, err)

	state2, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state2.TranscriptPath = filepath.Join(metadataDirAbs, paths.TranscriptFileName)
	state2.BaseCommit = commitHash[:7]
	state2.AgentType = agent.AgentTypeClaudeCode
	state2.CheckpointTranscriptStart = state.CheckpointTranscriptStart
	state2.CompactTranscriptStart = state.CompactTranscriptStart

	checkpointID2 := id.MustCheckpointID("dd22ee33ff44")
	result2, err := s.CondenseSession(context.Background(), repo, checkpointID2, state2, nil)
	require.NoError(t, err)
	require.NotNil(t, result2)

	// v2 /main metadata for second checkpoint should have compact start = firstCompactLines.
	v2MainRef2, err := repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	require.NoError(t, err)
	v2MainCommit2, err := repo.CommitObject(v2MainRef2.Hash())
	require.NoError(t, err)
	v2MainTree2, err := v2MainCommit2.Tree()
	require.NoError(t, err)

	cpPath2 := checkpointID2.Path()
	sessionTree2, err := v2MainTree2.Tree(cpPath2 + "/0")
	require.NoError(t, err)
	metadataFile2, err := sessionTree2.File(paths.MetadataFileName)
	require.NoError(t, err)
	metadataContent2, err := metadataFile2.Contents()
	require.NoError(t, err)

	var v2Metadata2 checkpoint.CommittedMetadata
	require.NoError(t, json.Unmarshal([]byte(metadataContent2), &v2Metadata2))
	require.Equal(t, firstCompactLines, v2Metadata2.CheckpointTranscriptStart,
		"second checkpoint v2 metadata should have checkpoint_transcript_start = first checkpoint's compact line count")

	// The compact transcript.jsonl for checkpoint 2 should be CUMULATIVE:
	// it should contain both checkpoint 1's and checkpoint 2's compact lines.
	compactFile2, err := sessionTree2.File(paths.CompactTranscriptFileName)
	require.NoError(t, err, "transcript.jsonl should exist for second checkpoint")
	compactContent2, err := compactFile2.Contents()
	require.NoError(t, err)
	secondCompactTotalLines := bytes.Count([]byte(compactContent2), []byte{'\n'})
	require.Greater(t, secondCompactTotalLines, firstCompactLines,
		"second checkpoint compact transcript should include all prior content plus new content")

	// The first checkpoint's content should be a prefix of the second checkpoint's content.
	require.True(t, strings.HasPrefix(compactContent2, compactContent1),
		"second checkpoint compact transcript should start with first checkpoint's content")
}

// TestCondenseSession_V2Disabled_NoV2Refs verifies that when checkpoints_v2 is
// not enabled, CondenseSession only writes to v1 and does not create v2 refs.
func TestCondenseSession_V2Disabled_NoV2Refs(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)

	worktree, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644))
	_, err = worktree.Add("main.go")
	require.NoError(t, err)
	commitHash, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	t.Chdir(dir)

	// No checkpoints_v2 setting — default is disabled
	traceDir := filepath.Join(dir, ".trace")
	require.NoError(t, os.MkdirAll(traceDir, 0o755))
	settingsJSON := `{"enabled": true, "strategy": "manual-commit"}`
	require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(settingsJSON), 0o644))

	s := &ManualCommitStrategy{}
	sessionID := "2025-01-15-test-v2-disabled"

	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	require.NoError(t, os.MkdirAll(metadataDirAbs, 0o755))

	transcript := `{"type":"human","message":{"content":"hello"}}
{"type":"assistant","message":{"content":"hi"}}
`
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
	state.BaseCommit = commitHash.String()[:7]

	checkpointID := id.MustCheckpointID("ee22ff33aa44")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 0, result.CompactTranscriptLines, "v2-disabled condensation should not report compact transcript line deltas")

	// v1 should exist
	_, err = repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err, "v1 metadata branch should exist")

	// v2 refs should NOT exist
	_, err = repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	require.Error(t, err, "v2 /main ref should not exist when v2 is disabled")

	_, err = repo.Reference(plumbing.ReferenceName(paths.V2FullCurrentRefName), true)
	require.Error(t, err, "v2 /full/current ref should not exist when v2 is disabled")
}

func TestCondenseSession_RedactionFailure_DropsTranscriptButWritesMetadata(t *testing.T) {
	originalRedact := redactSessionJSONLBytes
	redactSessionJSONLBytes = func([]byte) (redact.RedactedBytes, error) {
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

	store, err := s.getCheckpointStore()
	require.NoError(t, err)

	committed, err := store.ListCommitted(context.Background())
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

	_, err = store.ReadLatestSessionContent(context.Background(), checkpointID)
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
	require.Contains(t, resultSet, ".claude/settings.json")
	require.NotContains(t, resultSet, ".trace/settings.json", ".trace/ should be excluded")
	require.NotContains(t, resultSet, ".trace/.gitignore", ".trace/ should be excluded")
	require.Len(t, result, 3)
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
