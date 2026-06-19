//go:build integration

package integration

import (
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/paths"
)

// TestShadow_FullTranscriptContext verifies that each checkpoint includes
// only the prompts from its checkpoint portion, not the trace session.
//
// This tests checkpoint-scoped prompts:
// - First commit: prompt.txt includes prompts 1-2 (from checkpoint start)
// - Second commit: prompt.txt includes only prompt 3 (from second checkpoint start)
func TestShadow_FullTranscriptContext(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	// Setup repository
	env.InitRepo()
	env.WriteFile("README.md", "# Test Repository")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")
	env.GitCheckoutNewBranch("feature/incremental")
	env.InitTrace()

	t.Log("Phase 1: First session with two prompts")

	// Start first session
	session1 := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithPrompt(session1.ID, "Create function A in a.go"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	// First prompt: create file A
	fileAContent := "package main\n\nfunc A() {}\n"
	env.WriteFile("a.go", fileAContent)

	// Build transcript with first prompt
	session1.TranscriptBuilder.AddUserMessage("Create function A in a.go")
	session1.TranscriptBuilder.AddAssistantMessage("I'll create function A for you.")
	toolID1 := session1.TranscriptBuilder.AddToolUse("mcp__acp__Write", "a.go", fileAContent)
	session1.TranscriptBuilder.AddToolResult(toolID1)
	session1.TranscriptBuilder.AddAssistantMessage("Done creating function A!")

	// Second prompt in same session: create file B
	if err := env.SimulateUserPromptSubmitWithPrompt(session1.ID, "Now create function B in b.go"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt (second prompt) failed: %v", err)
	}
	fileBContent := "package main\n\nfunc B() {}\n"
	env.WriteFile("b.go", fileBContent)

	session1.TranscriptBuilder.AddUserMessage("Now create function B in b.go")
	session1.TranscriptBuilder.AddAssistantMessage("I'll create function B for you.")
	toolID2 := session1.TranscriptBuilder.AddToolUse("mcp__acp__Write", "b.go", fileBContent)
	session1.TranscriptBuilder.AddToolResult(toolID2)
	session1.TranscriptBuilder.AddAssistantMessage("Done creating function B!")

	// Write transcript
	if err := session1.TranscriptBuilder.WriteToFile(session1.TranscriptPath); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	// Save checkpoint (triggers SaveStep)
	if err := env.SimulateStop(session1.ID, session1.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	t.Log("Phase 2: First user commit")

	// User commits
	env.GitCommitWithShadowHooks("Add functions A and B", "a.go", "b.go")

	// Get first checkpoint ID from commit message trailer
	commit1Hash := env.GetHeadHash()
	checkpoint1ID := env.GetCheckpointIDFromCommitMessage(commit1Hash)
	t.Logf("First checkpoint ID: %s", checkpoint1ID)

	// Verify first checkpoint has both prompts (uses session file path in numbered subdirectory)
	promptPath1 := SessionFilePath(checkpoint1ID, "prompt.txt")
	prompt1Content, found := env.ReadFileFromBranch(paths.MetadataBranchName, promptPath1)
	if !found {
		t.Errorf("prompt.txt should exist at %s", promptPath1)
	} else {
		t.Logf("First prompt.txt content:\n%s", prompt1Content)
		// Should contain both "Create function A" and "create function B"
		if !strings.Contains(prompt1Content, "Create function A") {
			t.Error("First prompt.txt should contain 'Create function A'")
		}
		if !strings.Contains(prompt1Content, "create function B") {
			t.Error("First prompt.txt should contain 'create function B'")
		}
	}

	t.Log("Phase 3: Continue session with third prompt")

	// Continue the session with a new prompt
	// First, simulate another user prompt submit to track the new base
	if err := env.SimulateUserPromptSubmitWithPrompt(session1.ID, "Finally, create function C in c.go"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt (continued) failed: %v", err)
	}

	// Third prompt: create file C
	fileCContent := "package main\n\nfunc C() {}\n"
	env.WriteFile("c.go", fileCContent)

	// Add to transcript (continuing from previous)
	session1.TranscriptBuilder.AddUserMessage("Finally, create function C in c.go")
	session1.TranscriptBuilder.AddAssistantMessage("I'll create function C for you.")
	toolID3 := session1.TranscriptBuilder.AddToolUse("mcp__acp__Write", "c.go", fileCContent)
	session1.TranscriptBuilder.AddToolResult(toolID3)
	session1.TranscriptBuilder.AddAssistantMessage("Done creating function C!")

	// Write updated transcript
	if err := session1.TranscriptBuilder.WriteToFile(session1.TranscriptPath); err != nil {
		t.Fatalf("Failed to write updated transcript: %v", err)
	}

	// Save checkpoint
	if err := env.SimulateStop(session1.ID, session1.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (second) failed: %v", err)
	}

	t.Log("Phase 4: Second user commit")

	// User commits again
	env.GitCommitWithShadowHooks("Add function C", "c.go")

	// Get second checkpoint ID from commit message trailer
	commit2Hash := env.GetHeadHash()
	checkpoint2ID := env.GetCheckpointIDFromCommitMessage(commit2Hash)
	t.Logf("Second checkpoint ID: %s", checkpoint2ID)

	// Verify different checkpoint IDs
	if checkpoint1ID == checkpoint2ID {
		t.Errorf("Second commit should have different checkpoint ID: %s vs %s", checkpoint1ID, checkpoint2ID)
	}

	t.Log("Phase 5: Verify full transcript preserved in second checkpoint")

	// Verify second checkpoint has the FULL transcript (all three prompts)
	// Session files are now in numbered subdirectories (e.g., 0/prompt.txt)
	promptPath2 := SessionFilePath(checkpoint2ID, "prompt.txt")
	prompt2Content, found := env.ReadFileFromBranch(paths.MetadataBranchName, promptPath2)
	if !found {
		t.Errorf("prompt.txt should exist at %s", promptPath2)
	} else {
		t.Logf("Second prompt.txt content:\n%s", prompt2Content)

		// Should contain only the checkpoint-scoped prompt (third prompt only)
		if !strings.Contains(prompt2Content, "create function C") {
			t.Error("Second prompt.txt should contain 'create function C'")
		}
	}

	t.Log("Shadow full transcript context test completed successfully!")
}

// TestShadow_RewindAndCondensation verifies that after rewinding to an earlier
// checkpoint, the checkpoint only includes prompts up to that point.
//
// Workflow:
// 1. Create checkpoint 1 (prompt 1)
// 2. Create checkpoint 2 (prompt 2)
// 3. Rewind to checkpoint 1
// 4. User commits
// 5. Verify checkpoint only contains prompt 1 (NOT prompt 2)
func TestShadow_RewindAndCondensation(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	// Setup repository
	env.InitRepo()
	env.WriteFile("README.md", "# Test Repository")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")
	env.GitCheckoutNewBranch("feature/rewind-test")
	env.InitTrace()

	t.Log("Phase 1: Create first checkpoint with prompt 1")

	session := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Create function A in a.go"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	// First prompt: create file A
	fileAContent := "package main\n\nfunc A() {}\n"
	env.WriteFile("a.go", fileAContent)

	session.TranscriptBuilder.AddUserMessage("Create function A in a.go")
	session.TranscriptBuilder.AddAssistantMessage("I'll create function A for you.")
	toolID1 := session.TranscriptBuilder.AddToolUse("mcp__acp__Write", "a.go", fileAContent)
	session.TranscriptBuilder.AddToolResult(toolID1)
	session.TranscriptBuilder.AddAssistantMessage("Done creating function A!")

	if err := session.TranscriptBuilder.WriteToFile(session.TranscriptPath); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (checkpoint 1) failed: %v", err)
	}

	// Get checkpoint 1 for later
	rewindPoints := env.GetRewindPoints()
	if len(rewindPoints) != 1 {
		t.Fatalf("Expected 1 rewind point after checkpoint 1, got %d", len(rewindPoints))
	}
	checkpoint1 := rewindPoints[0]
	t.Logf("Checkpoint 1: %s - %s", checkpoint1.ID[:7], checkpoint1.Message)

	t.Log("Phase 2: Create second checkpoint with prompt 2")

	// Second prompt: modify file A (a different approach)
	fileAModified := "package main\n\nfunc A() {\n\t// Modified version\n}\n"
	env.WriteFile("a.go", fileAModified)

	session.TranscriptBuilder.AddUserMessage("Actually, modify function A to have a comment")
	session.TranscriptBuilder.AddAssistantMessage("I'll modify function A for you.")
	toolID2 := session.TranscriptBuilder.AddToolUse("mcp__acp__Write", "a.go", fileAModified)
	session.TranscriptBuilder.AddToolResult(toolID2)
	session.TranscriptBuilder.AddAssistantMessage("Done modifying function A!")

	if err := session.TranscriptBuilder.WriteToFile(session.TranscriptPath); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (checkpoint 2) failed: %v", err)
	}

	rewindPoints = env.GetRewindPoints()
	if len(rewindPoints) != 2 {
		t.Fatalf("Expected 2 rewind points after checkpoint 2, got %d", len(rewindPoints))
	}
	t.Logf("Checkpoint 2: %s - %s", rewindPoints[0].ID[:7], rewindPoints[0].Message)

	// Verify file has modified content
	currentContent := env.ReadFile("a.go")
	if currentContent != fileAModified {
		t.Errorf("a.go should have modified content before rewind")
	}

	t.Log("Phase 3: Rewind to checkpoint 1")

	// Rewind using the CLI (which calls the strategy internally)
	if err := env.Rewind(checkpoint1.ID); err != nil {
		t.Fatalf("Rewind failed: %v", err)
	}

	// Verify file content is restored to checkpoint 1
	restoredContent := env.ReadFile("a.go")
	if restoredContent != fileAContent {
		t.Errorf("a.go should have original content after rewind.\nExpected:\n%s\nGot:\n%s", fileAContent, restoredContent)
	}
	t.Log("Files successfully restored to checkpoint 1")

	t.Log("Phase 4: User commits after rewind")

	// User commits - this should trigger condensation
	env.GitCommitWithShadowHooks("Add function A (reverted)", "a.go")

	// Get checkpoint ID from commit message trailer
	commitHash := env.GetHeadHash()
	checkpointID := env.GetCheckpointIDFromCommitMessage(commitHash)
	t.Logf("Checkpoint ID: %s", checkpointID)

	t.Log("Phase 5: Verify checkpoint only contains prompt 1")

	// Check prompt.txt (uses session file path in numbered subdirectory)
	promptPath := SessionFilePath(checkpointID, "prompt.txt")
	promptContent, found := env.ReadFileFromBranch(paths.MetadataBranchName, promptPath)
	if !found {
		t.Errorf("prompt.txt should exist at %s", promptPath)
	} else {
		t.Logf("prompt.txt content:\n%s", promptContent)

		// Should contain prompt 1
		if !strings.Contains(promptContent, "Create function A") {
			t.Error("prompt.txt should contain 'Create function A' from checkpoint 1")
		}

		// Should NOT contain prompt 2 (because we rewound past it)
		if strings.Contains(promptContent, "modify function A") {
			t.Error("prompt.txt should NOT contain 'modify function A' - we rewound past that checkpoint")
		}
	}

	t.Log("Shadow rewind and condensation test completed successfully!")
}

