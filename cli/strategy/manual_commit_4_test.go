package strategy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// TestCondenseSession_IncludesInitialAttribution verifies that when manual-commit
// condenses a session, it calculates InitialAttribution by comparing the shadow branch
// (agent work) to HEAD (what was committed).
func TestCondenseSession_IncludesInitialAttribution(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	// Create initial commit with a file
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create a file with some content
	testFile := filepath.Join(dir, "test.go")
	originalContent := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(testFile, []byte(originalContent), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := worktree.Add("test.go"); err != nil {
		t.Fatalf("failed to stage file: %v", err)
	}

	_, err = worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}
	sessionID := "2025-01-15-test-attribution"

	// Create metadata directory with transcript
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	if err := os.MkdirAll(metadataDirAbs, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}

	transcript := `{"type":"human","message":{"content":"modify test.go"}}
{"type":"assistant","message":{"content":"I'll modify test.go"}}
`
	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(transcript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Agent modifies the file (adds a new function)
	agentContent := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n\nfunc newFunc() {\n\tprintln(\"agent added this\")\n}\n"
	if err := os.WriteFile(testFile, []byte(agentContent), 0o644); err != nil {
		t.Fatalf("failed to write agent changes: %v", err)
	}

	// First checkpoint - captures agent's work on shadow branch
	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{"test.go"},
		NewFiles:       []string{},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 1",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	if err != nil {
		t.Fatalf("SaveStep() error = %v", err)
	}

	// Human edits the file (adds a comment)
	humanEditedContent := "package main\n\n// Human added this comment\nfunc main() {\n\tprintln(\"hello\")\n}\n\nfunc newFunc() {\n\tprintln(\"agent added this\")\n}\n"
	if err := os.WriteFile(testFile, []byte(humanEditedContent), 0o644); err != nil {
		t.Fatalf("failed to write human edits: %v", err)
	}

	// Stage and commit the human-edited file (this is what the user does)
	if _, err := worktree.Add("test.go"); err != nil {
		t.Fatalf("failed to stage human edits: %v", err)
	}
	_, err = worktree.Commit("Add new function with human comment", &git.CommitOptions{
		Author: &object.Signature{Name: "Human", Email: "human@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit human edits: %v", err)
	}

	// Load session state
	state, err := s.loadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}

	// Condense the session - this should calculate InitialAttribution
	checkpointID := id.MustCheckpointID("a1b2c3d4e5f6")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v", err)
	}

	// Verify CondenseResult
	if result.CheckpointID != checkpointID {
		t.Errorf("CheckpointID = %q, want %q", result.CheckpointID, checkpointID)
	}

	// Read metadata from trace/checkpoints/v1 branch and verify InitialAttribution
	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("failed to get sessions branch: %v", err)
	}

	sessionsCommit, err := repo.CommitObject(sessionsRef.Hash())
	if err != nil {
		t.Fatalf("failed to get sessions commit: %v", err)
	}

	tree, err := sessionsCommit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// InitialAttribution is stored in session-level metadata (0/metadata.json), not root (0-based indexing)
	sessionMetadataPath := checkpointID.Path() + "/0/" + paths.MetadataFileName
	metadataFile, err := tree.File(sessionMetadataPath)
	if err != nil {
		t.Fatalf("failed to find session metadata.json at %s: %v", sessionMetadataPath, err)
	}

	content, err := metadataFile.Contents()
	if err != nil {
		t.Fatalf("failed to read metadata.json: %v", err)
	}

	// Parse and verify InitialAttribution is present
	var metadata struct {
		InitialAttribution *struct {
			AgentLines      int     `json:"agent_lines"`
			HumanAdded      int     `json:"human_added"`
			HumanModified   int     `json:"human_modified"`
			HumanRemoved    int     `json:"human_removed"`
			TotalCommitted  int     `json:"total_committed"`
			AgentPercentage float64 `json:"agent_percentage"`
		} `json:"initial_attribution"`
	}
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		t.Fatalf("failed to parse metadata.json: %v", err)
	}

	if metadata.InitialAttribution == nil {
		t.Fatal("InitialAttribution should be present in session metadata.json for manual-commit")
	}

	// Verify the attribution values are reasonable
	// Agent added new function, human added a comment line
	// The exact line counts depend on how the diff algorithm interprets the changes
	// (insertion vs modification), but we should have non-zero totals and reasonable percentages.
	if metadata.InitialAttribution.TotalCommitted == 0 {
		t.Error("TotalCommitted should be > 0")
	}
	if metadata.InitialAttribution.AgentLines == 0 {
		t.Error("AgentLines should be > 0 (agent wrote code)")
	}

	// Human contribution should be captured in either HumanAdded or HumanModified
	// When inserting lines in the middle of existing code, the diff algorithm may
	// interpret it as a modification rather than a pure addition.
	humanContribution := metadata.InitialAttribution.HumanAdded + metadata.InitialAttribution.HumanModified
	if humanContribution == 0 {
		t.Error("Human contribution (HumanAdded + HumanModified) should be > 0")
	}

	if metadata.InitialAttribution.AgentPercentage <= 0 || metadata.InitialAttribution.AgentPercentage > 100 {
		t.Errorf("AgentPercentage should be between 0-100, got %f", metadata.InitialAttribution.AgentPercentage)
	}

	t.Logf("Attribution: agent=%d, human_added=%d, human_modified=%d, human_removed=%d, total=%d, percentage=%.1f%%",
		metadata.InitialAttribution.AgentLines,
		metadata.InitialAttribution.HumanAdded,
		metadata.InitialAttribution.HumanModified,
		metadata.InitialAttribution.HumanRemoved,
		metadata.InitialAttribution.TotalCommitted,
		metadata.InitialAttribution.AgentPercentage)
}

