package strategy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/internal/flock"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/jsonutil"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/session"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/validation"
)

// Session state management functions shared across all strategies.
// SessionState is stored in .git/trace-sessions/{session_id}.json

// getSessionStateDir returns the path to the session state directory.
// This is stored in the git common dir so it's shared across all worktrees.
func getSessionStateDir(ctx context.Context) (string, error) {
	commonDir, err := GetGitCommonDir(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Join(commonDir, session.SessionStateDirName), nil
}

// sessionStateFile returns the path to a session state file.
func sessionStateFile(ctx context.Context, sessionID string) (string, error) {
	stateDir, err := getSessionStateDir(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, sessionID+".json"), nil
}

// LoadSessionState loads the session state for the given session ID.
// Returns (nil, nil) when session file doesn't exist or session is stale (not an error condition).
// Stale sessions are automatically deleted by the underlying StateStore.
func LoadSessionState(ctx context.Context, sessionID string) (*SessionState, error) {
	store, err := session.NewStateStore(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create state store: %w", err)
	}

	state, err := store.Load(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load session state: %w", err)
	}
	return state, nil
}

// SaveSessionState saves the session state atomically.
func SaveSessionState(ctx context.Context, state *SessionState) error {
	// Validate session ID to prevent path traversal
	if err := validation.ValidateSessionID(state.SessionID); err != nil {
		return fmt.Errorf("invalid session ID: %w", err)
	}

	stateDir, err := getSessionStateDir(ctx)
	if err != nil {
		return fmt.Errorf("failed to get session state directory: %w", err)
	}

	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return fmt.Errorf("failed to create session state directory: %w", err)
	}

	data, err := jsonutil.MarshalIndentWithNewline(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session state: %w", err)
	}

	stateFile, err := sessionStateFile(ctx, state.SessionID)
	if err != nil {
		return fmt.Errorf("failed to get session state file path: %w", err)
	}

	// Atomic write: write to temp file, then rename
	tmpFile := stateFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write session state: %w", err)
	}
	if err := os.Rename(tmpFile, stateFile); err != nil {
		return fmt.Errorf("failed to rename session state file: %w", err)
	}
	return nil
}

// ListSessionStates returns all session states from the state directory.
// This is a package-level function that doesn't require a specific strategy instance.
func ListSessionStates(ctx context.Context) ([]*SessionState, error) {
	store, err := session.NewStateStore(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create state store: %w", err)
	}

	states, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list session states: %w", err)
	}
	return states, nil
}

