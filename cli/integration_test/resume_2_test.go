//go:build integration

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestResume_MultiSessionMixedTimestamps tests resume with multiple sessions in a checkpoint
// where one session has a newer local log (conflict) and another doesn't (no conflict).
func TestResume_MultiSessionMixedTimestamps(t *testing.T) {
	t.Parallel()
	env := NewFeatureBranchEnv(t)

	// Create first session
	session1 := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session1.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit failed: %v", err)
	}

	content1 := "def hello; end"
	env.WriteFile("hello.rb", content1)

	session1.CreateTranscript(
		"Create hello method",
		[]FileChange{{Path: "hello.rb", Content: content1}},
	)
	if err := env.SimulateStop(session1.ID, session1.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop session1 failed: %v", err)
	}

	// Create second session (same base commit, different session)
	session2 := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session2.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit failed: %v", err)
	}

	content2 := "def goodbye; end"
	env.WriteFile("goodbye.rb", content2)

	session2.CreateTranscript(
		"Create goodbye method",
		[]FileChange{{Path: "goodbye.rb", Content: content2}},
	)
	if err := env.SimulateStop(session2.ID, session2.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop session2 failed: %v", err)
	}

	// Commit changes with hooks (this triggers prepare-commit-msg and post-commit hooks,
	// which adds Trace-Checkpoint trailer and condenses both sessions to the same checkpoint)
	env.GitCommitWithShadowHooks("Add hello and goodbye methods", "hello.rb", "goodbye.rb")

	featureBranch := env.GetCurrentBranch()

	// Create local logs with different timestamps:
	// - session1: NEWER than checkpoint (conflict)
	// - session2: OLDER than checkpoint (no conflict)
	if err := os.MkdirAll(env.ClaudeProjectDir, 0o755); err != nil {
		t.Fatalf("failed to create Claude project dir: %v", err)
	}

	// Session 1: newer local log (conflict)
	log1Path := filepath.Join(env.ClaudeProjectDir, session1.ID+".jsonl")
	futureTimestamp := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	newerContent := fmt.Sprintf(`{"type":"human","timestamp":"%s","message":{"content":"newer local work on session1"}}`, futureTimestamp)
	if err := os.WriteFile(log1Path, []byte(newerContent), 0o644); err != nil {
		t.Fatalf("failed to write session1 log: %v", err)
	}

	// Session 2: older local log (no conflict)
	log2Path := filepath.Join(env.ClaudeProjectDir, session2.ID+".jsonl")
	pastTimestamp := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	olderContent := fmt.Sprintf(`{"type":"human","timestamp":"%s","message":{"content":"older local work on session2"}}`, pastTimestamp)
	if err := os.WriteFile(log2Path, []byte(olderContent), 0o644); err != nil {
		t.Fatalf("failed to write session2 log: %v", err)
	}

	// Switch to main
	env.GitCheckoutBranch(masterBranch)

	// Resume WITH --force (to bypass confirmation for the conflict)
	output, err := env.RunResumeForce(featureBranch)
	if err != nil {
		t.Fatalf("resume --force failed: %v\nOutput: %s", err, output)
	}

	// Both logs should be overwritten with checkpoint content
	data1, err := os.ReadFile(log1Path)
	if err != nil {
		t.Fatalf("failed to read session1 log: %v", err)
	}
	if strings.Contains(string(data1), "newer local work") {
		t.Errorf("session1 log should have been overwritten, but still has newer content: %s", string(data1))
	}
	if !strings.Contains(string(data1), "Create hello method") {
		t.Errorf("session1 log should contain checkpoint transcript, got: %s", string(data1))
	}

	data2, err := os.ReadFile(log2Path)
	if err != nil {
		t.Fatalf("failed to read session2 log: %v", err)
	}
	if strings.Contains(string(data2), "older local work") {
		t.Errorf("session2 log should have been overwritten, but still has older content: %s", string(data2))
	}
	if !strings.Contains(string(data2), "Create goodbye method") {
		t.Errorf("session2 log should contain checkpoint transcript, got: %s", string(data2))
	}

	// Output should mention restoring multiple sessions
	if !strings.Contains(output, "Restoring 2 sessions") {
		t.Logf("Note: Expected 'Restoring 2 sessions' in output, got: %s", output)
	}
}