// TestCondenseSession_AttributionWithoutShadowBranch verifies that when an agent
// commits mid-turn (before SaveStep), attribution is still calculated using HEAD
// as the shadow tree. This reproduces the bug where agent_lines=0 for mid-turn commits.
func TestCondenseSession_AttributionWithoutShadowBranch(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create initial empty commit
	initialHash, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author:            &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
		AllowEmptyCommits: true,
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Agent creates files in nested directories and commits (mid-turn, no SaveStep)
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("failed to create src dir: %v", err)
	}
	agentFile := filepath.Join(srcDir, "main.go")
	agentContent := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	if err := os.WriteFile(agentFile, []byte(agentContent), 0o644); err != nil {
		t.Fatalf("failed to write agent file: %v", err)
	}
	agentFile2 := filepath.Join(dir, "README.md")
	agentContent2 := "# My Project\n\nA test project.\n"
	if err := os.WriteFile(agentFile2, []byte(agentContent2), 0o644); err != nil {
		t.Fatalf("failed to write agent file 2: %v", err)
	}
	if _, err := worktree.Add("src/main.go"); err != nil {
		t.Fatalf("failed to stage file: %v", err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatalf("failed to stage file 2: %v", err)
	}
	_, err = worktree.Commit("Add project files", &git.CommitOptions{
		Author: &object.Signature{Name: "Agent", Email: "agent@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	// Create a live transcript file (required when no shadow branch)
	transcriptDir := filepath.Join(dir, ".claude", "projects", "test")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatalf("failed to create transcript dir: %v", err)
	}
	transcriptFile := filepath.Join(transcriptDir, "session.jsonl")
	transcriptContent := `{"type":"human","message":{"content":"create project files"}}
{"type":"assistant","message":{"content":"I'll create src/main.go and README.md"}}
`
	if err := os.WriteFile(transcriptFile, []byte(transcriptContent), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Construct session state manually (no SaveStep was called, so no shadow branch)
	state := &SessionState{
		SessionID:             "test-no-shadow",
		BaseCommit:            initialHash.String(),
		AttributionBaseCommit: initialHash.String(),
		FilesTouched:          []string{"src/main.go", "README.md"},
		TranscriptPath:        transcriptFile,
		AgentType:             "Claude Code",
	}

	s := &ManualCommitStrategy{}
	checkpointID := id.MustCheckpointID("c3d4e5f6a7b8")

	// Condense — no shadow branch exists, but attribution should still work
	committedFiles := map[string]struct{}{"src/main.go": {}, "README.md": {}}
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, committedFiles)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v", err)
	}
	if result.CheckpointID != checkpointID {
		t.Errorf("CheckpointID = %q, want %q", result.CheckpointID, checkpointID)
	}

	// Read metadata from trace/checkpoints/v1 branch
	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("failed to get sessions branch: %v", err)
	}
	sessionsCommit, err := repo.CommitObject(sessionsRef.Hash())
	if err != nil {
		t.Fatalf("failed to get sessions commit: %v", err)
	}
	tree, err := sessionsCommit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	sessionMetadataPath := checkpointID.Path() + "/0/" + paths.MetadataFileName
	metadataFile, err := tree.File(sessionMetadataPath)
	if err != nil {
		t.Fatalf("failed to find session metadata at %s: %v", sessionMetadataPath, err)
	}
	content, err := metadataFile.Contents()
	if err != nil {
		t.Fatalf("failed to read metadata: %v", err)
	}

	var metadata struct {
		InitialAttribution *struct {
			AgentLines      int     `json:"agent_lines"`
			HumanAdded      int     `json:"human_added"`
			TotalCommitted  int     `json:"total_committed"`
			AgentPercentage float64 `json:"agent_percentage"`
		} `json:"initial_attribution"`
	}
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		t.Fatalf("failed to parse metadata: %v", err)
	}

	if metadata.InitialAttribution == nil {
		t.Fatal("InitialAttribution should be present even without shadow branch")
	}

	// Agent created all content (10 lines across 2 files), no human edits
	if metadata.InitialAttribution.AgentLines == 0 {
		t.Error("AgentLines should be > 0 (agent created the file)")
	}
	if metadata.InitialAttribution.TotalCommitted == 0 {
		t.Error("TotalCommitted should be > 0")
	}
	if metadata.InitialAttribution.AgentPercentage <= 50 {
		t.Errorf("AgentPercentage should be > 50%% (agent wrote all content), got %.1f%%",
			metadata.InitialAttribution.AgentPercentage)
	}

	t.Logf("Attribution (no shadow branch): agent=%d, human_added=%d, total=%d, percentage=%.1f%%",
		metadata.InitialAttribution.AgentLines,
		metadata.InitialAttribution.HumanAdded,
		metadata.InitialAttribution.TotalCommitted,
		metadata.InitialAttribution.AgentPercentage)
}

