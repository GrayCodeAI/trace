//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/strategy"
)

// TestShadow_FullWorkflow tests the complete shadow workflow as described in
// docs/requirements/shadow-strategy/example.md
//
// This test simulates Alice's workflow:
// 1. Start session, create checkpoints
// 2. Rewind to earlier checkpoint
// 3. Create new checkpoint after rewind
// 4. User commits (triggers condensation)
// 5. Continue working after commit (new shadow branch)
// 6. User commits again (second condensation)
// 7. Verify final state
func TestShadow_FullWorkflow(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	// ========================================
	// Phase 1: Setup
	// ========================================
	env.InitRepo()

	// Create initial commit on main
	env.WriteFile("README.md", "# Test Repository")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")

	// Switch to feature branch (shadow skips main/master)
	env.GitCheckoutNewBranch("feature/auth")

	// Initialize Trace AFTER branch switch to avoid go-git cleaning untracked files
	env.InitTrace()

	initialHead := env.GetHeadHash()
	t.Logf("Initial HEAD on feature/auth: %s", initialHead[:7])

	// ========================================
	// Phase 2: Session Start & First Checkpoint
	// ========================================
	t.Log("Phase 2: Starting session and creating first checkpoint")

	session := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Create authentication module"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	// Verify session state file exists in .git/trace-sessions/
	sessionStateDir := filepath.Join(env.RepoDir, ".git", "trace-sessions")
	entries, err := os.ReadDir(sessionStateDir)
	if err != nil {
		t.Fatalf("Failed to read session state dir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("Expected session state file in .git/trace-sessions/")
	}

	// Create first file (src/auth.go)
	authV1 := "package auth\n\nfunc Authenticate(user, pass string) bool {\n\treturn false\n}"
	env.WriteFile("src/auth.go", authV1)

	session.CreateTranscript(
		"Create authentication module",
		[]FileChange{{Path: "src/auth.go", Content: authV1}},
	)
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (checkpoint 1) failed: %v", err)
	}

	// Verify shadow branch created with correct worktree-specific naming
	expectedShadowBranch := env.GetShadowBranchNameForCommit(initialHead)
	if !env.BranchExists(expectedShadowBranch) {
		t.Errorf("Expected shadow branch %s to exist", expectedShadowBranch)
	}

	// Verify 1 rewind point
	points := env.GetRewindPoints()
	if len(points) != 1 {
		t.Fatalf("Expected 1 rewind point after first checkpoint, got %d", len(points))
	}
	t.Logf("Checkpoint 1 created: %s", points[0].Message)

	// ========================================
	// Phase 3: Second Checkpoint (continuing same session)
	// ========================================
	t.Log("Phase 3: Creating second checkpoint (continuing same session)")

	// Continue the same session (not a new session) - this is the expected Claude behavior
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Add password hashing"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt (checkpoint 2) failed: %v", err)
	}

	// Create hash.go and modify auth.go
	hashV1 := "package auth\n\nimport \"crypto/sha256\"\n\nfunc HashPassword(pass string) []byte {\n\th := sha256.Sum256([]byte(pass))\n\treturn h[:]\n}"
	authV2 := "package auth\n\nfunc Authenticate(user, pass string) bool {\n\t// TODO: use hashed passwords\n\treturn false\n}"
	env.WriteFile("src/hash.go", hashV1)
	env.WriteFile("src/auth.go", authV2)

	// Reset transcript builder for the new checkpoint
	session.TranscriptBuilder = NewTranscriptBuilder()
	session.CreateTranscript(
		"Add password hashing",
		[]FileChange{
			{Path: "src/hash.go", Content: hashV1},
			{Path: "src/auth.go", Content: authV2},
		},
	)
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (checkpoint 2) failed: %v", err)
	}

	// Verify 2 rewind points on same shadow branch
	points = env.GetRewindPoints()
	if len(points) != 2 {
		t.Fatalf("Expected 2 rewind points after second checkpoint, got %d", len(points))
	}
	t.Logf("Checkpoint 2 created: %s", points[0].Message)

	// Verify both files exist
	if !env.FileExists("src/hash.go") {
		t.Error("src/hash.go should exist after checkpoint 2")
	}
	if content := env.ReadFile("src/auth.go"); content != authV2 {
		t.Errorf("src/auth.go should have v2 content, got: %s", content)
	}

	// ========================================
	// Phase 4: Rewind to First Checkpoint
	// ========================================
	t.Log("Phase 4: Rewinding to first checkpoint")

	// Find checkpoint 1 by message (the one for "Create authentication module")
	var checkpoint1ID string
	for _, p := range points {
		if p.Message == "Create authentication module" {
			checkpoint1ID = p.ID
			break
		}
	}
	if checkpoint1ID == "" {
		t.Fatalf("Could not find checkpoint for 'Create authentication module' in %d points", len(points))
	}

	if err := env.Rewind(checkpoint1ID); err != nil {
		t.Fatalf("Rewind to checkpoint 1 failed: %v", err)
	}

	// Verify hash.go was removed (it was only added in checkpoint 2)
	if env.FileExists("src/hash.go") {
		t.Error("src/hash.go should NOT exist after rewind to checkpoint 1")
	}

	// Verify auth.go restored to v1 (without the "TODO: use hashed passwords" comment)
	content := env.ReadFile("src/auth.go")
	if content != authV1 {
		t.Errorf("src/auth.go should be restored to v1 after rewind, got: %s", content)
	}

	// Verify HEAD unchanged (shadow doesn't modify user's branch)
	if head := env.GetHeadHash(); head != initialHead {
		t.Errorf("HEAD should be unchanged after rewind, got %s, want %s", head[:7], initialHead[:7])
	}

	// Verify shadow branch still exists (history preserved)
	if !env.BranchExists(expectedShadowBranch) {
		t.Errorf("Shadow branch %s should still exist after rewind", expectedShadowBranch)
	}

	// ========================================
	// Phase 5: New Checkpoint After Rewind (continue same session)
	// ========================================
	t.Log("Phase 5: Creating checkpoint after rewind (continuing same session)")

	// Continue the same session after rewind
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Use bcrypt for hashing"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt (after rewind) failed: %v", err)
	}

	// Reset transcript builder for next checkpoint
	session.TranscriptBuilder = NewTranscriptBuilder()

	// Create bcrypt.go instead (different approach)
	bcryptV1 := "package auth\n\nimport \"golang.org/x/crypto/bcrypt\"\n\nfunc HashPassword(pass string) ([]byte, error) {\n\treturn bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)\n}"
	authV3 := "package auth\n\nfunc Authenticate(user, pass string) bool {\n\t// Use bcrypt for password hashing\n\treturn false\n}"
	env.WriteFile("src/bcrypt.go", bcryptV1)
	env.WriteFile("src/auth.go", authV3)

	session.CreateTranscript(
		"Use bcrypt for hashing",
		[]FileChange{
			{Path: "src/bcrypt.go", Content: bcryptV1},
			{Path: "src/auth.go", Content: authV3},
		},
	)
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (checkpoint 3) failed: %v", err)
	}

	// Verify we now have checkpoints (may be 3 if history preserved, or could vary)
	points = env.GetRewindPoints()
	if len(points) < 1 {
		t.Fatalf("Expected at least 1 rewind point after checkpoint 3, got %d", len(points))
	}
	t.Logf("After rewind and new checkpoint: %d rewind points", len(points))

	// ========================================
	// Phase 6: User Commits (Condensation)
	// ========================================
	t.Log("Phase 6: User commits - triggering condensation")

	// Stage and commit with shadow hooks
	env.GitCommitWithShadowHooks("Add user authentication with bcrypt", "src/auth.go", "src/bcrypt.go")

	// Get the new commit
	commit1Hash := env.GetHeadHash()
	t.Logf("User commit 1: %s", commit1Hash[:7])

	// Active branch commits should be clean (no Trace-* trailers)
	commitMsg := env.GetCommitMessage(commit1Hash)
	if strings.Contains(commitMsg, "Trace-Session:") {
		t.Errorf("Commit should NOT have Trace-Session trailer (clean history), got: %s", commitMsg)
	}
	if strings.Contains(commitMsg, "Trace-Condensation:") {
		t.Errorf("Commit should NOT have Trace-Condensation trailer (clean history), got: %s", commitMsg)
	}

	// Get checkpoint ID by walking history - verifies condensation added the trailer
	checkpoint1ID = env.GetLatestCheckpointIDFromHistory()
	t.Logf("Checkpoint 1 ID: %s", checkpoint1ID)

	// Verify trace/checkpoints/v1 branch exists with checkpoint folder
	if !env.BranchExists(paths.MetadataBranchName) {
		t.Error("trace/checkpoints/v1 branch should exist after condensation")
	}

	// Verify checkpoint folder contents (check via git show)
	// Uses sharded path: <id[:2]>/<id[2:]>/metadata.json
	checkpointPath := ShardedCheckpointPath(checkpoint1ID) + "/metadata.json"
	if !env.FileExistsInBranch(paths.MetadataBranchName, checkpointPath) {
		t.Errorf("Checkpoint folder should contain metadata.json at %s", checkpointPath)
	}

	// Clear session state to simulate session completion (avoids concurrent session warning)
	if err := env.ClearSessionState(session.ID); err != nil {
		t.Fatalf("ClearSessionState failed: %v", err)
	}

	// ========================================
	// Phase 7: Continue Working After Commit
	// ========================================
	t.Log("Phase 7: Continuing work after user commit")

	// Verify HEAD changed
	if commit1Hash == initialHead {
		t.Error("HEAD should have changed after user commit")
	}

	// Start new session
	session4 := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithPrompt(session4.ID, "Add session management"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt (session4) failed: %v", err)
	}

	// Create session management file
	sessionMgmt := "package auth\n\nimport \"time\"\n\ntype Session struct {\n\tUserID string\n\tExpiry time.Time\n}"
	env.WriteFile("src/session.go", sessionMgmt)

	session4.CreateTranscript(
		"Add session management",
		[]FileChange{{Path: "src/session.go", Content: sessionMgmt}},
	)
	if err := env.SimulateStop(session4.ID, session4.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (checkpoint 4) failed: %v", err)
	}

	// Verify NEW shadow branch created (based on new HEAD)
	expectedShadowBranch2 := env.GetShadowBranchNameForCommit(commit1Hash)
	if !env.BranchExists(expectedShadowBranch2) {
		t.Errorf("Expected new shadow branch %s after commit", expectedShadowBranch2)
	}

	// Verify it's different from the first shadow branch
	if expectedShadowBranch == expectedShadowBranch2 {
		t.Error("New shadow branch should have different name than first (different base commit)")
	}
	t.Logf("New shadow branch after commit: %s", expectedShadowBranch2)

	// ========================================
	// Phase 8: Second User Commit
	// ========================================
	t.Log("Phase 8: Second user commit")

	env.GitCommitWithShadowHooks("Add session management", "src/session.go")

	commit2Hash := env.GetHeadHash()
	t.Logf("User commit 2: %s", commit2Hash[:7])

	// Verify commit is clean (no trailers)
	commitMsg2 := env.GetCommitMessage(commit2Hash)
	if strings.Contains(commitMsg2, "Trace-Session:") {
		t.Errorf("Commit should NOT have Trace-Session trailer (clean history), got: %s", commitMsg2)
	}

	// Get checkpoint ID from commit message trailer (not from timestamp-based matching
	// which is flaky when commits happen within the same second)
	checkpoint2ID := env.GetCheckpointIDFromCommitMessage(commit2Hash)
	t.Logf("Checkpoint 2 ID: %s", checkpoint2ID)

	// Verify DIFFERENT checkpoint ID
	if checkpoint1ID == checkpoint2ID {
		t.Errorf("Second commit should have different checkpoint ID: %s vs %s", checkpoint1ID, checkpoint2ID)
	}

	// Verify second checkpoint folder exists (uses sharded path)
	checkpoint2Path := ShardedCheckpointPath(checkpoint2ID) + "/metadata.json"
	if !env.FileExistsInBranch(paths.MetadataBranchName, checkpoint2Path) {
		t.Errorf("Second checkpoint folder should exist at %s", checkpoint2Path)
	}

	// ========================================
	// Phase 9: Verify Final State
	// ========================================
	t.Log("Phase 9: Verifying final state")

	// 2 user commits on feature branch
	// Both should be clean (no Trace-* trailers)
	if strings.Contains(commitMsg, "Trace-Session:") || strings.Contains(commitMsg2, "Trace-Session:") {
		t.Error("Commits should NOT have Trace-Session trailer (clean history)")
	}

	// 2 checkpoint folders in trace/checkpoints/v1 (Already verified above)

	// Verify all expected files exist in working directory
	expectedFiles := []string{"README.md", "src/auth.go", "src/bcrypt.go", "src/session.go"}
	for _, f := range expectedFiles {
		if !env.FileExists(f) {
			t.Errorf("Expected file %s to exist in final state", f)
		}
	}

	// Verify shadow branches exist (can be pruned later)
	if !env.BranchExists(expectedShadowBranch) {
		t.Logf("Note: First shadow branch %s may have been cleaned up", expectedShadowBranch)
	}
	if !env.BranchExists(expectedShadowBranch2) {
		t.Logf("Note: Second shadow branch %s may have been cleaned up", expectedShadowBranch2)
	}

	t.Log("Shadow full workflow test completed successfully!")
}

