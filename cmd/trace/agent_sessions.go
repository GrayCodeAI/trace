package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const agentSessionFile = "agent-sessions.json"

type agentCheckpoint struct {
	ID             int       `json:"id"`
	Commit         string    `json:"commit"`
	Summary        string    `json:"summary"`
	State          string    `json:"state,omitempty"`
	Redacted       bool      `json:"redacted"`
	CreatedAt      time.Time `json:"created_at"`
	CaptureEventID string    `json:"capture_event_id,omitempty"`
}

type agentSession struct {
	ID                  int               `json:"id"`
	Repo                string            `json:"repo"`
	Agent               string            `json:"agent"`
	Commit              string            `json:"commit"`
	Status              string            `json:"status"`
	Summary             string            `json:"summary,omitempty"`
	Redacted            bool              `json:"redacted"`
	CreatedBy           string            `json:"created_by"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	Checkpoints         []agentCheckpoint `json:"checkpoints,omitempty"`
	OriginNodeID        string            `json:"origin_node_id,omitempty"`
	OriginSessionID     int               `json:"origin_session_id,omitempty"`
	CaptureSessionKey   string            `json:"capture_session_key,omitempty"`
	CaptureFirstEventID string            `json:"capture_first_event_id,omitempty"`
}

type agentSessionDB struct {
	NextID   int            `json:"next_id"`
	Sessions []agentSession `json:"sessions"`
}

var sensitiveAgentText = regexp.MustCompile(`(?i)(token|password|secret|authorization)\s*[:=]\s*[^\s,;]+`)
var agentCaptureID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var agentCaptureCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

func redactAgentText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 20000 {
		value = value[:20000]
	}
	return sensitiveAgentText.ReplaceAllString(value, `$1=[REDACTED]`)
}

func (s *store) loadAgentSessions() (agentSessionDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, agentSessionFile))
	if errors.Is(err, os.ErrNotExist) {
		return agentSessionDB{NextID: 1, Sessions: []agentSession{}}, nil
	}
	if err != nil {
		return agentSessionDB{}, err
	}
	var db agentSessionDB
	if err := json.Unmarshal(b, &db); err != nil {
		return agentSessionDB{}, fmt.Errorf("read agent sessions: %w", err)
	}
	if db.NextID < 1 {
		db.NextID = 1
	}
	if db.Sessions == nil {
		db.Sessions = []agentSession{}
	}
	return db, nil
}

func (s *store) updateAgentSessions(change func(*agentSessionDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".agent-sessions.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadAgentSessions()
	if err != nil {
		return err
	}
	if err := change(&db); err != nil {
		return err
	}
	b, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".agent-sessions-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(s.root, agentSessionFile))
}

func resolveAgentCommit(repoPath, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		ref = "main"
	}
	if len(ref) > 200 || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, " \t\r\n") {
		return "", errors.New("invalid commit or ref")
	}
	commit, err := gitActionOutput(repoPath, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil || len(commit) != 40 {
		return "", errors.New("commit or ref not found")
	}
	return commit, nil
}

func (s *store) createAgentSession(repo, agent, ref, summary, actor string) (agentSession, error) {
	path, err := s.repoPath(repo)
	if err != nil {
		return agentSession{}, err
	}
	if _, err := os.Stat(path); err != nil {
		return agentSession{}, errors.New("repository not found")
	}
	agent = strings.TrimSpace(agent)
	if agent == "" || len(agent) > 100 {
		return agentSession{}, errors.New("agent name must be between 1 and 100 characters")
	}
	commit, err := resolveAgentCommit(path, ref)
	if err != nil {
		return agentSession{}, err
	}
	var created agentSession
	err = s.updateAgentSessions(func(db *agentSessionDB) error {
		now := time.Now().UTC()
		created = agentSession{ID: db.NextID, Repo: repo, Agent: agent, Commit: commit, Status: "active", Summary: redactAgentText(summary), Redacted: true, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
		db.NextID++
		db.Sessions = append(db.Sessions, created)
		return nil
	})
	return created, err
}

func (s *store) listAgentSessions(repo string) ([]agentSession, error) {
	db, err := s.loadAgentSessions()
	if err != nil {
		return nil, err
	}
	items := make([]agentSession, 0)
	for _, session := range db.Sessions {
		if repo == "" || session.Repo == repo {
			items = append(items, session)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	return items, nil
}

func (s *store) agentSessionByID(repo string, id int) (agentSession, error) {
	db, err := s.loadAgentSessions()
	if err != nil {
		return agentSession{}, err
	}
	for _, session := range db.Sessions {
		if session.Repo == repo && session.ID == id {
			return session, nil
		}
	}
	return agentSession{}, errors.New("agent session not found")
}

func (s *store) addAgentCheckpoint(repo string, id int, ref, summary, state string) (agentSession, error) {
	path, err := s.repoPath(repo)
	if err != nil {
		return agentSession{}, err
	}
	commit, err := resolveAgentCommit(path, ref)
	if err != nil {
		return agentSession{}, err
	}
	var updated agentSession
	err = s.updateAgentSessions(func(db *agentSessionDB) error {
		var found *agentSession
		for i := range db.Sessions {
			if db.Sessions[i].Repo == repo && db.Sessions[i].ID == id {
				found = &db.Sessions[i]
				break
			}
		}
		if found == nil {
			return errors.New("agent session not found")
		}
		if found.OriginNodeID != "" {
			return errors.New("imported agent sessions cannot be changed locally")
		}
		checkpointID := 1
		for _, checkpoint := range found.Checkpoints {
			if checkpoint.ID >= checkpointID {
				checkpointID = checkpoint.ID + 1
			}
		}
		found.Checkpoints = append(found.Checkpoints, agentCheckpoint{ID: checkpointID, Commit: commit, Summary: redactAgentText(summary), State: redactAgentText(state), Redacted: true, CreatedAt: time.Now().UTC()})
		found.UpdatedAt = time.Now().UTC()
		updated = *found
		return nil
	})
	return updated, err
}

// captureAgentEvent is idempotent for a provider session and event ID. The commit
// is the client's HEAD at event time; it is not evidence that the agent authored it.
func (s *store) captureAgentEvent(repo, agent, sessionKey, eventID, ref, summary, actor string) (agentSession, bool, error) {
	if !agentCaptureID.MatchString(sessionKey) || !agentCaptureID.MatchString(eventID) {
		return agentSession{}, false, errors.New("invalid capture session or event ID")
	}
	if !agentCaptureCommit.MatchString(ref) {
		return agentSession{}, false, errors.New("capture requires an exact 40-character commit SHA")
	}
	agent = strings.TrimSpace(agent)
	if agent == "" || len(agent) > 100 {
		return agentSession{}, false, errors.New("agent name must be between 1 and 100 characters")
	}
	path, err := s.repoPath(repo)
	if err != nil {
		return agentSession{}, false, err
	}
	commit, err := resolveAgentCommit(path, ref)
	if err != nil {
		return agentSession{}, false, err
	}
	summary = redactAgentText(summary)
	var result agentSession
	created := false
	err = s.updateAgentSessions(func(db *agentSessionDB) error {
		for i := range db.Sessions {
			found := &db.Sessions[i]
			if found.Repo != repo || found.OriginNodeID != "" || found.CaptureSessionKey != sessionKey || found.CreatedBy != actor {
				continue
			}
			if found.Agent != agent {
				return errors.New("capture session agent changed")
			}
			if found.CaptureFirstEventID == eventID {
				if found.Commit != commit || found.Summary != summary {
					return errors.New("capture event ID was reused with different content")
				}
				result = *found
				return nil
			}
			for _, checkpoint := range found.Checkpoints {
				if checkpoint.CaptureEventID == eventID {
					if checkpoint.Commit != commit || checkpoint.Summary != summary {
						return errors.New("capture event ID was reused with different content")
					}
					result = *found
					return nil
				}
			}
			now := time.Now().UTC()
			found.Checkpoints = append(found.Checkpoints, agentCheckpoint{ID: len(found.Checkpoints) + 1, Commit: commit, Summary: summary, State: "HEAD observed at turn completion", Redacted: true, CreatedAt: now, CaptureEventID: eventID})
			found.UpdatedAt = now
			result = *found
			created = true
			return nil
		}
		now := time.Now().UTC()
		result = agentSession{ID: db.NextID, Repo: repo, Agent: agent, Commit: commit, Status: "active", Summary: summary, Redacted: true, CreatedBy: actor, CreatedAt: now, UpdatedAt: now, CaptureSessionKey: sessionKey, CaptureFirstEventID: eventID}
		db.NextID++
		db.Sessions = append(db.Sessions, result)
		created = true
		return nil
	})
	return result, created, err
}

func (a *app) apiAgentSessions(w http.ResponseWriter, r *http.Request, u userRecord, username, repo string, tail []string) {
	if len(tail) == 1 && tail[0] == "git-bundle" {
		switch r.Method {
		case http.MethodGet:
			result, err := a.store.agentGitStatus(repo)
			if err != nil {
				apiError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, result)
		case http.MethodPost:
			if !u.Admin {
				apiError(w, http.StatusForbidden, "admin required to publish Git agent history")
				return
			}
			result, err := a.store.publishAgentGitBundle(repo)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			if result.Changed {
				_ = a.store.recordAudit(username, "agent_git.publish", repo, result.Ref, map[string]any{"commit": result.Commit, "sessions": result.Sessions})
			}
			writeJSON(w, http.StatusOK, result)
		case http.MethodDelete:
			if !u.Admin {
				apiError(w, http.StatusForbidden, "admin required to remove Git agent history")
				return
			}
			var input struct {
				ExpectedNodeID string `json:"expected_node_id"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
			if err := decoder.Decode(&input); err != nil {
				apiError(w, http.StatusBadRequest, "expected node ID is required")
				return
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				apiError(w, http.StatusBadRequest, "request contains extra data")
				return
			}
			result, err := a.store.unpublishAgentGitBundle(repo, input.ExpectedNodeID)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "agent_git.unpublish", repo, result.Ref, nil)
			writeJSON(w, http.StatusOK, result)
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if len(tail) == 1 && tail[0] == "capture" {
		if r.Method != http.MethodPost {
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !u.canWrite(repo) {
			apiError(w, http.StatusForbidden, "write access required")
			return
		}
		var input struct {
			Agent      string `json:"agent"`
			SessionKey string `json:"session_key"`
			EventID    string `json:"event_id"`
			Ref        string `json:"ref"`
			Summary    string `json:"summary"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		if err := decoder.Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid capture event")
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			apiError(w, http.StatusBadRequest, "capture event contains extra data")
			return
		}
		session, created, err := a.store.captureAgentEvent(repo, input.Agent, input.SessionKey, input.EventID, input.Ref, input.Summary, username)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		if created {
			_ = a.store.recordAudit(username, "agent_session.capture", repo, fmt.Sprint(session.ID), map[string]any{"event_id": input.EventID})
		}
		writeJSON(w, http.StatusOK, session)
		return
	}
	if len(tail) == 1 && tail[0] == "bundle" {
		switch r.Method {
		case http.MethodGet:
			bundle, err := a.store.exportAgentBundle(repo)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			w.Header().Set("Content-Disposition", `attachment; filename="trace-agent-sessions.json"`)
			writeJSON(w, http.StatusOK, bundle)
		case http.MethodPost:
			if !u.Admin {
				apiError(w, http.StatusForbidden, "admin required to import signed agent history")
				return
			}
			var input struct {
				ExpectedNodeID string            `json:"expected_node_id"`
				Bundle         signedAgentBundle `json:"bundle"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAgentBundleBytes+64<<10))
			if err := decoder.Decode(&input); err != nil {
				apiError(w, http.StatusBadRequest, "invalid agent bundle")
				return
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				apiError(w, http.StatusBadRequest, "agent bundle contains extra data")
				return
			}
			result, err := a.store.importAgentBundle(repo, input.ExpectedNodeID, input.Bundle)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "agent_bundle.import", repo, input.ExpectedNodeID, map[string]any{"sessions_created": result.SessionsCreated, "checkpoints_added": result.CheckpointsAdded})
			writeJSON(w, http.StatusOK, result)
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if len(tail) == 0 {
		switch r.Method {
		case http.MethodGet:
			items, err := a.store.listAgentSessions(repo)
			if err != nil {
				apiError(w, http.StatusInternalServerError, "cannot list agent sessions")
				return
			}
			writeJSON(w, http.StatusOK, items)
		case http.MethodPost:
			if !u.canWrite(repo) {
				apiError(w, http.StatusForbidden, "write access required")
				return
			}
			var input struct {
				Agent   string `json:"agent"`
				Ref     string `json:"ref"`
				Summary string `json:"summary"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
				apiError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			session, err := a.store.createAgentSession(repo, input.Agent, input.Ref, input.Summary, username)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = a.store.recordAudit(username, "agent_session.create", repo, fmt.Sprint(session.ID), nil)
			writeJSON(w, http.StatusCreated, session)
		default:
			apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	id, err := strconv.Atoi(tail[0])
	if err != nil || id < 1 {
		apiError(w, http.StatusNotFound, "agent session not found")
		return
	}
	if len(tail) == 1 && r.Method == http.MethodGet {
		session, err := a.store.agentSessionByID(repo, id)
		if err != nil {
			apiError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, session)
		return
	}
	if len(tail) == 2 && tail[1] == "checkpoints" && r.Method == http.MethodPost {
		if !u.canWrite(repo) {
			apiError(w, http.StatusForbidden, "write access required")
			return
		}
		var input struct {
			Ref     string `json:"ref"`
			Summary string `json:"summary"`
			State   string `json:"state"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		session, err := a.store.addAgentCheckpoint(repo, id, input.Ref, input.Summary, input.State)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = a.store.recordAudit(username, "agent_session.checkpoint", repo, fmt.Sprint(id), nil)
		writeJSON(w, http.StatusOK, session)
		return
	}
	apiError(w, http.StatusNotFound, "not found")
}