// TestCondenseSession_AttributionWithoutShadowBranch_MixedHumanAgent verifies attribution
// when an agent commits mid-turn (no shadow branch) and the commit includes both human
// pre-session changes and agent-created files. Human changes are captured in PromptAttributions
// and should be subtracted from the total to isolate agent contribution.
func TestCondenseSession_AttributionWithoutShadowBranch_MixedHumanAgent(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create initial commit with one file
	existingFile := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(existingFile, []byte("key: value\n"), 0o644); err != nil {
		t.Fatalf("failed to write initial file: %v", err)
	}
	if _, err := wt.Add("config.yaml"); err != nil {
		t.Fatalf("failed to stage: %v", err)
	}
	initialHash, err := wt.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Human adds a new file (before the agent session starts).
	// This is captured by calculatePromptAttributionAtStart.
	humanFile := filepath.Join(dir, "docs", "notes.md")
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatalf("failed to mkdir: %v", err)
	}
	humanContent := "# Notes\n\nSome human notes.\nAnother line.\n"
	if err := os.WriteFile(humanFile, []byte(humanContent), 0o644); err != nil {
		t.Fatalf("failed to write human file: %v", err)
	}

	// Agent creates its own file in a nested directory
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatalf("failed to mkdir: %v", err)
	}
	agentFile := filepath.Join(dir, "src", "app.go")
	agentContent := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"app\")\n}\n"
	if err := os.WriteFile(agentFile, []byte(agentContent), 0o644); err != nil {
		t.Fatalf("failed to write agent file: %v", err)
	}

	// Agent stages everything and commits (mid-turn, no SaveStep)
	if _, err := wt.Add("docs/notes.md"); err != nil {
		t.Fatalf("failed to stage: %v", err)
	}
	if _, err := wt.Add("src/app.go"); err != nil {
		t.Fatalf("failed to stage: %v", err)
	}
	_, err = wt.Commit("Add app and notes", &git.CommitOptions{
		Author: &object.Signature{Name: "Agent", Email: "agent@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	// Create live transcript
	transcriptDir := filepath.Join(dir, ".claude", "projects", "test")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatalf("failed to create transcript dir: %v", err)
	}
	transcriptFile := filepath.Join(transcriptDir, "session.jsonl")
	if err := os.WriteFile(transcriptFile, []byte(`{"type":"human","message":{"content":"create src/app.go"}}
{"type":"assistant","message":{"content":"Done"}}
`), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Session state with PromptAttributions capturing human's pre-session file (4 lines)
	state := &SessionState{
		SessionID:             "test-mixed-no-shadow",
		BaseCommit:            initialHash.String(),
		AttributionBaseCommit: initialHash.String(),
		FilesTouched:          []string{"src/app.go"},
		TranscriptPath:        transcriptFile,
		AgentType:             "Claude Code",
		PromptAttributions: []PromptAttribution{{
			CheckpointNumber: 1,
			UserLinesAdded:   4,
			UserAddedPerFile: map[string]int{"docs/notes.md": 4},
		}},
	}

	s := &ManualCommitStrategy{}
	checkpointID := id.MustCheckpointID("d4e5f6a7b8c9")

	committedFiles := map[string]struct{}{"src/app.go": {}, "docs/notes.md": {}}
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, committedFiles)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v", err)
	}
	if result.CheckpointID != checkpointID {
		t.Errorf("CheckpointID = %q, want %q", result.CheckpointID, checkpointID)
	}

	// Read metadata
	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("failed to get sessions branch: %v", err)
	}
	sessionsCommit, err := repo.CommitObject(sessionsRef.Hash())
	if err != nil {
		t.Fatalf("failed to get sessions commit: %v", err)
	}
	tree, err := sessionsCommit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	sessionMetadataPath := checkpointID.Path() + "/0/" + paths.MetadataFileName
	metadataFile, err := tree.File(sessionMetadataPath)
	if err != nil {
		t.Fatalf("failed to find session metadata at %s: %v", sessionMetadataPath, err)
	}
	content, err := metadataFile.Contents()
	if err != nil {
		t.Fatalf("failed to read metadata: %v", err)
	}

	var metadata struct {
		InitialAttribution *struct {
			AgentLines      int     `json:"agent_lines"`
			HumanAdded      int     `json:"human_added"`
			TotalCommitted  int     `json:"total_committed"`
			AgentPercentage float64 `json:"agent_percentage"`
		} `json:"initial_attribution"`
	}
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		t.Fatalf("failed to parse metadata: %v", err)
	}

	if metadata.InitialAttribution == nil {
		t.Fatal("InitialAttribution should be present")
	}

	attr := metadata.InitialAttribution
	t.Logf("Attribution (mixed, no shadow): agent=%d, human_added=%d, total=%d, percentage=%.1f%%",
		attr.AgentLines, attr.HumanAdded, attr.TotalCommitted, attr.AgentPercentage)

	// src/app.go has 7 lines (agent). docs/notes.md was added before the session
	// (captured by PA1) so it's pre-session baseline — excluded from human count.
	if attr.AgentLines != 7 {
		t.Errorf("AgentLines = %d, want 7 (src/app.go has 7 lines)", attr.AgentLines)
	}
	if attr.HumanAdded != 0 {
		t.Errorf("HumanAdded = %d, want 0 (docs/notes.md is pre-session baseline, excluded)", attr.HumanAdded)
	}
	if attr.TotalCommitted != 7 {
		t.Errorf("TotalCommitted = %d, want 7 (agent-only, pre-session excluded)", attr.TotalCommitted)
	}
	// Agent wrote 7/7 = 100%
	if attr.AgentPercentage < 99.0 {
		t.Errorf("AgentPercentage = %.1f%%, want ~100%% (pre-session human file excluded)", attr.AgentPercentage)
	}
}