// TestShadow_SessionStateLocation verifies session state is stored in .git/
// (not .trace/) so it's never accidentally committed.
func TestShadow_SessionStateLocation(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	env.InitRepo()

	env.WriteFile("README.md", "# Test")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")

	env.GitCheckoutNewBranch("feature/test")

	// Initialize AFTER branch switch
	env.InitTrace()

	session := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit failed: %v", err)
	}

	// Session state should be in .git/trace-sessions/, NOT .trace/
	gitSessionDir := filepath.Join(env.RepoDir, ".git", "trace-sessions")
	traceSessionDir := filepath.Join(env.RepoDir, ".trace", "trace-sessions")

	if _, err := os.Stat(gitSessionDir); os.IsNotExist(err) {
		t.Error("Session state directory should exist at .git/trace-sessions/")
	}

	if _, err := os.Stat(traceSessionDir); err == nil {
		t.Error("Session state should NOT be in .trace/trace-sessions/")
	}
}

// TestShadow_MultipleConcurrentSessions tests that starting a second Claude session
// while another session has uncommitted checkpoints triggers a warning.
// The first prompt is blocked with continue:false, subsequent prompts proceed.
func TestShadow_MultipleConcurrentSessions(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	env.InitRepo()

	env.WriteFile("README.md", "# Test")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")

	env.GitCheckoutNewBranch("feature/test")

	// Initialize AFTER branch switch
	env.InitTrace()

	// Start first session
	session1 := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session1.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit (session1) failed: %v", err)
	}

	// Create a checkpoint for session1 (this creates the shadow branch)
	env.WriteFile("file.txt", "content")
	session1.CreateTranscript("Add file", []FileChange{{Path: "file.txt", Content: "content"}})
	if err := env.SimulateStop(session1.ID, session1.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (session1) failed: %v", err)
	}

	// Verify session state file exists
	sessionStateDir := filepath.Join(env.RepoDir, ".git", "trace-sessions")
	entries, err := os.ReadDir(sessionStateDir)
	if err != nil {
		t.Fatalf("Failed to read session state dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("Expected 1 session state file, got %d", len(entries))
	}

	// Starting a second session while session1 has uncommitted checkpoints triggers warning
	// The hook outputs JSON {"continue":false,...} but returns nil (success)
	session2 := env.NewSession()
	err = env.SimulateUserPromptSubmit(session2.ID)
	// The hook succeeds (returns nil) but outputs JSON with continue:false
	// The test infrastructure treats this as success since the hook didn't return an error
	if err != nil {
		t.Logf("SimulateUserPromptSubmit returned error (expected in some cases): %v", err)
	}

	// Verify session2 state file was created with ConcurrentWarningShown flag
	// This is set by the hook when it outputs continue:false
	entries, err = os.ReadDir(sessionStateDir)
	if err != nil {
		t.Fatalf("Failed to read session state dir: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("Expected 2 session state files after second session attempt, got %d", len(entries))
	}

	// Clear session1 state file - this makes the shadow branch "orphaned"
	if err := env.ClearSessionState(session1.ID); err != nil {
		t.Fatalf("ClearSessionState failed: %v", err)
	}

	// Session2 had ConcurrentWarningShown=true, but now the conflict is resolved
	// (session1 state cleared), so the warning flag is cleared and hooks proceed normally.
	// The orphaned shadow branch (from session1) is reset, allowing session2 to proceed.
	err = env.SimulateUserPromptSubmit(session2.ID)
	if err != nil {
		t.Errorf("Expected success after orphaned shadow branch is reset, got: %v", err)
	} else {
		t.Log("Session2 proceeded after orphaned shadow branch was reset")
	}
}

// TestShadow_ShadowBranchMigrationOnPull verifies that when the base commit changes
// (e.g., after stash → pull → apply), the shadow branch is moved to the new commit.
func TestShadow_ShadowBranchMigrationOnPull(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	env.InitRepo()

	env.WriteFile("README.md", "# Test")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")

	env.GitCheckoutNewBranch("feature/test")
	env.InitTrace()

	originalHead := env.GetHeadHash()
	originalShadowBranch := env.GetShadowBranchNameForCommit(originalHead)

	// Start session and create checkpoint
	session := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit failed: %v", err)
	}

	env.WriteFile("file.txt", "content")
	session.CreateTranscript("Add file", []FileChange{{Path: "file.txt", Content: "content"}})
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	// Verify shadow branch exists at original commit
	if !env.BranchExists(originalShadowBranch) {
		t.Fatalf("Shadow branch %s should exist", originalShadowBranch)
	}
	t.Logf("Original shadow branch: %s", originalShadowBranch)

	// Simulate pull: create a new commit (simulating what pull would do)
	// In real scenario: stash → pull → apply
	// Here we just create a commit to simulate HEAD moving
	env.WriteFile("pulled.txt", "from remote")
	env.GitAdd("pulled.txt")
	env.GitCommit("Simulated pull commit")

	newHead := env.GetHeadHash()
	newShadowBranch := env.GetShadowBranchNameForCommit(newHead)
	t.Logf("After simulated pull: old=%s new=%s", originalHead[:7], newHead[:7])

	// Restore the file (simulating stash apply)
	env.WriteFile("file.txt", "content")

	// Next prompt should migrate the shadow branch
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit after pull failed: %v", err)
	}

	// Verify old shadow branch is gone and new one exists
	if env.BranchExists(originalShadowBranch) {
		t.Errorf("Old shadow branch %s should be deleted after migration", originalShadowBranch)
	}
	if !env.BranchExists(newShadowBranch) {
		t.Errorf("New shadow branch %s should exist after migration", newShadowBranch)
	}

	// Verify we can still create checkpoints on the new shadow branch
	env.WriteFile("file2.txt", "more content")
	session.TranscriptBuilder = NewTranscriptBuilder()
	session.CreateTranscript("Add file2", []FileChange{{Path: "file2.txt", Content: "more content"}})
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop after migration failed: %v", err)
	}

	// Verify session state has updated base commit and preserves agent type
	state, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState failed: %v", err)
	}
	if state.BaseCommit != newHead {
		t.Errorf("Session base commit should be %s, got %s", newHead[:7], state.BaseCommit[:7])
	}
	if state.StepCount != 2 {
		t.Errorf("Expected 2 checkpoints after migration, got %d", state.StepCount)
	}
	// Verify agent_type is preserved across checkpoints and migration
	expectedAgentType := agent.AgentTypeClaudeCode
	if state.AgentType != expectedAgentType {
		t.Errorf("Session AgentType should be %q, got %q", expectedAgentType, state.AgentType)
	}

	t.Log("Shadow branch successfully migrated after base commit change")
}

