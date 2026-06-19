//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
)

// TestShadow_SessionDepleted_ManualEditNoCheckpoint tests that once all session
// files are committed, subsequent manual edits (even to previously committed files)
// do NOT get checkpoint trailers.
//
// Flow:
// 1. Agent creates files A, B, C, then stops (IDLE)
// 2. User commits files A and B → checkpoint #1
// 3. User commits file C → checkpoint #2 (carry-forward if implemented, or just C)
// 4. Session is now "depleted" (all FilesTouched committed)
// 5. User manually edits file A and commits → NO checkpoint (session exhausted)
func TestShadow_SessionDepleted_ManualEditNoCheckpoint(t *testing.T) {
	t.Parallel()

	env := NewFeatureBranchEnv(t)

	sess := env.NewSession()

	// Start session
	if err := env.SimulateUserPromptSubmitWithPrompt(sess.ID, "Create files A, B, and C"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	// Create 3 files through session
	env.WriteFile("fileA.go", "package main\n\nfunc A() {}\n")
	env.WriteFile("fileB.go", "package main\n\nfunc B() {}\n")
	env.WriteFile("fileC.go", "package main\n\nfunc C() {}\n")
	sess.CreateTranscript("Create files A, B, and C", []FileChange{
		{Path: "fileA.go", Content: "package main\n\nfunc A() {}\n"},
		{Path: "fileB.go", Content: "package main\n\nfunc B() {}\n"},
		{Path: "fileC.go", Content: "package main\n\nfunc C() {}\n"},
	})

	// Stop session (becomes IDLE)
	if err := env.SimulateStop(sess.ID, sess.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	// First commit: files A and B
	env.GitCommitWithShadowHooks("Add files A and B", "fileA.go", "fileB.go")
	firstCommitHash := env.GetHeadHash()
	firstCheckpointID := env.GetCheckpointIDFromCommitMessage(firstCommitHash)
	if firstCheckpointID == "" {
		t.Fatal("First commit should have checkpoint trailer (files overlap with session)")
	}
	t.Logf("First checkpoint ID: %s", firstCheckpointID)

	// Second commit: file C
	env.GitCommitWithShadowHooks("Add file C", "fileC.go")
	secondCommitHash := env.GetHeadHash()
	secondCheckpointID := env.GetCheckpointIDFromCommitMessage(secondCommitHash)
	// Note: Whether this gets a checkpoint depends on carry-forward implementation
	// for IDLE sessions. Log either way.
	if secondCheckpointID != "" {
		t.Logf("Second checkpoint ID: %s (carry-forward active for IDLE)", secondCheckpointID)
	} else {
		t.Log("Second commit has no checkpoint (IDLE sessions don't carry forward)")
	}

	// Verify session state - FilesTouched should be empty or session ended
	state, err := env.GetSessionState(sess.ID)
	if err != nil {
		// Session may have been cleaned up, which is fine
		t.Logf("Session state not found (may have been cleaned up): %v", err)
	} else {
		t.Logf("Session state after all commits: Phase=%s, FilesTouched=%v",
			state.Phase, state.FilesTouched)
	}

	// Now manually edit file A (which was already committed as part of session)
	env.WriteFile("fileA.go", "package main\n\n// Manual edit by user\nfunc A() { return }\n")

	// Commit the manual edit - should NOT get checkpoint
	env.GitCommitWithShadowHooks("Manual edit to file A", "fileA.go")
	thirdCommitHash := env.GetHeadHash()
	thirdCheckpointID := env.GetCheckpointIDFromCommitMessage(thirdCommitHash)

	if thirdCheckpointID != "" {
		t.Errorf("Third commit should NOT have checkpoint trailer "+
			"(manual edit after session depleted), got %s", thirdCheckpointID)
	} else {
		t.Log("Third commit correctly has no checkpoint trailer (session depleted)")
	}

	t.Log("SessionDepleted_ManualEditNoCheckpoint test completed successfully")
}

// TestShadow_RevertedFiles_ManualEditNoCheckpoint tests that after reverting
// uncommitted session files, manual edits with completely different content
// do NOT get checkpoint trailers.
//
// The overlap check is content-aware: it compares file hashes between the
// committed content and the shadow branch content. If they don't match,
// the file is not considered session-related.
//
// Flow:
// 1. Agent creates files A, B, C, then stops (IDLE)
// 2. User commits files A and B → checkpoint #1
// 3. User reverts file C (deletes it)
// 4. User manually creates file C with different content
// 5. User commits file C → NO checkpoint (content doesn't match shadow branch)
func TestShadow_RevertedFiles_ManualEditNoCheckpoint(t *testing.T) {
	t.Parallel()

	env := NewFeatureBranchEnv(t)

	sess := env.NewSession()

	// Start session
	if err := env.SimulateUserPromptSubmitWithPrompt(sess.ID, "Create files A, B, and C"); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithPrompt failed: %v", err)
	}

	// Create 3 files through session
	env.WriteFile("fileA.go", "package main\n\nfunc A() {}\n")
	env.WriteFile("fileB.go", "package main\n\nfunc B() {}\n")
	env.WriteFile("fileC.go", "package main\n\nfunc C() {}\n")
	sess.CreateTranscript("Create files A, B, and C", []FileChange{
		{Path: "fileA.go", Content: "package main\n\nfunc A() {}\n"},
		{Path: "fileB.go", Content: "package main\n\nfunc B() {}\n"},
		{Path: "fileC.go", Content: "package main\n\nfunc C() {}\n"},
	})

	// Stop session (becomes IDLE)
	if err := env.SimulateStop(sess.ID, sess.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	// First commit: files A and B
	env.GitCommitWithShadowHooks("Add files A and B", "fileA.go", "fileB.go")
	firstCommitHash := env.GetHeadHash()
	firstCheckpointID := env.GetCheckpointIDFromCommitMessage(firstCommitHash)
	if firstCheckpointID == "" {
		t.Fatal("First commit should have checkpoint trailer (files overlap with session)")
	}
	t.Logf("First checkpoint ID: %s", firstCheckpointID)

	// Revert file C (undo agent's changes)
	// Since fileC.go is a new file (untracked), we need to delete it
	if err := os.Remove(filepath.Join(env.RepoDir, "fileC.go")); err != nil {
		t.Fatalf("Failed to remove fileC.go: %v", err)
	}
	t.Log("Reverted fileC.go by removing it")

	// Verify file C is gone
	if _, err := os.Stat(filepath.Join(env.RepoDir, "fileC.go")); !os.IsNotExist(err) {
		t.Fatal("fileC.go should not exist after revert")
	}

	// User manually creates file C with DIFFERENT content (not what agent wrote)
	env.WriteFile("fileC.go", "package main\n\n// Completely different implementation\nfunc C() { panic(\"manual\") }\n")

	// Commit the manual file C - should NOT get checkpoint because content-aware
	// overlap check compares file hashes. The content is completely different
	// from what the session wrote, so it's not linked.
	env.GitCommitWithShadowHooks("Add file C (manual implementation)", "fileC.go")
	secondCommitHash := env.GetHeadHash()
	secondCheckpointID := env.GetCheckpointIDFromCommitMessage(secondCommitHash)

	if secondCheckpointID != "" {
		t.Errorf("Second commit should NOT have checkpoint trailer "+
			"(content doesn't match shadow branch), got %s", secondCheckpointID)
	} else {
		t.Log("Second commit correctly has no checkpoint trailer (content mismatch)")
	}

	t.Log("RevertedFiles_ManualEditNoCheckpoint test completed successfully")
}

// TestShadow_ResetSession_ClearsTurnCheckpointIDs tests that resetting a session
// properly clears TurnCheckpointIDs and doesn't leave orphaned checkpoints.
//
// Flow:
// 1. Agent starts working (ACTIVE)
// 2. User commits mid-turn → TurnCheckpointIDs populated
// 3. User calls "trace reset --session <id> --force"
// 4. Session state file should be deleted
// 5. A new session can start cleanly without orphaned state
func TestShadow_ResetSession_ClearsTurnCheckpointIDs(t *testing.T) {
	t.Parallel()

	env := NewFeatureBranchEnv(t)

	sess := env.NewSession()

	// Start session (ACTIVE)
	if err := env.SimulateUserPromptSubmitWithPromptAndTranscriptPath(sess.ID, "Create feature function", sess.TranscriptPath); err != nil {
		t.Fatalf("user-prompt-submit failed: %v", err)
	}

	// Create file and transcript
	env.WriteFile("feature.go", "package main\n\nfunc Feature() {}\n")
	sess.CreateTranscript("Create feature function", []FileChange{
		{Path: "feature.go", Content: "package main\n\nfunc Feature() {}\n"},
	})

	// User commits while agent is still ACTIVE → TurnCheckpointIDs gets populated
	env.GitCommitWithShadowHooks("Add feature", "feature.go")
	commitHash := env.GetHeadHash()
	checkpointID := env.GetCheckpointIDFromCommitMessage(commitHash)
	if checkpointID == "" {
		t.Fatal("Commit should have checkpoint trailer")
	}

	// Verify TurnCheckpointIDs is populated
	state, err := env.GetSessionState(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionState failed: %v", err)
	}
	if len(state.TurnCheckpointIDs) == 0 {
		t.Error("TurnCheckpointIDs should be populated after mid-turn commit")
	}
	t.Logf("TurnCheckpointIDs before reset: %v", state.TurnCheckpointIDs)

	// Reset the session using the CLI
	output, resetErr := env.RunCLIWithError("reset", "--session", sess.ID, "--force")
	t.Logf("Reset output: %s", output)
	if resetErr != nil {
		t.Fatalf("Reset failed: %v", resetErr)
	}

	// Verify session state is cleared (file deleted)
	state, err = env.GetSessionState(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionState after reset failed unexpectedly: %v", err)
	}
	if state != nil {
		t.Errorf("Session state should be nil after reset, got: phase=%s, TurnCheckpointIDs=%v",
			state.Phase, state.TurnCheckpointIDs)
	}

	// Verify a new session can start cleanly
	newSess := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithTranscriptPath(newSess.ID, newSess.TranscriptPath); err != nil {
		t.Fatalf("user-prompt-submit for new session failed: %v", err)
	}

	newState, err := env.GetSessionState(newSess.ID)
	if err != nil {
		t.Fatalf("GetSessionState for new session failed: %v", err)
	}
	if newState == nil {
		t.Fatal("New session state should exist")
	}
	if len(newState.TurnCheckpointIDs) != 0 {
		t.Errorf("New session should have empty TurnCheckpointIDs, got: %v", newState.TurnCheckpointIDs)
	}

	t.Log("ResetSession_ClearsTurnCheckpointIDs test completed successfully")
}

// TestShadow_EndedSession_UserCommitsRemainingFiles tests that after a session ends
// (IDLE → ENDED via session-end hook), user commits still get checkpoint trailers
// and condensation happens correctly.
//
// This exercises the ENDED + GitCommit → ActionCondenseIfFilesTouched code path,
// which is distinct from IDLE + GitCommit → ActionCondense.
//
// Flow:
// 1. Agent creates files A and B, then stops (IDLE)
// 2. Session ends (ENDED via SimulateSessionEnd)
// 3. User commits file A → checkpoint #1
// 4. User commits file B → checkpoint #2
// 5. Both checkpoints exist, unique IDs, no shadow branches remain
func TestShadow_EndedSession_UserCommitsRemainingFiles(t *testing.T) {
	t.Parallel()

	env := NewFeatureBranchEnv(t)

	sess := env.NewSession()

	// Start session (ACTIVE)
	if err := env.SimulateUserPromptSubmitWithPromptAndTranscriptPath(sess.ID, "Create files A and B", sess.TranscriptPath); err != nil {
		t.Fatalf("user-prompt-submit failed: %v", err)
	}

	// Create files
	env.WriteFile("fileA.go", "package main\n\nfunc A() {}\n")
	env.WriteFile("fileB.go", "package main\n\nfunc B() {}\n")

	sess.CreateTranscript("Create files A and B", []FileChange{
		{Path: "fileA.go", Content: "package main\n\nfunc A() {}\n"},
		{Path: "fileB.go", Content: "package main\n\nfunc B() {}\n"},
	})

	// Stop session (IDLE)
	if err := env.SimulateStop(sess.ID, sess.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	state, err := env.GetSessionState(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionState failed: %v", err)
	}
	if state.Phase != session.PhaseIdle {
		t.Errorf("Expected IDLE phase, got %s", state.Phase)
	}

	// End session (ENDED) — exercises the distinct ENDED code path
	if err := env.SimulateSessionEnd(sess.ID); err != nil {
		t.Fatalf("SimulateSessionEnd failed: %v", err)
	}

	state, err = env.GetSessionState(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionState failed: %v", err)
	}
	if state.Phase != session.PhaseEnded {
		t.Errorf("Expected ENDED phase, got %s", state.Phase)
	}
	if state.EndedAt == nil {
		t.Error("EndedAt should be set after session-end")
	}
	t.Logf("Session phase: %s, EndedAt: %v, FilesTouched: %v",
		state.Phase, state.EndedAt, state.FilesTouched)

	// User commits file A → checkpoint #1
	env.GitCommitWithShadowHooks("Add file A", "fileA.go")
	firstCommitHash := env.GetHeadHash()
	firstCheckpointID := env.GetCheckpointIDFromCommitMessage(firstCommitHash)
	if firstCheckpointID == "" {
		t.Fatal("First commit should have checkpoint trailer (ENDED session, files overlap)")
	}
	t.Logf("First checkpoint ID: %s", firstCheckpointID)

	// Verify phase stays ENDED
	state, err = env.GetSessionState(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionState failed: %v", err)
	}
	if state.Phase != session.PhaseEnded {
		t.Errorf("Expected phase to stay ENDED after commit, got %s", state.Phase)
	}

	// Validate first checkpoint
	env.ValidateCheckpoint(CheckpointValidation{
		CheckpointID:    firstCheckpointID,
		SessionID:       sess.ID,
		FilesTouched:    []string{"fileA.go"},
		ExpectedPrompts: []string{"Create files A and B"},
	})

	// User commits file B → checkpoint #2
	env.GitCommitWithShadowHooks("Add file B", "fileB.go")
	secondCommitHash := env.GetHeadHash()
	secondCheckpointID := env.GetCheckpointIDFromCommitMessage(secondCommitHash)
	if secondCheckpointID == "" {
		t.Fatal("Second commit should have checkpoint trailer (carry-forward in ENDED)")
	}
	t.Logf("Second checkpoint ID: %s", secondCheckpointID)

	// Checkpoint IDs must be unique
	if firstCheckpointID == secondCheckpointID {
		t.Errorf("Each commit should get a unique checkpoint ID.\nFirst: %s\nSecond: %s",
			firstCheckpointID, secondCheckpointID)
	}

	// Validate second checkpoint
	env.ValidateCheckpoint(CheckpointValidation{
		CheckpointID:    secondCheckpointID,
		SessionID:       sess.ID,
		FilesTouched:    []string{"fileB.go"},
		ExpectedPrompts: []string{"Create files A and B"},
	})

	// No shadow branches should remain
	branchesAfter := env.ListBranchesWithPrefix("trace/")
	for _, b := range branchesAfter {
		if b != paths.MetadataBranchName && b != paths.TrailsBranchName {
			t.Errorf("Unexpected shadow branch after all files committed: %s", b)
		}
	}

	t.Log("EndedSession_UserCommitsRemainingFiles test completed successfully")
}

// TestShadow_DeletedFiles_CheckpointAndCarryForward tests that deleted files
// in a session are properly handled: they get checkpoint trailers when committed
// via git rm, and carry-forward works for remaining files.
//
// Flow:
// 1. Pre-commit 3 files: old_a.go, old_b.go, old_c.go
// 2. Session: agent creates new_file.go AND deletes old_a.go
// 3. SimulateStop → IDLE
// 4. User commits new_file.go → checkpoint #1
// 5. User does git rm old_a.go + commit → checkpoint #2
// 6. Both checkpoints validated, no shadow branches remain
func TestShadow_DeletedFiles_CheckpointAndCarryForward(t *testing.T) {
	t.Parallel()

	env := NewFeatureBranchEnv(t)

	// Pre-commit existing files
	env.WriteFile("old_a.go", "package main\n\nfunc OldA() {}\n")
	env.WriteFile("old_b.go", "package main\n\nfunc OldB() {}\n")
	env.WriteFile("old_c.go", "package main\n\nfunc OldC() {}\n")
	env.GitAdd("old_a.go")
	env.GitAdd("old_b.go")
	env.GitAdd("old_c.go")
	env.GitCommit("Add old files")

	sess := env.NewSession()

	// Start session
	if err := env.SimulateUserPromptSubmitWithPromptAndTranscriptPath(sess.ID, "Create new_file.go and delete old_a.go", sess.TranscriptPath); err != nil {
		t.Fatalf("user-prompt-submit failed: %v", err)
	}

	// Agent creates new_file.go and deletes old_a.go
	env.WriteFile("new_file.go", "package main\n\nfunc NewFunc() {}\n")
	if err := os.Remove(filepath.Join(env.RepoDir, "old_a.go")); err != nil {
		t.Fatalf("Failed to delete old_a.go: %v", err)
	}

	sess.CreateTranscript("Create new_file.go and delete old_a.go", []FileChange{
		{Path: "new_file.go", Content: "package main\n\nfunc NewFunc() {}\n"},
		{Path: "old_a.go", Content: ""}, // deletion
	})

	// Stop session (IDLE)
	if err := env.SimulateStop(sess.ID, sess.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	// User commits new_file.go → checkpoint #1
	env.GitCommitWithShadowHooks("Add new file", "new_file.go")
	firstCommitHash := env.GetHeadHash()
	firstCheckpointID := env.GetCheckpointIDFromCommitMessage(firstCommitHash)
	if firstCheckpointID == "" {
		t.Fatal("First commit should have checkpoint trailer")
	}
	t.Logf("First checkpoint ID: %s", firstCheckpointID)

	// User does git rm old_a.go and commits the deletion
	env.GitRm("old_a.go")
	env.GitCommitStagedWithShadowHooks("Remove old_a.go")
	secondCommitHash := env.GetHeadHash()
	secondCheckpointID := env.GetCheckpointIDFromCommitMessage(secondCommitHash)
	// Deleted files may get a trailer via carry-forward, but condensation may not
	// produce full metadata since the file doesn't exist in the working tree.
	// Just verify uniqueness if a trailer was added.
	if secondCheckpointID != "" {
		t.Logf("Second checkpoint ID: %s (carry-forward for deleted file)", secondCheckpointID)
		if firstCheckpointID == secondCheckpointID {
			t.Error("Checkpoint IDs should be unique")
		}
	} else {
		t.Log("Second commit has no checkpoint trailer (deleted files may not carry forward)")
	}

	// Validate first checkpoint
	env.ValidateCheckpoint(CheckpointValidation{
		CheckpointID:    firstCheckpointID,
		SessionID:       sess.ID,
		ExpectedPrompts: []string{"Create new_file.go and delete old_a.go"},
	})

	// Check for remaining shadow branches.
	// Note: deleted file carry-forward may leave shadow branches if condensation
	// doesn't produce full metadata (known limitation).
	branchesAfter := env.ListBranchesWithPrefix("trace/")
	for _, b := range branchesAfter {
		if b != paths.MetadataBranchName && b != paths.TrailsBranchName {
			t.Logf("Shadow branch remaining after commits (may be expected for deleted files): %s", b)
		}
	}

	t.Log("DeletedFiles_CheckpointAndCarryForward test completed successfully")
}

// TestShadow_CarryForward_ModifiedExistingFiles tests that modified (not new) files
// in carry-forward get checkpoint trailers correctly. Modified files always trigger
// overlap because the user is editing a file the session worked on.
//
// Flow:
// 1. Pre-commit 3 files: model.go, view.go, controller.go
// 2. Session: agent modifies all three
// 3. SimulateStop → IDLE
// 4. User commits model.go → checkpoint #1
// 5. User commits view.go → checkpoint #2
// 6. User commits controller.go → checkpoint #3
// 7. All IDs unique, all validated, no shadow branches
func TestShadow_CarryForward_ModifiedExistingFiles(t *testing.T) {
	t.Parallel()

	env := NewFeatureBranchEnv(t)

	// Pre-commit existing files
	env.WriteFile("model.go", "package main\n\nfunc Model() {}\n")
	env.WriteFile("view.go", "package main\n\nfunc View() {}\n")
	env.WriteFile("controller.go", "package main\n\nfunc Controller() {}\n")
	env.GitAdd("model.go")
	env.GitAdd("view.go")
	env.GitAdd("controller.go")
	env.GitCommit("Add MVC files")

	sess := env.NewSession()

	// Start session
	if err := env.SimulateUserPromptSubmitWithPromptAndTranscriptPath(sess.ID, "Update MVC files", sess.TranscriptPath); err != nil {
		t.Fatalf("user-prompt-submit failed: %v", err)
	}

	// Agent modifies all three files
	env.WriteFile("model.go", "package main\n\n// Updated by agent\nfunc Model() { return }\n")
	env.WriteFile("view.go", "package main\n\n// Updated by agent\nfunc View() { return }\n")
	env.WriteFile("controller.go", "package main\n\n// Updated by agent\nfunc Controller() { return }\n")

	sess.CreateTranscript("Update MVC files", []FileChange{
		{Path: "model.go", Content: "package main\n\n// Updated by agent\nfunc Model() { return }\n"},
		{Path: "view.go", Content: "package main\n\n// Updated by agent\nfunc View() { return }\n"},
		{Path: "controller.go", Content: "package main\n\n// Updated by agent\nfunc Controller() { return }\n"},
	})

	// Stop session (IDLE)
	if err := env.SimulateStop(sess.ID, sess.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	// Commit each file separately
	checkpointIDs := make([]string, 3)
	files := []string{"model.go", "view.go", "controller.go"}

	for i, file := range files {
		env.GitCommitWithShadowHooks("Update "+file, file)
		commitHash := env.GetHeadHash()
		cpID := env.GetCheckpointIDFromCommitMessage(commitHash)
		if cpID == "" {
			t.Fatalf("Commit %d (%s) should have checkpoint trailer", i+1, file)
		}
		checkpointIDs[i] = cpID
		t.Logf("Checkpoint %d (%s): %s", i+1, file, cpID)
	}

	// All checkpoint IDs must be unique
	seen := make(map[string]bool)
	for i, cpID := range checkpointIDs {
		if seen[cpID] {
			t.Errorf("Duplicate checkpoint ID at position %d: %s", i, cpID)
		}
		seen[cpID] = true
	}

	// Validate all checkpoints
	for i, cpID := range checkpointIDs {
		env.ValidateCheckpoint(CheckpointValidation{
			CheckpointID:    cpID,
			SessionID:       sess.ID,
			FilesTouched:    []string{files[i]},
			ExpectedPrompts: []string{"Update MVC files"},
		})
	}

	// No shadow branches should remain
	branchesAfter := env.ListBranchesWithPrefix("trace/")
	for _, b := range branchesAfter {
		if b != paths.MetadataBranchName && b != paths.TrailsBranchName {
			t.Errorf("Unexpected shadow branch after all files committed: %s", b)
		}
	}

	t.Log("CarryForward_ModifiedExistingFiles test completed successfully")
}
