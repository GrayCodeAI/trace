package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type agentCaptureConfig struct {
	URL                     string `json:"url"`
	User                    string `json:"user"`
	TokenFile               string `json:"token_file"`
	Repo                    string `json:"repo"`
	Worktree                string `json:"worktree"`
	IncludeAssistantSummary bool   `json:"include_assistant_summary"`
}

type queuedAgentEvent struct {
	Agent      string `json:"agent"`
	SessionKey string `json:"session_key"`
	EventID    string `json:"event_id"`
	Ref        string `json:"ref"`
	Summary    string `json:"summary"`
}

type codexNotification struct {
	Type                 string `json:"type"`
	ThreadID             string `json:"thread-id"`
	TurnID               string `json:"turn-id"`
	CWD                  string `json:"cwd"`
	LastAssistantMessage string `json:"last-assistant-message"`
}

type claudeStopNotification struct {
	HookEventName        string `json:"hook_event_name"`
	SessionID            string `json:"session_id"`
	PromptID             string `json:"prompt_id"`
	CWD                  string `json:"cwd"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

type geminiAfterAgentNotification struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	Timestamp      string `json:"timestamp"`
	PromptResponse string `json:"prompt_response"`
}

func agentCaptureCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trace agent <configure|codex-notify|claude-stop|gemini-after-agent|cursor-stop|sync|pending|drop> ...")
	}
	fs := flag.NewFlagSet("agent "+args[0], flag.ContinueOnError)
	configPath := fs.String("config", "", "private capture configuration file")
	base := fs.String("url", "http://127.0.0.1:8787", "Trace server URL")
	user := fs.String("user", "admin", "Trace username")
	tokenFile := fs.String("token-file", "", "file containing a Trace personal token")
	repo := fs.String("repo", "", "Trace OWNER/NAME repository")
	worktree := fs.String("worktree", ".", "local Git worktree")
	includeSummary := fs.Bool("include-assistant-summary", false, "opt in to storing the last assistant message after basic redaction")
	dropEventID := fs.String("event-id", "", "exact queued event ID to discard")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *configPath == "" {
		return errors.New("-config FILE is required")
	}
	switch args[0] {
	case "configure":
		if fs.NArg() != 0 || !validRepoName(*repo) || *tokenFile == "" || strings.TrimSpace(*user) == "" {
			return errors.New("usage: trace agent configure -config NEW_FILE -url URL -user USER -token-file FILE -repo OWNER/NAME [-worktree DIR] [-include-assistant-summary]")
		}
		parsed, err := url.Parse(*base)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("-url must be an HTTP(S) URL without embedded credentials or a query")
		}
		root, err := gitWorktreeRoot(*worktree)
		if err != nil {
			return err
		}
		tokenPath, err := filepath.Abs(*tokenFile)
		if err != nil {
			return err
		}
		if _, err := os.Stat(tokenPath); err != nil {
			return fmt.Errorf("token file: %w", err)
		}
		cfg := agentCaptureConfig{URL: strings.TrimRight(*base, "/"), User: *user, TokenFile: tokenPath, Repo: *repo, Worktree: root, IncludeAssistantSummary: *includeSummary}
		encoded, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.OpenFile(*configPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			_ = os.Remove(*configPath)
			return err
		}
		if err := file.Sync(); err != nil {
			_ = os.Remove(*configPath)
			return err
		}
		fmt.Println("created private capture configuration:", *configPath)
		return nil
	case "codex-notify":
		if fs.NArg() != 1 {
			return errors.New("usage: trace agent codex-notify -config FILE CODEX_JSON_EVENT")
		}
		cfg, err := loadAgentCaptureConfig(*configPath)
		if err != nil {
			return err
		}
		var notice codexNotification
		if err := json.Unmarshal([]byte(fs.Arg(0)), &notice); err != nil {
			return errors.New("invalid Codex notification JSON")
		}
		if notice.Type != "agent-turn-complete" {
			return nil
		}
		if notice.ThreadID == "" || notice.TurnID == "" || notice.CWD == "" {
			return errors.New("Codex notification is missing thread-id, turn-id, or cwd")
		}
		root, err := gitWorktreeRoot(notice.CWD)
		if err != nil {
			return nil // Global Codex notifications can come from non-Git directories.
		}
		if root != cfg.Worktree {
			return nil
		}
		commit, err := gitCaptureOutput(root, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil || len(commit) != 40 {
			return errors.New("cannot resolve local HEAD")
		}
		summary := "Codex turn completed; HEAD observed at notification time"
		if cfg.IncludeAssistantSummary && strings.TrimSpace(notice.LastAssistantMessage) != "" {
			summary = redactAgentText(notice.LastAssistantMessage)
		}
		event := queuedAgentEvent{Agent: "Codex", SessionKey: captureID("codex", notice.ThreadID), EventID: captureID("turn", notice.ThreadID+"\x00"+notice.TurnID), Ref: commit, Summary: summary}
		return enqueueAndTryAgentEvent(*configPath, cfg, event)
	case "claude-stop":
		if fs.NArg() != 0 {
			return errors.New("usage: trace agent claude-stop -config FILE < CLAUDE_HOOK_JSON")
		}
		cfg, err := loadAgentCaptureConfig(*configPath)
		if err != nil {
			return err
		}
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20+1))
		if err != nil {
			return err
		}
		if len(input) > 1<<20 {
			return errors.New("Claude hook input exceeds 1 MiB")
		}
		event, matched, err := claudeStopCaptureEvent(input, cfg)
		if err != nil || !matched {
			return err
		}
		return enqueueAndTryAgentEvent(*configPath, cfg, event)
	case "gemini-after-agent":
		if fs.NArg() != 0 {
			return errors.New("usage: trace agent gemini-after-agent -config FILE < GEMINI_HOOK_JSON")
		}
		cfg, err := loadAgentCaptureConfig(*configPath)
		if err != nil {
			return err
		}
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20+1))
		if err != nil {
			return err
		}
		if len(input) > 4<<20 {
			return errors.New("Gemini hook input exceeds 4 MiB")
		}
		event, matched, err := geminiAfterAgentCaptureEvent(input, cfg)
		if err != nil {
			return err
		}
		if matched {
			if err := enqueueAndTryAgentEvent(*configPath, cfg, event); err != nil {
				return err
			}
		}
		fmt.Println("{}") // Gemini parses successful hook stdout as JSON.
		return nil
	case "cursor-stop":
		if fs.NArg() != 0 {
			return errors.New("usage: trace agent cursor-stop -config FILE < CURSOR_HOOK_JSON")
		}
		cfg, err := loadAgentCaptureConfig(*configPath)
		if err != nil {
			return err
		}
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20+1))
		if err != nil {
			return err
		}
		if len(input) > 1<<20 {
			return errors.New("Cursor hook input exceeds 1 MiB")
		}
		event, matched, err := cursorStopCaptureEvent(input, cfg)
		if err != nil {
			return err
		}
		if matched {
			if err := enqueueAndTryAgentEvent(*configPath, cfg, event); err != nil {
				return err
			}
		}
		fmt.Println("{}") // Cursor command hooks exchange JSON on stdout.
		return nil
	case "sync":
		if fs.NArg() != 0 {
			return errors.New("usage: trace agent sync -config FILE")
		}
		cfg, err := loadAgentCaptureConfig(*configPath)
		if err != nil {
			return err
		}
		count, err := syncAgentOutbox(*configPath, cfg, 1000)
		if err != nil {
			return fmt.Errorf("synced %d events; remaining events are queued: %w", count, err)
		}
		fmt.Printf("synced %d agent events\n", count)
		return nil
	case "pending":
		if fs.NArg() != 0 {
			return errors.New("usage: trace agent pending -config FILE")
		}
		if _, err := loadAgentCaptureConfig(*configPath); err != nil {
			return err
		}
		return withAgentOutbox(*configPath, func(events *[]queuedAgentEvent) error {
			fmt.Printf("%d pending agent events\n", len(*events))
			for _, event := range *events {
				fmt.Printf("%s %s %s\n", event.Agent, event.EventID, event.Ref)
			}
			return nil
		})
	case "drop":
		if fs.NArg() != 0 || *dropEventID == "" {
			return errors.New("usage: trace agent drop -config FILE -event-id ID")
		}
		if _, err := loadAgentCaptureConfig(*configPath); err != nil {
			return err
		}
		err := withAgentOutbox(*configPath, func(events *[]queuedAgentEvent) error {
			for i, event := range *events {
				if event.EventID == *dropEventID {
					*events = append((*events)[:i], (*events)[i+1:]...)
					return nil
				}
			}
			return errors.New("queued event not found")
		})
		if err != nil {
			return err
		}
		fmt.Println("discarded queued event", *dropEventID)
		return nil
	default:
		return errors.New("usage: trace agent <configure|codex-notify|claude-stop|gemini-after-agent|cursor-stop|sync|pending|drop> ...")
	}
}

func geminiAfterAgentCaptureEvent(input []byte, cfg agentCaptureConfig) (queuedAgentEvent, bool, error) {
	var notice geminiAfterAgentNotification
	if err := json.Unmarshal(input, &notice); err != nil {
		return queuedAgentEvent{}, false, errors.New("invalid Gemini hook JSON")
	}
	if notice.HookEventName != "AfterAgent" {
		return queuedAgentEvent{}, false, nil
	}
	if notice.SessionID == "" || notice.CWD == "" || notice.Timestamp == "" {
		return queuedAgentEvent{}, false, errors.New("Gemini AfterAgent hook is missing session_id, cwd, or timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, notice.Timestamp); err != nil {
		return queuedAgentEvent{}, false, errors.New("Gemini hook timestamp is not ISO 8601 with a timezone")
	}
	root, err := gitWorktreeRoot(notice.CWD)
	if err != nil || root != cfg.Worktree {
		return queuedAgentEvent{}, false, nil
	}
	commit, err := gitCaptureOutput(root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || len(commit) != 40 {
		return queuedAgentEvent{}, false, errors.New("cannot resolve local HEAD")
	}
	summary := "Gemini CLI turn completed; HEAD observed at notification time"
	if cfg.IncludeAssistantSummary && strings.TrimSpace(notice.PromptResponse) != "" {
		summary = redactAgentText(notice.PromptResponse)
	}
	eventID := captureID("after", notice.SessionID+"\x00"+notice.Timestamp+"\x00"+commit+"\x00"+summary)
	return queuedAgentEvent{Agent: "Gemini CLI", SessionKey: captureID("gemini", notice.SessionID), EventID: eventID, Ref: commit, Summary: summary}, true, nil
}

func claudeStopCaptureEvent(input []byte, cfg agentCaptureConfig) (queuedAgentEvent, bool, error) {
	var notice claudeStopNotification
	if err := json.Unmarshal(input, &notice); err != nil {
		return queuedAgentEvent{}, false, errors.New("invalid Claude hook JSON")
	}
	if notice.HookEventName != "Stop" {
		return queuedAgentEvent{}, false, nil
	}
	if notice.SessionID == "" || notice.CWD == "" {
		return queuedAgentEvent{}, false, errors.New("Claude Stop hook is missing session_id or cwd")
	}
	root, err := gitWorktreeRoot(notice.CWD)
	if err != nil || root != cfg.Worktree {
		return queuedAgentEvent{}, false, nil
	}
	commit, err := gitCaptureOutput(root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || len(commit) != 40 {
		return queuedAgentEvent{}, false, errors.New("cannot resolve local HEAD")
	}
	summary := "Claude Code turn completed; HEAD observed at notification time"
	if cfg.IncludeAssistantSummary && strings.TrimSpace(notice.LastAssistantMessage) != "" {
		summary = redactAgentText(notice.LastAssistantMessage)
	}
	var eventID string
	if notice.PromptID != "" {
		eventID = captureID("stop", notice.SessionID+"\x00"+notice.PromptID+"\x00"+commit+"\x00"+summary)
	} else {
		// Older Claude Code versions omit prompt_id. Keep each Stop event rather
		// than silently merging separate turns with the same text and HEAD.
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return queuedAgentEvent{}, false, err
		}
		eventID = "stop-" + hex.EncodeToString(random)
	}
	return queuedAgentEvent{Agent: "Claude Code", SessionKey: captureID("claude", notice.SessionID), EventID: eventID, Ref: commit, Summary: summary}, true, nil
}

func enqueueAndTryAgentEvent(configPath string, cfg agentCaptureConfig, event queuedAgentEvent) error {
	if err := appendAgentOutbox(configPath, event); err != nil {
		return err
	}
	if _, err := syncAgentOutbox(configPath, cfg, 1); err != nil {
		fmt.Fprintln(os.Stderr, "trace: agent event queued; push the commit, then run trace agent sync -config", configPath)
	}
	return nil
}

func captureID(prefix, value string) string {
	digest := sha256.Sum256([]byte(value))
	return prefix + "-" + hex.EncodeToString(digest[:])
}

func gitCaptureOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.Output()
	return strings.TrimSpace(string(output)), err
}

func gitWorktreeRoot(dir string) (string, error) {
	root, err := gitCaptureOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return "", errors.New("not a Git worktree")
	}
	return filepath.EvalSymlinks(root)
}

func loadAgentCaptureConfig(path string) (agentCaptureConfig, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return agentCaptureConfig{}, err
	}
	var cfg agentCaptureConfig
	if err := json.Unmarshal(encoded, &cfg); err != nil {
		return cfg, err
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || !validRepoName(cfg.Repo) || cfg.User == "" || cfg.TokenFile == "" || cfg.Worktree == "" {
		return cfg, errors.New("invalid capture configuration")
	}
	return cfg, nil
}

func withAgentOutbox(path string, change func(*[]queuedAgentEvent) error) error {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	outboxPath := path + ".outbox.json"
	events := []queuedAgentEvent{}
	encoded, err := os.ReadFile(outboxPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && json.Unmarshal(encoded, &events) != nil {
		return errors.New("invalid agent outbox")
	}
	if err := change(&events); err != nil {
		return err
	}
	encoded, err = json.MarshalIndent(events, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(outboxPath), ".trace-agent-outbox-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(append(encoded, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), outboxPath)
}

func appendAgentOutbox(path string, event queuedAgentEvent) error {
	return withAgentOutbox(path, func(events *[]queuedAgentEvent) error {
		for _, existing := range *events {
			if existing.SessionKey == event.SessionKey && existing.EventID == event.EventID {
				return nil
			}
		}
		if len(*events) >= 1000 {
			return errors.New("agent outbox is full; sync it before capturing more events")
		}
		*events = append(*events, event)
		return nil
	})
}

func syncAgentOutbox(path string, cfg agentCaptureConfig, limit int) (int, error) {
	count := 0
	for count < limit {
		var next *queuedAgentEvent
		err := withAgentOutbox(path, func(events *[]queuedAgentEvent) error {
			if len(*events) > 0 {
				copy := (*events)[0]
				next = &copy
			}
			return nil
		})
		if err != nil || next == nil {
			return count, err
		}
		if err := sendAgentCapture(cfg, *next); err != nil {
			return count, err
		}
		err = withAgentOutbox(path, func(events *[]queuedAgentEvent) error {
			if len(*events) > 0 && (*events)[0].SessionKey == next.SessionKey && (*events)[0].EventID == next.EventID {
				*events = (*events)[1:]
			}
			return nil
		})
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func sendAgentCapture(cfg agentCaptureConfig, event queuedAgentEvent) error {
	tokenBytes, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return errors.New("capture token file is empty")
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	endpoint := cfg.URL + "/api/v1/repos/" + cfg.Repo + "/agent-sessions/capture"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.SetBasicAuth(cfg.User, token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("capture API returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	return nil
}