// TestShadow_ShadowBranchNaming verifies shadow branches follow the
// trace/<base-sha[:7]> naming convention.
func TestShadow_ShadowBranchNaming(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	env.InitRepo()

	env.WriteFile("README.md", "# Test")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")

	env.GitCheckoutNewBranch("feature/test")

	// Initialize AFTER branch switch
	env.InitTrace()

	baseHead := env.GetHeadHash()

	session := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit failed: %v", err)
	}

	env.WriteFile("file.txt", "content")
	session.CreateTranscript("Add file", []FileChange{{Path: "file.txt", Content: "content"}})

	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	// Verify shadow branch name matches worktree-specific format
	expectedBranch := env.GetShadowBranchNameForCommit(baseHead)
	if !env.BranchExists(expectedBranch) {
		t.Errorf("Shadow branch should be named %s", expectedBranch)
	}

	// List all trace/ branches
	branches := env.ListBranchesWithPrefix("trace/")
	t.Logf("Found trace/ branches: %v", branches)

	foundExpected := false
	for _, b := range branches {
		if b == expectedBranch {
			foundExpected = true
			break
		}
	}
	if !foundExpected {
		t.Errorf("Expected branch %s not found in %v", expectedBranch, branches)
	}
}

// TestShadow_TranscriptCondensation verifies that session transcripts are
// included in the trace/checkpoints/v1 branch during condensation.
func TestShadow_TranscriptCondensation(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	// Setup
	env.InitRepo()
	env.WriteFile("README.md", "# Test Repository")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")
	env.GitCheckoutNewBranch("feature/test")
	env.InitTrace()

	// Start session and create checkpoint with transcript
	session := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Create main.go with hello world"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	// Create a file change
	content := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	env.WriteFile("main.go", content)

	// Create transcript with meaningful content
	session.CreateTranscript(
		"Create main.go with hello world",
		[]FileChange{{Path: "main.go", Content: content}},
	)

	// Save checkpoint (this stores transcript in shadow branch)
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	// Commit with hooks (triggers condensation)
	env.GitCommitWithShadowHooks("Add main.go", "main.go")

	// Get checkpoint ID from trace/checkpoints/v1 branch (not from commit message)
	checkpointID := env.GetLatestCheckpointID()
	t.Logf("Checkpoint ID: %s", checkpointID)

	// Verify trace/checkpoints/v1 branch exists
	if !env.BranchExists(paths.MetadataBranchName) {
		t.Fatal("trace/checkpoints/v1 branch should exist after condensation")
	}

	// Comprehensive checkpoint validation
	env.ValidateCheckpoint(CheckpointValidation{
		CheckpointID:    checkpointID,
		SessionID:       session.ID,
		Strategy:        strategy.StrategyNameManualCommit,
		FilesTouched:    []string{"main.go"},
		ExpectedPrompts: []string{"Create main.go with hello world"},
		ExpectedTranscriptContent: []string{
			"Create main.go with hello world",
			"main.go",
		},
	})

	// Additionally verify agent field in session metadata
	sessionMetadataPath := SessionFilePath(checkpointID, paths.MetadataFileName)
	sessionMetadataContent, found := env.ReadFileFromBranch(paths.MetadataBranchName, sessionMetadataPath)
	if !found {
		t.Fatal("session metadata.json should be readable")
	}
	var sessionMetadata checkpoint.CommittedMetadata
	if err := json.Unmarshal([]byte(sessionMetadataContent), &sessionMetadata); err != nil {
		t.Fatalf("failed to parse session metadata.json: %v", err)
	}
	expectedAgent := agent.AgentTypeClaudeCode
	if sessionMetadata.Agent != expectedAgent {
		t.Errorf("session metadata.Agent = %q, want %q", sessionMetadata.Agent, expectedAgent)
	} else {
		t.Logf("✓ Session metadata has agent: %q", sessionMetadata.Agent)
	}
}