// TestShadow_RewindPreservesUntrackedFilesFromSessionStart tests that files that existed
// in the working directory (but weren't tracked in git) before the session started are
// preserved when rewinding. This was a bug where such files were incorrectly deleted.
func TestShadow_RewindPreservesUntrackedFilesFromSessionStart(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	// Setup repository with initial commit
	env.InitRepo()
	env.WriteFile("README.md", "# Test Repository")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")
	env.GitCheckoutNewBranch("feature/untracked-test")

	// Create an untracked file BEFORE initializing Trace session
	// This simulates files like .claude/settings.json created by "trace setup"
	untrackedContent := `{"key": "value"}`
	env.WriteFile(".claude/settings.json", untrackedContent)

	// Initialize Trace with manual-commit strategy
	env.InitTrace()

	t.Log("Phase 1: Create first checkpoint")

	session := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Create function A"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	// First prompt: create file A
	fileAContent := "package main\n\nfunc A() {}\n"
	env.WriteFile("a.go", fileAContent)

	session.TranscriptBuilder.AddUserMessage("Create function A")
	session.TranscriptBuilder.AddAssistantMessage("Done!")
	toolID1 := session.TranscriptBuilder.AddToolUse("mcp__acp__Write", "a.go", fileAContent)
	session.TranscriptBuilder.AddToolResult(toolID1)

	if err := session.TranscriptBuilder.WriteToFile(session.TranscriptPath); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (checkpoint 1) failed: %v", err)
	}

	rewindPoints := env.GetRewindPoints()
	if len(rewindPoints) != 1 {
		t.Fatalf("Expected 1 rewind point, got %d", len(rewindPoints))
	}
	checkpoint1 := rewindPoints[0]
	t.Logf("Checkpoint 1: %s", checkpoint1.ID[:7])

	t.Log("Phase 2: Create second checkpoint")

	// Second prompt: create file B
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
		t.Fatalf("SimulateStop (checkpoint 2) failed: %v", err)
	}

	rewindPoints = env.GetRewindPoints()
	if len(rewindPoints) != 2 {
		t.Fatalf("Expected 2 rewind points, got %d", len(rewindPoints))
	}
	t.Logf("Checkpoint 2: %s", rewindPoints[0].ID[:7])

	// Verify the untracked file still exists before rewind
	if !env.FileExists(".claude/settings.json") {
		t.Fatal("Untracked file .claude/settings.json should exist before rewind")
	}

	t.Log("Phase 3: Rewind to checkpoint 1")

	if err := env.Rewind(checkpoint1.ID); err != nil {
		t.Fatalf("Rewind failed: %v", err)
	}

	// Verify that the untracked file that existed before session start is PRESERVED
	if !env.FileExists(".claude/settings.json") {
		t.Error("CRITICAL: .claude/settings.json was deleted during rewind but it existed before the session started!")
	} else {
		restoredContent := env.ReadFile(".claude/settings.json")
		if restoredContent != untrackedContent {
			t.Errorf("Untracked file content changed.\nExpected:\n%s\nGot:\n%s", untrackedContent, restoredContent)
		} else {
			t.Log("✓ Untracked file .claude/settings.json was preserved correctly")
		}
	}

	// Verify b.go was deleted (it was created after checkpoint 1)
	if env.FileExists("b.go") {
		t.Error("b.go should have been deleted during rewind (it was created after checkpoint 1)")
	} else {
		t.Log("✓ b.go was correctly deleted during rewind")
	}

	// Verify a.go was restored
	if !env.FileExists("a.go") {
		t.Error("a.go should exist after rewind to checkpoint 1")
	} else {
		restoredA := env.ReadFile("a.go")
		if restoredA != fileAContent {
			t.Errorf("a.go content incorrect after rewind")
		} else {
			t.Log("✓ a.go was correctly restored")
		}
	}

	t.Log("Test completed successfully!")
}