// TestResume_LocalLogNoTimestamp tests that when local log has no valid timestamp,
// resume proceeds without requiring --force (treated as new).
func TestResume_LocalLogNoTimestamp(t *testing.T) {
	t.Parallel()
	env := NewFeatureBranchEnv(t)

	// Create a session
	session := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit failed: %v", err)
	}

	content := "def hello; end"
	env.WriteFile("hello.rb", content)

	session.CreateTranscript(
		"Create hello method",
		[]FileChange{{Path: "hello.rb", Content: content}},
	)
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	// Commit the session's changes (manual-commit requires user to commit)
	env.GitCommitWithShadowHooks("Create hello method", "hello.rb")

	featureBranch := env.GetCurrentBranch()

	// Create a local log WITHOUT a valid timestamp (can't be parsed)
	if err := os.MkdirAll(env.ClaudeProjectDir, 0o755); err != nil {
		t.Fatalf("failed to create Claude project dir: %v", err)
	}
	existingLog := filepath.Join(env.ClaudeProjectDir, session.ID+".jsonl")
	// Content without timestamp field - should be treated as "new"
	noTimestampContent := `{"type":"human","message":{"content":"no timestamp"}}`
	if err := os.WriteFile(existingLog, []byte(noTimestampContent), 0o644); err != nil {
		t.Fatalf("failed to write existing log: %v", err)
	}

	// Switch to main
	env.GitCheckoutBranch(masterBranch)

	// Resume WITHOUT --force should succeed (no timestamp = treated as new)
	output, err := env.RunResume(featureBranch)
	if err != nil {
		t.Fatalf("resume failed (should succeed when local has no timestamp): %v\nOutput: %s", err, output)
	}

	// Verify local log was overwritten with checkpoint content
	data, err := os.ReadFile(existingLog)
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}
	if strings.Contains(string(data), "no timestamp") {
		t.Errorf("local log should have been overwritten, but still has old content: %s", string(data))
	}
	if !strings.Contains(string(data), "Create hello method") {
		t.Errorf("restored log should contain checkpoint transcript, got: %s", string(data))
	}
}

// TestResume_SquashMergeMultipleCheckpoints tests resume when a squash merge commit
// contains multiple Trace-Checkpoint trailers from different sessions/commits.
// This simulates the GitHub squash merge workflow where:
// 1. Developer creates feature branch with multiple commits, each with its own checkpoint
// 2. PR is squash-merged to main, combining all commit messages (and their checkpoint trailers)
// 3. Feature branch is deleted
// 4. Running "trace resume main" should resume only from the latest checkpoint (most recent session)
func TestResume_SquashMergeMultipleCheckpoints(t *testing.T) {
	t.Parallel()
	env := NewFeatureBranchEnv(t)

	// === Session 1: First piece of work on feature branch ===
	session1 := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session1.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit session1 failed: %v", err)
	}

	content1 := "puts 'hello world'"
	env.WriteFile("hello.rb", content1)

	session1.CreateTranscript(
		"Create hello script",
		[]FileChange{{Path: "hello.rb", Content: content1}},
	)
	if err := env.SimulateStop(session1.ID, session1.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop session1 failed: %v", err)
	}

	// Commit session 1 (triggers condensation → checkpoint 1 on trace/checkpoints/v1)
	env.GitCommitWithShadowHooks("Create hello script", "hello.rb")
	checkpointID1 := env.GetLatestCheckpointID()
	t.Logf("Session 1 checkpoint: %s", checkpointID1)

	// === Session 2: Second piece of work on feature branch ===
	session2 := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session2.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit session2 failed: %v", err)
	}

	content2 := "puts 'goodbye world'"
	env.WriteFile("goodbye.rb", content2)

	session2.CreateTranscript(
		"Create goodbye script",
		[]FileChange{{Path: "goodbye.rb", Content: content2}},
	)
	if err := env.SimulateStop(session2.ID, session2.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop session2 failed: %v", err)
	}

	// Commit session 2 (triggers condensation → checkpoint 2 on trace/checkpoints/v1)
	env.GitCommitWithShadowHooks("Create goodbye script", "goodbye.rb")
	checkpointID2 := env.GetLatestCheckpointID()
	t.Logf("Session 2 checkpoint: %s", checkpointID2)

	// Verify we got two different checkpoint IDs
	if checkpointID1 == checkpointID2 {
		t.Fatalf("expected different checkpoint IDs, got same: %s", checkpointID1)
	}

	// === Simulate squash merge: switch to master, create squash commit ===
	env.GitCheckoutBranch(masterBranch)

	// Write the combined file changes (as if squash merged)
	env.WriteFile("hello.rb", content1)
	env.WriteFile("goodbye.rb", content2)
	env.GitAdd("hello.rb")
	env.GitAdd("goodbye.rb")

	// Create squash merge commit with both checkpoint trailers in the message
	// This mimics GitHub's squash merge format: PR title + individual commit messages
	env.GitCommitWithMultipleCheckpoints(
		"Feature branch (#1)\n\n* Create hello script\n\n* Create goodbye script",
		[]string{checkpointID1, checkpointID2},
	)

	// Remove local session logs (simulating a fresh machine or deleted local state)
	if err := os.RemoveAll(env.ClaudeProjectDir); err != nil {
		t.Fatalf("failed to remove Claude project dir: %v", err)
	}

	// === Run resume on master ===
	output, err := env.RunResume(masterBranch)
	if err != nil {
		t.Fatalf("resume failed: %v\nOutput: %s", err, output)
	}

	t.Logf("Resume output:\n%s", output)

	// Should show info about skipped checkpoints
	if !strings.Contains(output, "older checkpoints skipped") {
		t.Errorf("expected 'older checkpoints skipped' in output, got: %s", output)
	}

	// Should only resume the latest session (session2), not session1
	if strings.Contains(output, session1.ID) {
		t.Errorf("session1 ID %s should NOT appear in output (older checkpoint was skipped), got: %s", session1.ID, output)
	}
	if !strings.Contains(output, session2.ID) {
		t.Errorf("expected session2 ID %s in output, got: %s", session2.ID, output)
	}

	// Should contain claude -r command
	if !strings.Contains(output, "claude -r") {
		t.Errorf("expected 'claude -r' in output, got: %s", output)
	}
}

