//go:build integration

package integration

import (
	"testing"
	"time"
)

// TestLastInteractionTime_SetOnFirstPrompt verifies that LastInteractionTime is set
// when a session is first initialized via UserPromptSubmit.
func TestLastInteractionTime_SetOnFirstPrompt(t *testing.T) {
	t.Parallel()
	env := NewRepoWithCommit(t)
	session := env.NewSession()

	beforePrompt := time.Now()
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit failed: %v", err)
	}

	state, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState failed: %v", err)
	}
	if state == nil {
		t.Fatal("session state should exist after UserPromptSubmit")
	}

	if state.LastInteractionTime == nil {
		t.Fatal("LastInteractionTime should be set after first prompt")
	}
	if state.LastInteractionTime.Before(beforePrompt) {
		t.Errorf("LastInteractionTime %v should be after test start %v",
			*state.LastInteractionTime, beforePrompt)
	}
}

// TestLastInteractionTime_UpdatedOnSubsequentPrompts verifies that LastInteractionTime
// is updated on each subsequent UserPromptSubmit call.
func TestLastInteractionTime_UpdatedOnSubsequentPrompts(t *testing.T) {
	t.Parallel()
	env := NewRepoWithCommit(t)
	session := env.NewSession()

	// First prompt
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("first SimulateUserPromptSubmit failed: %v", err)
	}

	state1, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState after first prompt failed: %v", err)
	}
	if state1.LastInteractionTime == nil {
		t.Fatal("LastInteractionTime should be set after first prompt")
	}
	firstInteraction := *state1.LastInteractionTime

	// Small delay to ensure timestamps differ
	time.Sleep(10 * time.Millisecond)

	// Second prompt
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("second SimulateUserPromptSubmit failed: %v", err)
	}

	state2, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState after second prompt failed: %v", err)
	}
	if state2.LastInteractionTime == nil {
		t.Fatal("LastInteractionTime should be set after second prompt")
	}

	if !state2.LastInteractionTime.After(firstInteraction) {
		t.Errorf("LastInteractionTime should be updated: first=%v, second=%v",
			firstInteraction, *state2.LastInteractionTime)
	}
}

// TestLastInteractionTime_PreservedAcrossCheckpoints verifies that LastInteractionTime
// survives a full checkpoint cycle (prompt → stop → prompt).
func TestLastInteractionTime_PreservedAcrossCheckpoints(t *testing.T) {
	t.Parallel()
	env := NewRepoWithCommit(t)
	session := env.NewSession()

	// First prompt + checkpoint
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit failed: %v", err)
	}

	env.WriteFile("file1.txt", "content1")
	session.CreateTranscript("Create file1", []FileChange{
		{Path: "file1.txt", Content: "content1"},
	})
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	// Second prompt
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("second SimulateUserPromptSubmit failed: %v", err)
	}

	state, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState failed: %v", err)
	}
	if state.LastInteractionTime == nil {
		t.Fatal("LastInteractionTime should be set after second prompt")
	}

	// LastInteractionTime should be after StartedAt (second prompt is later)
	if !state.LastInteractionTime.After(state.StartedAt) {
		t.Errorf("LastInteractionTime %v should be after StartedAt %v",
			*state.LastInteractionTime, state.StartedAt)
	}
}