// TestShadow_IntermediateCommitsWithoutPrompts tests that commits without new Claude
// content do NOT get checkpoint trailers.
//
// Scenario:
// 1. Session starts, work happens, checkpoint created
// 2. First commit gets a trailer (has new content)
// 3. User commits unrelated files without new Claude work - NO trailer (no new content)
// 4. User enters new prompt, creates more files
// 5. Second commit with Claude content gets a trailer
func TestShadow_IntermediateCommitsWithoutPrompts(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	// Setup repository
	env.InitRepo()
	env.WriteFile("README.md", "# Test Repository")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")
	env.GitCheckoutNewBranch("feature/intermediate-commits")
	env.InitTrace()

	t.Log("Phase 1: Start session and create checkpoint")

	session := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Create function A in a.go"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	// First prompt: create file A
	fileAContent := "package main\n\nfunc A() {}\n"
	env.WriteFile("a.go", fileAContent)

	session.TranscriptBuilder.AddUserMessage("Create function A in a.go")
	session.TranscriptBuilder.AddAssistantMessage("I'll create function A for you.")
	toolID1 := session.TranscriptBuilder.AddToolUse("mcp__acp__Write", "a.go", fileAContent)
	session.TranscriptBuilder.AddToolResult(toolID1)
	session.TranscriptBuilder.AddAssistantMessage("Done creating function A!")

	if err := session.TranscriptBuilder.WriteToFile(session.TranscriptPath); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	t.Log("Phase 2: First commit (with session content)")

	env.GitCommitWithShadowHooks("Add function A", "a.go")
	commit1Hash := env.GetHeadHash()
	checkpoint1ID := env.GetCheckpointIDFromCommitMessage(commit1Hash)
	t.Logf("First commit: %s, checkpoint from trailer: %s", commit1Hash[:7], checkpoint1ID)
	t.Logf("First commit message:\n%s", env.GetCommitMessage(commit1Hash))

	if checkpoint1ID == "" {
		t.Fatal("First commit should have a checkpoint ID in its trailer (has new content)")
	}

	t.Log("Phase 3: Create unrelated file and commit WITHOUT new prompt")

	// User creates an unrelated file and commits without entering a new Claude prompt
	// Since there's no new session content, this commit should NOT get a trailer
	env.WriteFile("unrelated.txt", "This is an unrelated file")
	env.GitCommitWithShadowHooks("Add unrelated file", "unrelated.txt")

	commit2Hash := env.GetHeadHash()
	checkpoint2ID := env.GetCheckpointIDFromCommitMessage(commit2Hash)
	t.Logf("Second commit: %s, checkpoint from trailer: %s", commit2Hash[:7], checkpoint2ID)
	t.Logf("Second commit message:\n%s", env.GetCommitMessage(commit2Hash))

	// Second commit should NOT get a checkpoint ID (no new session content)
	if checkpoint2ID != "" {
		t.Errorf("Second commit should NOT have a checkpoint trailer (no new content), got: %s", checkpoint2ID)
	}

	t.Log("Phase 4: New Claude work and commit")

	// Now user enters new prompt and does more work
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Create function B in b.go"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	fileBContent := "package main\n\nfunc B() {}\n"
	env.WriteFile("b.go", fileBContent)

	session.TranscriptBuilder.AddUserMessage("Create function B in b.go")
	session.TranscriptBuilder.AddAssistantMessage("I'll create function B for you.")
	toolID2 := session.TranscriptBuilder.AddToolUse("mcp__acp__Write", "b.go", fileBContent)
	session.TranscriptBuilder.AddToolResult(toolID2)
	session.TranscriptBuilder.AddAssistantMessage("Done creating function B!")

	if err := session.TranscriptBuilder.WriteToFile(session.TranscriptPath); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	env.GitCommitWithShadowHooks("Add function B", "b.go")

	commit3Hash := env.GetHeadHash()
	checkpoint3ID := env.GetCheckpointIDFromCommitMessage(commit3Hash)
	t.Logf("Third commit: %s, checkpoint from trailer: %s", commit3Hash[:7], checkpoint3ID)
	t.Logf("Third commit message:\n%s", env.GetCommitMessage(commit3Hash))

	if checkpoint3ID == "" {
		t.Fatal("Third commit should have a checkpoint ID (has new content)")
	}

	// First and third checkpoint IDs should be different
	if checkpoint1ID == checkpoint3ID {
		t.Errorf("First and third commits should have different checkpoint IDs: %s vs %s",
			checkpoint1ID, checkpoint3ID)
	}

	t.Log("Phase 5: Verify checkpoints exist in trace/checkpoints/v1")

	for _, cpID := range []string{checkpoint1ID, checkpoint3ID} {
		shardedPath := ShardedCheckpointPath(cpID)
		metadataPath := shardedPath + "/metadata.json"
		if !env.FileExistsInBranch(paths.MetadataBranchName, metadataPath) {
			t.Errorf("Checkpoint %s should have metadata.json at %s", cpID, metadataPath)
		}
	}

	t.Log("Intermediate commits test completed successfully!")
}

