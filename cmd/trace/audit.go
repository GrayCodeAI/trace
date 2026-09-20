package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const auditFile = "audit.jsonl"

type auditEvent struct {
	ID       string         `json:"id"`
	At       time.Time      `json:"at"`
	Actor    string         `json:"actor"`
	Action   string         `json:"action"`
	Repo     string         `json:"repo,omitempty"`
	Resource string         `json:"resource,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func (s *store) recordAudit(actor, action, repo, resource string, metadata map[string]any) error {
	if strings.TrimSpace(actor) == "" {
		actor = "system"
	}
	event := auditEvent{ID: newAuditID(), At: time.Now().UTC(), Actor: actor, Action: action, Repo: repo, Resource: resource, Metadata: metadata}
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.root, ".audit.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	f, err := os.OpenFile(filepath.Join(s.root, auditFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	go s.dispatchWebhook(event)
	return nil
}

func newAuditID() string {
	return time.Now().UTC().Format("20060102T150405.000000000Z")
}

func (s *store) listAudit(limit int) ([]auditEvent, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("limit must be between 1 and 1000")
	}
	f, err := os.Open(filepath.Join(s.root, auditFile))
	if errors.Is(err, os.ErrNotExist) {
		return []auditEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []auditEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	for scanner.Scan() {
		var event auditEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.After(events[j].At) })
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}
