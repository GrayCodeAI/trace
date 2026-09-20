package main

import (
	"encoding/json"
	"errors"
	"strconv"
)

// Cursor's stop hook has a stable conversation ID and a per-turn generation ID.
// The hook does not include the final response, so this adapter stores only
// completion metadata and the Git HEAD observed when the hook runs.
type cursorStopNotification struct {
	HookEventName  string   `json:"hook_event_name"`
	ConversationID string   `json:"conversation_id"`
	GenerationID   string   `json:"generation_id"`
	WorkspaceRoots []string `json:"workspace_roots"`
	Status         string   `json:"status"`
	LoopCount      int      `json:"loop_count"`
}

func cursorStopCaptureEvent(input []byte, cfg agentCaptureConfig) (queuedAgentEvent, bool, error) {
	var notice cursorStopNotification
	if err := json.Unmarshal(input, &notice); err != nil {
		return queuedAgentEvent{}, false, errors.New("invalid Cursor hook JSON")
	}
	if notice.HookEventName != "stop" || notice.Status != "completed" {
		return queuedAgentEvent{}, false, nil
	}
	if notice.ConversationID == "" || notice.GenerationID == "" || len(notice.WorkspaceRoots) == 0 || notice.LoopCount < 0 {
		return queuedAgentEvent{}, false, errors.New("Cursor stop hook is missing conversation_id, generation_id, or workspace_roots")
	}
	matched := false
	for _, work := range notice.WorkspaceRoots {
		root, err := gitWorktreeRoot(work)
		if err == nil && root == cfg.Worktree {
			matched = true
			break
		}
	}
	if !matched {
		return queuedAgentEvent{}, false, nil
	}
	commit, err := gitCaptureOutput(cfg.Worktree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || len(commit) != 40 {
		return queuedAgentEvent{}, false, errors.New("cannot resolve local HEAD")
	}
	eventID := captureID("cursor-stop", notice.ConversationID+"\x00"+notice.GenerationID+"\x00"+strconv.Itoa(notice.LoopCount)+"\x00"+commit)
	return queuedAgentEvent{
		Agent:      "Cursor",
		SessionKey: captureID("cursor", notice.ConversationID),
		EventID:    eventID,
		Ref:        commit,
		Summary:    "Cursor agent turn completed; HEAD observed at stop hook time",
	}, true, nil
}