// FindMostRecentSession returns the session ID of the most recently interacted session
// (by LastInteractionTime) in the current worktree. Returns empty string if no sessions exist.
// Scoping to the current worktree prevents cross-worktree pollution in log routing.
// Falls back to unfiltered search if the worktree path can't be determined.
func FindMostRecentSession(ctx context.Context) string {
	states, err := ListSessionStates(ctx)
	if err != nil || len(states) == 0 {
		return ""
	}

	// Scope to current worktree to prevent cross-worktree pollution.
	worktreePath, wpErr := paths.WorktreeRoot(ctx)
	if wpErr == nil && worktreePath != "" {
		var filtered []*SessionState
		for _, s := range states {
			if s.WorktreePath == worktreePath {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) > 0 {
			states = filtered
		}
		// If no sessions match the worktree, fall back to all sessions
	}

	var best *SessionState
	for _, s := range states {
		if s.LastInteractionTime == nil {
			continue
		}
		if best == nil || s.LastInteractionTime.After(*best.LastInteractionTime) {
			best = s
		}
	}
	if best != nil {
		return best.SessionID
	}

	// Fallback: return most recently started session
	for _, s := range states {
		if best == nil || s.StartedAt.After(best.StartedAt) {
			best = s
		}
	}
	if best != nil {
		return best.SessionID
	}
	return ""
}

// TransitionAndLog runs a session phase transition, applies actions via the
// handler, and logs the transition. Returns the first handler error from
// ApplyTransition (if any) so callers can surface it. The error is also
// logged internally for diagnostics.
// This is the single entry point for all state machine transitions to ensure
// consistent logging of phase changes.
func TransitionAndLog(goCtx context.Context, state *SessionState, event session.Event, ctx session.TransitionContext, handler session.ActionHandler) error {
	oldPhase := state.Phase
	result := session.Transition(oldPhase, event, ctx)
	logCtx := logging.WithComponent(goCtx, "session")

	handlerErr := session.ApplyTransition(goCtx, state, result, handler)
	if handlerErr != nil {
		logging.Error(
			logCtx, "action handler error during transition",
			slog.String("session_id", state.SessionID),
			slog.String("event", event.String()),
			slog.Any("error", handlerErr),
		)
	}

	if result.NewPhase != oldPhase {
		logging.Info(
			logCtx, "phase transition",
			slog.String("session_id", state.SessionID),
			slog.String("event", event.String()),
			slog.String("from", string(oldPhase)),
			slog.String("to", string(result.NewPhase)),
		)
	} else {
		logging.Debug(
			logCtx, "phase unchanged",
			slog.String("session_id", state.SessionID),
			slog.String("event", event.String()),
			slog.String("phase", string(result.NewPhase)),
			slog.Any("result", result),
		)
	}

	if handlerErr != nil {
		return fmt.Errorf("transition %s: %w", event, handlerErr)
	}
	return nil
}

// StoreModelHint writes the LLM model name to a lightweight hint file
// (.git/trace-sessions/{session_id}.model) for cross-process persistence.
//
// Why a separate file instead of SessionState?
//
// SessionState requires BaseCommit (used for shadow branch naming, checkpoint
// writing, doctor classification, etc.) and is only created during TurnStart
// when the git repo is fully inspected. Some agents report the model on earlier
// hooks that fire as separate CLI processes before TurnStart:
//
//   - Claude Code sends "model" on SessionStart (before any TurnStart)
//   - Gemini CLI sends "llm_request.model" on BeforeModel (after TurnStart,
//     so handleLifecycleModelUpdate writes to SessionState directly when it
//     exists and only falls back to this hint file otherwise)
//
// The hint is read by handleLifecycleTurnStart/TurnEnd when event.Model is
// empty, passed to InitializeSession, and persisted in state.ModelName. After
// that the hint file is redundant — it sits unused until ClearSessionState
// removes it alongside the session state file.
func StoreModelHint(ctx context.Context, sessionID, model string) error {
	if err := validation.ValidateSessionID(sessionID); err != nil {
		return fmt.Errorf("invalid session ID: %w", err)
	}
	if model == "" {
		return nil
	}

	stateDir, err := getSessionStateDir(ctx)
	if err != nil {
		return fmt.Errorf("failed to get session state directory: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return fmt.Errorf("failed to create session state directory: %w", err)
	}

	hintFile := filepath.Join(stateDir, sessionID+".model")
	if err := os.WriteFile(hintFile, []byte(model), 0o600); err != nil {
		return fmt.Errorf("failed to write model hint file: %w", err)
	}
	return nil
}

// LoadModelHint reads the LLM model name from the hint file for the given session.
// Returns empty string if the hint file doesn't exist or can't be read.
func LoadModelHint(ctx context.Context, sessionID string) string {
	if err := validation.ValidateSessionID(sessionID); err != nil {
		return ""
	}

	stateDir, err := getSessionStateDir(ctx)
	if err != nil {
		logging.Warn(logging.WithComponent(ctx, "session"), "failed to resolve state dir for model hint",
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return ""
	}

	hintPath := filepath.Join(stateDir, sessionID+".model")
	data, err := os.ReadFile(hintPath) //nolint:gosec // sessionID is validated above
	if err != nil {
		if !os.IsNotExist(err) {
			logging.Warn(logging.WithComponent(ctx, "session"), "failed to read model hint file",
				slog.String("path", hintPath),
				slog.Any("error", err))
		}
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ClearSessionState removes the session state file for the given session ID.
func ClearSessionState(ctx context.Context, sessionID string) error {
	// Validate session ID to prevent path traversal
	if err := validation.ValidateSessionID(sessionID); err != nil {
		return fmt.Errorf("invalid session ID: %w", err)
	}

	stateDir, err := getSessionStateDir(ctx)
	if err != nil {
		return fmt.Errorf("failed to get session state directory: %w", err)
	}

	// Remove all files for this session (state .json, .model hint, any future hint files).
	matches, _ := filepath.Glob(filepath.Join(stateDir, sessionID+".*")) //nolint:errcheck // pattern is always valid
	for _, f := range matches {
		_ = os.Remove(f)
	}

	return nil
}

// StoreAgentTypeHint writes the agent type hint for a session using
// first-writer-wins semantics. Returns (true, nil) when this call won the race.
func StoreAgentTypeHint(ctx context.Context, sessionID string, agentType types.AgentType) (created bool, err error) {
	if vErr := validation.ValidateSessionID(sessionID); vErr != nil {
		return false, fmt.Errorf("invalid session ID: %w", vErr)
	}
	if agentType == "" || agentType == agent.AgentTypeUnknown {
		return false, nil
	}

	stateDir, sErr := getSessionStateDir(ctx)
	if sErr != nil {
		return false, fmt.Errorf("failed to get session state directory: %w", sErr)
	}
	if mErr := os.MkdirAll(stateDir, 0o750); mErr != nil {
		return false, fmt.Errorf("failed to create session state directory: %w", mErr)
	}

	hintFile := filepath.Join(stateDir, sessionID+".agent")
	f, oErr := os.OpenFile(hintFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // hintFile path is built from validated sessionID
	if oErr != nil {
		if errors.Is(oErr, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("failed to create agent hint file: %w", oErr)
	}
	defer f.Close()
	if _, wErr := f.WriteString(string(agentType)); wErr != nil {
		return false, fmt.Errorf("failed to write agent hint file: %w", wErr)
	}
	return true, nil
}

// ClaimSessionStartBanner records that the SessionStart banner has been emitted
// for a session. First-writer-wins semantics.
func ClaimSessionStartBanner(ctx context.Context, sessionID string) (claimed bool, err error) {
	if vErr := validation.ValidateSessionID(sessionID); vErr != nil {
		return false, fmt.Errorf("invalid session ID: %w", vErr)
	}

	stateDir, sErr := getSessionStateDir(ctx)
	if sErr != nil {
		return false, fmt.Errorf("failed to get session state directory: %w", sErr)
	}
	if mErr := os.MkdirAll(stateDir, 0o750); mErr != nil {
		return false, fmt.Errorf("failed to create session state directory: %w", mErr)
	}

	markerFile := filepath.Join(stateDir, sessionID+".banner")
	f, oErr := os.OpenFile(markerFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // markerFile path is built from validated sessionID
	if oErr != nil {
		if errors.Is(oErr, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("failed to create banner marker file: %w", oErr)
	}
	_ = f.Close()
	return true, nil
}

// LoadAgentTypeHint reads the .agent hint file for a session, returning
// the agent type that first claimed ownership via SessionStart.
func LoadAgentTypeHint(ctx context.Context, sessionID string) types.AgentType {
	if err := validation.ValidateSessionID(sessionID); err != nil {
		return ""
	}

	stateDir, err := getSessionStateDir(ctx)
	if err != nil {
		logging.Warn(logging.WithComponent(ctx, "session"), "failed to resolve state dir for agent hint",
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return ""
	}

	hintPath := filepath.Join(stateDir, sessionID+".agent")
	data, err := os.ReadFile(hintPath) //nolint:gosec // sessionID is validated above
	if err != nil {
		if !os.IsNotExist(err) {
			logging.Warn(logging.WithComponent(ctx, "session"), "failed to read agent hint file",
				slog.String("path", hintPath),
				slog.Any("error", err))
		}
		return ""
	}
	return types.AgentType(strings.TrimSpace(string(data)))
}

// sessionMutationGate provides per-process serialization layered over the
// OS-level flock so that nested MutateSessionState calls in the same
// goroutine don't deadlock or lose updates. POSIX flock isn't reentrant
// across distinct file descriptors in the same process; on top of that, a
// nested call that did its own load → save would have its save overwritten
// by the outer save. The gate fixes both: nested calls in the same
// goroutine reuse the outer's state pointer (no second load, no second
// save), and only the outermost release drops the flock.
var sessionMutationGate sync.Map // map[string]*sessionGate

type sessionGate struct {
	mu          sync.Mutex
	owner       int64 // goroutine ID of the current holder, 0 when unlocked
	depth       int
	flockRel    func()
	activeState *SessionState // shared state pointer for nested mutations
}

// goroutineID extracts the runtime goroutine ID from the stack header. Used
// only as a reentrancy key for the session mutation gate — never as a
// security boundary or for application logic.
func goroutineID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	const prefix = "goroutine "
	s := string(buf[:n])
	if !strings.HasPrefix(s, prefix) {
		return -1
	}
	s = s[len(prefix):]
	end := strings.IndexByte(s, ' ')
	if end < 0 {
		return -1
	}
	id, err := strconv.ParseInt(s[:end], 10, 64)
	if err != nil {
		return -1
	}
	return id
}

// ErrMutationSkip signals MutateSessionState to skip the save without
// treating fn's return as an error. Use it when the mutation function
// observes the loaded state and decides no write is needed.
var ErrMutationSkip = errors.New("session state mutation skipped")

// ErrStateNotFound is returned by MutateSessionState when no state file
// exists for the session ID (typically because the event arrived before
// InitializeSession ran).
var ErrStateNotFound = errors.New("session state not found")

// MutateSessionState is the safe load → mutate → save helper. It takes an
// OS-level advisory lock against .git/trace-session-locks/<id>.lock for the
// duration of the read+write so concurrent processes cannot lose each
// other's updates. fn receives the freshly-loaded state and mutates it in
// place; returning ErrMutationSkip skips the save. Reentrant within the same
// goroutine: nested calls share the outer's state pointer and skip the
// inner load/save, so all mutations are flushed by the outermost call.
//
// Returns ErrStateNotFound if the state file doesn't exist (event arrived
// before InitializeSession). Errors from fn or from load/save propagate.
func MutateSessionState(ctx context.Context, sessionID string, fn func(*SessionState) error) error {
	if sessionID == "" {
		return ErrStateNotFound
	}
	gate, isOuter, release, err := acquireSessionGate(ctx, sessionID)
	if err != nil {
		return err
	}
	defer release()

	if !isOuter {
		// Nested call: reuse the outer's state pointer.
		if gate.activeState == nil {
			return ErrStateNotFound
		}
		if err := fn(gate.activeState); err != nil && !errors.Is(err, ErrMutationSkip) {
			return err
		}
		return nil
	}

	state, err := LoadSessionState(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("load session state: %w", err)
	}
	if state == nil {
		return ErrStateNotFound
	}
	gate.activeState = state
	defer func() { gate.activeState = nil }()

	if err := fn(state); err != nil {
		if errors.Is(err, ErrMutationSkip) {
			return nil
		}
		return err
	}
	if err := SaveSessionState(ctx, state); err != nil {
		return fmt.Errorf("save session state: %w", err)
	}
	return nil
}

// acquireSessionGate takes the per-process gate (in-memory) and, on the
// outermost call, the cross-process flock.
func acquireSessionGate(ctx context.Context, sessionID string) (gate *sessionGate, isOuter bool, release func(), err error) {
	val, _ := sessionMutationGate.LoadOrStore(sessionID, &sessionGate{})
	gate, ok := val.(*sessionGate)
	if !ok {
		return nil, false, nil, fmt.Errorf("session gate type assertion failed for %s", sessionID)
	}

	gid := goroutineID()
	gate.mu.Lock()
	if gate.owner == gid {
		gate.depth++
		gate.mu.Unlock()
		return gate, false, func() {
			gate.mu.Lock()
			gate.depth--
			gate.mu.Unlock()
		}, nil
	}
	gate.mu.Unlock()

	lockPath, err := stateLockPath(ctx, sessionID)
	if err != nil {
		return nil, false, nil, fmt.Errorf("resolve state lock path: %w", err)
	}
	flockRel, err := flock.Acquire(lockPath)
	if err != nil {
		return nil, false, nil, fmt.Errorf("acquire state lock: %w", err)
	}

	gate.mu.Lock()
	gate.owner = gid
	gate.depth = 1
	gate.flockRel = flockRel
	gate.mu.Unlock()

	return gate, true, func() {
		gate.mu.Lock()
		gate.depth--
		if gate.depth == 0 {
			rel := gate.flockRel
			gate.flockRel = nil
			gate.owner = 0
			gate.mu.Unlock()
			rel()
			return
		}
		gate.mu.Unlock()
	}, nil
}

// stateLockPath returns the lock file path for a session. Lock files live in
// .git/trace-session-locks/ (a sibling to trace-sessions/) so callers that
// enumerate session state files don't have to filter lock entries.
func stateLockPath(ctx context.Context, sessionID string) (string, error) {
	if err := validation.ValidateSessionID(sessionID); err != nil {
		return "", fmt.Errorf("invalid session ID: %w", err)
	}
	commonDir, err := GetGitCommonDir(ctx)
	if err != nil {
		return "", err
	}
	lockDir := filepath.Join(commonDir, "trace-session-locks")
	if err := os.MkdirAll(lockDir, 0o750); err != nil {
		return "", fmt.Errorf("create session lock directory: %w", err)
	}
	return filepath.Join(lockDir, sessionID+".lock"), nil
}

// RecordFilesTouched merges paths into the session's FilesTouched, used by
// mid-turn lifecycle events (per-tool-use hooks) so PostCommit's carry-forward
// decision sees an accurate file list. Caller must pre-normalize paths to
// repo-relative form. No-ops when the session state doesn't exist or the
// merge produced no changes.
func RecordFilesTouched(ctx context.Context, sessionID string, modified, added, deleted []string) error {
	if len(modified) == 0 && len(added) == 0 && len(deleted) == 0 {
		return nil
	}
	err := MutateSessionState(ctx, sessionID, func(state *SessionState) error {
		merged := mergeFilesTouched(state.FilesTouched, modified, added, deleted)
		if slices.Equal(merged, state.FilesTouched) {
			return ErrMutationSkip
		}
		state.FilesTouched = merged
		return nil
	})
	if errors.Is(err, ErrStateNotFound) {
		return nil
	}
	return err
}
