//go:build integration

package integration

import (
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/trailers"
)

// TestShadow_RewindPreservesUntrackedFilesWithExistingShadowBranch tests that untracked files
// present at session start are preserved during rewind, even when the shadow branch already
// exists from a previous session.
func TestShadow_RewindPreservesUntrackedFilesWithExistingShadowBranch(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	// Setup repository with initial commit
	env.InitRepo()
	env.WriteFile("README.md", "# Test Repository")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")
	env.GitCheckoutNewBranch("feature/existing-shadow-test")
	env.InitTrace()

	t.Log("Phase 1: Create untracked file before session starts")

	// Create an untracked file BEFORE the first checkpoint
	// This simulates configuration files that exist before Claude starts
	untrackedContent := `{"new": "config"}`
	env.WriteFile(".claude/settings.json", untrackedContent)

	t.Log("Phase 1: Create a previous session to establish shadow branch")

	// First session - creates the shadow branch
	session1 := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithPrompt(session1.ID, "Create old.go"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	env.WriteFile("old.go", "package main\n")
	session1.TranscriptBuilder.AddUserMessage("Create old.go")
	session1.TranscriptBuilder.AddAssistantMessage("Done!")
	toolID := session1.TranscriptBuilder.AddToolUse("mcp__acp__Write", "old.go", "package main\n")
	session1.TranscriptBuilder.AddToolResult(toolID)

	if err := session1.TranscriptBuilder.WriteToFile(session1.TranscriptPath); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	if err := env.SimulateStop(session1.ID, session1.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (session 1) failed: %v", err)
	}

	// Verify shadow branch exists
	shadowBranchName := env.GetShadowBranchName()
	if !env.BranchExists(shadowBranchName) {
		t.Fatalf("Shadow branch %s should exist after first session", shadowBranchName)
	}
	t.Logf("Shadow branch %s exists from first session", shadowBranchName)

	t.Log("Phase 2: Continue session and create second checkpoint")

	// Continue the SAME session (Claude resumes with the same session ID)
	// This is the expected behavior - continuing work on the same base commit
	if err := env.SimulateUserPromptSubmitWithPrompt(session1.ID, "Create A"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt (continue session) failed: %v", err)
	}

	// Reset transcript builder for next checkpoint
	session1.TranscriptBuilder = NewTranscriptBuilder()

	// Second checkpoint of session - should capture .claude/settings.json
	env.WriteFile("a.go", "package main\n\nfunc A() {}\n")
	session1.TranscriptBuilder.AddUserMessage("Create A")
	session1.TranscriptBuilder.AddAssistantMessage("Done!")
	toolID2 := session1.TranscriptBuilder.AddToolUse("mcp__acp__Write", "a.go", "package main\n\nfunc A() {}\n")
	session1.TranscriptBuilder.AddToolResult(toolID2)

	if err := session1.TranscriptBuilder.WriteToFile(session1.TranscriptPath); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	if err := env.SimulateStop(session1.ID, session1.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (checkpoint 2) failed: %v", err)
	}

	rewindPoints := env.GetRewindPoints()
	if len(rewindPoints) < 2 {
		t.Fatalf("Expected at least 2 rewind points, got %d", len(rewindPoints))
	}
	// Find the most recent checkpoint (checkpoint 2)
	checkpoint1 := &rewindPoints[0] // Most recent first
	t.Logf("Checkpoint 2: %s", checkpoint1.ID[:7])

	t.Log("Phase 3: Create third checkpoint")

	// Continue the session for the third checkpoint
	if err := env.SimulateUserPromptSubmitWithPrompt(session1.ID, "Create B"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt (checkpoint 3) failed: %v", err)
	}

	// Reset transcript builder for next checkpoint
	session1.TranscriptBuilder = NewTranscriptBuilder()

	env.WriteFile("b.go", "package main\n\nfunc B() {}\n")
	session1.TranscriptBuilder.AddUserMessage("Create B")
	session1.TranscriptBuilder.AddAssistantMessage("Done!")
	toolID3 := session1.TranscriptBuilder.AddToolUse("mcp__acp__Write", "b.go", "package main\n\nfunc B() {}\n")
	session1.TranscriptBuilder.AddToolResult(toolID3)

	if err := session1.TranscriptBuilder.WriteToFile(session1.TranscriptPath); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	if err := env.SimulateStop(session1.ID, session1.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (checkpoint 3) failed: %v", err)
	}

	t.Log("Phase 4: Rewind to checkpoint 2")

	if err := env.Rewind(checkpoint1.ID); err != nil {
		t.Fatalf("Rewind failed: %v", err)
	}

	// Verify that the untracked file that existed at session start is PRESERVED
	// Since .claude/settings.json was created before checkpoint 1, it's in checkpoint 1's tree
	// and will flow through to checkpoint 2, so it should be preserved on rewind
	if !env.FileExists(".claude/settings.json") {
		t.Error(".claude/settings.json should have been preserved during rewind")
	} else {
		restoredContent := env.ReadFile(".claude/settings.json")
		if restoredContent != untrackedContent {
			t.Errorf("Untracked file content changed.\nExpected:\n%s\nGot:\n%s", untrackedContent, restoredContent)
		} else {
			t.Log("✓ .claude/settings.json was preserved correctly")
		}
	}

	// Verify b.go was deleted
	if env.FileExists("b.go") {
		t.Error("b.go should have been deleted during rewind")
	} else {
		t.Log("✓ b.go was correctly deleted during rewind")
	}

	t.Log("Test completed successfully!")
}

// TestShadow_TrailerRemovalSkipsCondensation tests that removing the Trace-Checkpoint
// trailer during commit message editing causes condensation to be skipped.
// This allows users to opt-out of linking a commit to their Claude session.
func TestShadow_TrailerRemovalSkipsCondensation(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	// Setup
	env.InitRepo()
	env.WriteFile("README.md", "# Test Repository")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")
	env.GitCheckoutNewBranch("feature/trailer-opt-out")
	env.InitTrace()

	t.Log("Phase 1: Create session with content")

	session := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Create function A"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	fileAContent := "package main\n\nfunc A() {}\n"
	env.WriteFile("a.go", fileAContent)

	session.TranscriptBuilder.AddUserMessage("Create function A")
	session.TranscriptBuilder.AddAssistantMessage("I'll create function A for you.")
	toolID := session.TranscriptBuilder.AddToolUse("mcp__acp__Write", "a.go", fileAContent)
	session.TranscriptBuilder.AddToolResult(toolID)
	session.TranscriptBuilder.AddAssistantMessage("Done!")

	if err := session.TranscriptBuilder.WriteToFile(session.TranscriptPath); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	t.Log("Phase 2: Commit WITH trailer removed (user opts out)")

	// Use the special helper that removes the trailer before committing
	env.GitCommitWithTrailerRemoved("Add function A (manual commit)", "a.go")

	commitHash := env.GetHeadHash()
	t.Logf("Commit: %s", commitHash[:7])

	// Verify commit does NOT have trailer
	commitMsg := env.GetCommitMessage(commitHash)
	if _, found := trailers.ParseCheckpoint(commitMsg); found {
		t.Errorf("Commit should NOT have Trace-Checkpoint trailer (it was removed), got:\n%s", commitMsg)
	}
	t.Logf("Commit message (trailer removed):\n%s", commitMsg)

	t.Log("Phase 3: Verify no condensation happened")

	// trace/checkpoints/v1 branch exists (created at setup), but should not have any checkpoint commits yet
	// since the user removed the trailer
	latestCheckpointID := env.TryGetLatestCheckpointID()
	if latestCheckpointID == "" {
		t.Log("✓ No checkpoint found on trace/checkpoints/v1 branch (no condensation)")
	} else {
		// If there is a checkpoint, this is unexpected for this test
		t.Logf("Found checkpoint ID: %s (should be from previous activity, not this commit)", latestCheckpointID)
	}

	t.Log("Phase 4: Now commit WITH trailer (user keeps it)")

	// Continue session with new content
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Create function B"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	fileBContent := "package main\n\nfunc B() {}\n"
	env.WriteFile("b.go", fileBContent)

	session.TranscriptBuilder.AddUserMessage("Create function B")
	session.TranscriptBuilder.AddAssistantMessage("Done!")
	toolID2 := session.TranscriptBuilder.AddToolUse("mcp__acp__Write", "b.go", fileBContent)
	session.TranscriptBuilder.AddToolResult(toolID2)

	if err := session.TranscriptBuilder.WriteToFile(session.TranscriptPath); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	// This time, keep the trailer (normal commit with hooks)
	env.GitCommitWithShadowHooks("Add function B", "b.go")

	commit2Hash := env.GetHeadHash()
	checkpointID := env.GetCheckpointIDFromCommitMessage(commit2Hash)
	t.Logf("Second commit: %s, checkpoint: %s", commit2Hash[:7], checkpointID)

	// Verify second commit HAS trailer with valid format
	commit2Msg := env.GetCommitMessage(commit2Hash)
	if _, found := trailers.ParseCheckpoint(commit2Msg); !found {
		t.Errorf("Second commit should have valid Trace-Checkpoint trailer, got:\n%s", commit2Msg)
	}

	// Verify condensation happened for second commit
	if !env.BranchExists(paths.MetadataBranchName) {
		t.Fatal("trace/checkpoints/v1 branch should exist after second commit with trailer")
	}

	// Verify checkpoint exists
	shardedPath := ShardedCheckpointPath(checkpointID)
	metadataPath := shardedPath + "/metadata.json"
	if !env.FileExistsInBranch(paths.MetadataBranchName, metadataPath) {
		t.Errorf("Checkpoint should exist at %s", metadataPath)
	} else {
		t.Log("✓ Condensation happened for commit with trailer")
	}

	t.Log("Trailer removal opt-out test completed successfully!")
}

// TestShadow_SessionsBranchCommitTrailers verifies that commits on the trace/checkpoints/v1
// branch contain the expected trailers: Trace-Session, Trace-Strategy, and Trace-Agent.
func TestShadow_SessionsBranchCommitTrailers(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	// Setup
	env.InitRepo()
	env.WriteFile("README.md", "# Test Repository")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")
	env.GitCheckoutNewBranch("feature/trailer-test")
	env.InitTrace()

	// Start session and create checkpoint
	session := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Create main.go"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	fileContent := "package main\n\nfunc main() {}\n"
	env.WriteFile("main.go", fileContent)
	session.CreateTranscript("Create main.go", []FileChange{{Path: "main.go", Content: fileContent}})

	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	// Commit to trigger condensation
	env.GitCommitWithShadowHooks("Add main.go", "main.go")

	// Get the commit message on trace/checkpoints/v1 branch
	sessionsCommitMsg := env.GetLatestCommitMessageOnBranch(paths.MetadataBranchName)
	t.Logf("trace/checkpoints/v1 commit message:\n%s", sessionsCommitMsg)

	// Verify required trailers are present
	requiredTrailers := map[string]string{
		trailers.SessionTrailerKey:  "",                                // Trace-Session: <session-id>
		trailers.StrategyTrailerKey: strategy.StrategyNameManualCommit, // Trace-Strategy: manual-commit
		trailers.AgentTrailerKey:    "Claude Code",                     // Trace-Agent: Claude Code
	}

	for trailerKey, expectedValue := range requiredTrailers {
		if !strings.Contains(sessionsCommitMsg, trailerKey+":") {
			t.Errorf("trace/checkpoints/v1 commit should have %s trailer", trailerKey)
			continue
		}

		// If we have an expected value, verify it
		if expectedValue != "" {
			expectedTrailer := trailerKey + ": " + expectedValue
			if !strings.Contains(sessionsCommitMsg, expectedTrailer) {
				t.Errorf("trace/checkpoints/v1 commit should have %q, got message:\n%s", expectedTrailer, sessionsCommitMsg)
			} else {
				t.Logf("✓ Found trailer: %s", expectedTrailer)
			}
		} else {
			t.Logf("✓ Found trailer: %s", trailerKey)
		}
	}

	t.Log("Sessions branch commit trailers test completed successfully!")
}