// TestResume_RelocatedRepo tests that resume works when a repository is moved
// to a different directory after checkpoint creation. This validates that resume
// reads checkpoint data from the git metadata branch (which travels with the repo)
// and writes transcripts to the current project dir, not any stored path from
// checkpoint creation time.
func TestResume_RelocatedRepo(t *testing.T) {
	t.Parallel()
	env := NewFeatureBranchEnv(t)

	// Create a session on the feature branch
	session := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit failed: %v", err)
	}

	content := "puts 'Hello from session'"
	env.WriteFile("hello.rb", content)

	session.CreateTranscript(
		"Create a hello script",
		[]FileChange{{Path: "hello.rb", Content: content}},
	)
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	// Commit the file (manual-commit requires user to commit with hooks)
	env.GitCommitWithShadowHooks("Create a hello script", "hello.rb")

	featureBranch := env.GetCurrentBranch()
	originalClaudeProjectDir := env.ClaudeProjectDir

	// Switch to master before moving the repo
	env.GitCheckoutBranch(masterBranch)

	// Move the repository to a completely different location
	newBase := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(newBase); err == nil {
		newBase = resolved
	}
	newRepoDir := filepath.Join(newBase, "relocated", "new-location", "test-repo")
	if err := os.MkdirAll(filepath.Dir(newRepoDir), 0o755); err != nil {
		t.Fatalf("failed to create parent dir: %v", err)
	}
	if err := os.Rename(env.RepoDir, newRepoDir); err != nil {
		t.Fatalf("failed to move repo: %v", err)
	}

	// Verify original location no longer exists
	if _, err := os.Stat(env.RepoDir); !os.IsNotExist(err) {
		t.Fatalf("original repo dir should not exist after move")
	}
	t.Logf("Moved repo from %s to %s", env.RepoDir, newRepoDir)

	// Create a fresh Claude project dir for the new location
	newClaudeProjectDir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(newClaudeProjectDir); err == nil {
		newClaudeProjectDir = resolved
	}

	// Create a new TestEnv pointing at the relocated repo
	newEnv := &TestEnv{
		T:                t,
		RepoDir:          newRepoDir,
		ClaudeProjectDir: newClaudeProjectDir,
	}

	// Run resume in the relocated repo with --force to bypass any timestamp checks
	output, err := newEnv.RunResumeForce(featureBranch)
	if err != nil {
		t.Fatalf("resume in relocated repo failed: %v\nOutput: %s", err, output)
	}
	t.Logf("Resume output:\n%s", output)

	// Verify we switched to the feature branch
	if branch := newEnv.GetCurrentBranch(); branch != featureBranch {
		t.Errorf("expected to be on %s, got %s", featureBranch, branch)
	}

	// Verify transcript was restored to the NEW Claude project dir
	transcriptFiles, err := filepath.Glob(filepath.Join(newClaudeProjectDir, "*.jsonl"))
	if err != nil {
		t.Fatalf("failed to glob transcript files: %v", err)
	}
	if len(transcriptFiles) == 0 {
		t.Fatal("expected transcript file to be restored to new Claude project dir")
	}

	// Verify the transcript contains the original session content
	data, err := os.ReadFile(transcriptFiles[0])
	if err != nil {
		t.Fatalf("failed to read restored transcript: %v", err)
	}
	if !strings.Contains(string(data), "Create a hello script") {
		t.Errorf("restored transcript should contain session content, got: %s", string(data))
	}

	// Verify the OLD Claude project dir was NOT written to by resume
	oldTranscriptFiles, err := filepath.Glob(filepath.Join(originalClaudeProjectDir, "*.jsonl"))
	if err != nil {
		t.Fatalf("failed to glob old transcript files: %v", err)
	}
	if len(oldTranscriptFiles) > 0 {
		t.Errorf("old Claude project dir should not have transcript files after resume, but found %d", len(oldTranscriptFiles))
	}

	// Verify output contains session info
	if !strings.Contains(output, "Restored session") {
		t.Errorf("output should contain 'Restored session', got: %s", output)
	}
}