// TestExtractUserPromptsFromLines tests extraction of user prompts from JSONL format.
func TestExtractUserPromptsFromLines(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected []string
	}{
		{
			name: "human type message",
			lines: []string{
				`{"type":"human","message":{"content":"Hello world"}}`,
			},
			expected: []string{"Hello world"},
		},
		{
			name: "user type message",
			lines: []string{
				`{"type":"user","message":{"content":"Test prompt"}}`,
			},
			expected: []string{"Test prompt"},
		},
		{
			name: "mixed human and assistant",
			lines: []string{
				`{"type":"human","message":{"content":"First"}}`,
				`{"type":"assistant","message":{"content":"Response"}}`,
				`{"type":"human","message":{"content":"Second"}}`,
			},
			expected: []string{"First", "Second"},
		},
		{
			name: "array content",
			lines: []string{
				`{"type":"human","message":{"content":[{"type":"text","text":"Part 1"},{"type":"text","text":"Part 2"}]}}`,
			},
			expected: []string{"Part 1\n\nPart 2"},
		},
		{
			name: "empty lines ignored",
			lines: []string{
				`{"type":"human","message":{"content":"Valid"}}`,
				"",
				"  ",
			},
			expected: []string{"Valid"},
		},
		{
			name: "invalid JSON ignored",
			lines: []string{
				`{"type":"human","message":{"content":"Valid"}}`,
				"not json",
			},
			expected: []string{"Valid"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractUserPromptsFromLines(tt.lines)
			if len(result) != len(tt.expected) {
				t.Errorf("extractUserPromptsFromLines() returned %d prompts, want %d", len(result), len(tt.expected))
				return
			}
			for i, prompt := range result {
				if prompt != tt.expected[i] {
					t.Errorf("prompt[%d] = %q, want %q", i, prompt, tt.expected[i])
				}
			}
		})
	}
}