// TestShadow_FullTranscriptCondensationWithIntermediateCommits tests that checkpoints
// contain only checkpoint-scoped prompts across multiple commits.
//
// Scenario:
// 1. Session with prompts A and B, commit 1 → prompt.txt has A and B
// 2. Continue session with prompt C, commit 2 (without intermediate prompt submit)
// 3. Verify commit 2's prompt.txt has only C (checkpoint-scoped)
func TestShadow_FullTranscriptCondensationWithIntermediateCommits(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	// Setup repository
	env.InitRepo()
	env.WriteFile("README.md", "# Test Repository")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")
	env.GitCheckoutNewBranch("feature/incremental-intermediate")
	env.InitTrace()

	t.Log("Phase 1: Session with two prompts")

	session := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Create function A"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	// First prompt
	fileAContent := "package main\n\nfunc A() {}\n"
	env.WriteFile("a.go", fileAContent)

	session.TranscriptBuilder.AddUserMessage("Create function A")
	session.TranscriptBuilder.AddAssistantMessage("Done!")
	toolID1 := session.TranscriptBuilder.AddToolUse("mcp__acp__Write", "a.go", fileAContent)
	session.TranscriptBuilder.AddToolResult(toolID1)

	// Second prompt in same session
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Create function B"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt (second prompt) failed: %v", err)
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

	t.Log("Phase 2: First commit")

	env.GitCommitWithShadowHooks("Add functions A and B", "a.go", "b.go")
	commit1Hash := env.GetHeadHash()
	checkpoint1ID := env.GetCheckpointIDFromCommitMessage(commit1Hash)
	t.Logf("First commit: %s, checkpoint: %s", commit1Hash[:7], checkpoint1ID)

	// Verify first checkpoint has prompts A and B (session files in numbered subdirectory)
	prompt1Content, found := env.ReadFileFromBranch(paths.MetadataBranchName, SessionFilePath(checkpoint1ID, "prompt.txt"))
	if !found {
		t.Fatal("First checkpoint should have prompt.txt")
	}
	if !strings.Contains(prompt1Content, "function A") || !strings.Contains(prompt1Content, "function B") {
		t.Errorf("First checkpoint should contain prompts A and B, got: %s", prompt1Content)
	}
	t.Logf("First checkpoint prompts:\n%s", prompt1Content)

	t.Log("Phase 3: Continue session with third prompt")

	// Submit the new prompt through the hook so it gets recorded in prompt.txt
	if err := env.SimulateUserPromptSubmitWithPrompt(session.ID, "Create function C"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt (third prompt) failed: %v", err)
	}

	fileCContent := "package main\n\nfunc C() {}\n"
	env.WriteFile("c.go", fileCContent)

	// Add to transcript
	session.TranscriptBuilder.AddUserMessage("Create function C")
	session.TranscriptBuilder.AddAssistantMessage("Done!")
	toolID3 := session.TranscriptBuilder.AddToolUse("mcp__acp__Write", "c.go", fileCContent)
	session.TranscriptBuilder.AddToolResult(toolID3)

	if err := session.TranscriptBuilder.WriteToFile(session.TranscriptPath); err != nil {
		t.Fatalf("Failed to write updated transcript: %v", err)
	}

	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop (second) failed: %v", err)
	}

	t.Log("Phase 4: Second commit")

	env.GitCommitWithShadowHooks("Add function C", "c.go")
	commit2Hash := env.GetHeadHash()
	checkpoint2ID := env.GetCheckpointIDFromCommitMessage(commit2Hash)
	t.Logf("Second commit: %s, checkpoint: %s", commit2Hash[:7], checkpoint2ID)

	if checkpoint1ID == checkpoint2ID {
		t.Errorf("Commits should have different checkpoint IDs")
	}

	t.Log("Phase 5: Verify second checkpoint has only checkpoint-scoped prompt (C)")

	// Session files are now in numbered subdirectory (e.g., 0/prompt.txt)
	prompt2Content, found := env.ReadFileFromBranch(paths.MetadataBranchName, SessionFilePath(checkpoint2ID, "prompt.txt"))
	if !found {
		t.Fatal("Second checkpoint should have prompt.txt")
	}

	t.Logf("Second checkpoint prompts:\n%s", prompt2Content)

	// Should contain only the checkpoint-scoped prompt (C), not earlier prompts
	if !strings.Contains(prompt2Content, "function C") {
		t.Error("Second checkpoint should contain 'function C'")
	}
	if strings.Contains(prompt2Content, "function A") {
		t.Error("Second checkpoint should NOT contain 'function A' (checkpoint-scoped)")
	}
	if strings.Contains(prompt2Content, "function B") {
		t.Error("Second checkpoint should NOT contain 'function B' (checkpoint-scoped)")
	}

	t.Log("Checkpoint-scoped prompt condensation with intermediate commits test completed successfully!")
}
