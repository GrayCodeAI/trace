package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type commitStatus struct {
	Repo        string    `json:"repo"`
	Context     string    `json:"context"`
	State       string    `json:"state"`
	TargetURL   string    `json:"target_url,omitempty"`
	Description string    `json:"description,omitempty"`
	Creator     string    `json:"creator"`
	Commit      string    `json:"commit"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type statusDB struct {
	Statuses []commitStatus `json:"statuses"`
}

func (s *store) loadStatuses() (statusDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, "statuses.json"))
	if errors.Is(err, os.ErrNotExist) {
		return statusDB{Statuses: []commitStatus{}}, nil
	}
	if err != nil {
		return statusDB{}, err
	}
	var db statusDB
	if err := json.Unmarshal(b, &db); err != nil {
		return statusDB{}, fmt.Errorf("read statuses: %w", err)
	}
	return db, nil
}

func (s *store) updateStatuses(change func(*statusDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".statuses.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadStatuses()
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
	tmp, err := os.CreateTemp(s.root, ".statuses-")
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
	return os.Rename(name, filepath.Join(s.root, "statuses.json"))
}

func validStatusState(state string) bool {
	switch state {
	case "error", "failure", "pending", "success":
		return true
	default:
		return false
	}
}

func (s *store) setCommitStatus(repo, commit string, status commitStatus) error {
	path, err := s.repoPath(repo)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return errors.New("repository not found")
	}
	if strings.TrimSpace(commit) == "" || !validStatusState(status.State) || strings.TrimSpace(status.Context) == "" {
		return errors.New("context, commit, and valid state are required")
	}
	if _, _, err := gitOutput(path, 4096, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return errors.New("commit not found")
	}
	now := time.Now().UTC()
	status.Repo, status.Commit, status.CreatedAt, status.UpdatedAt = repo, commit, now, now
	return s.updateStatuses(func(db *statusDB) error {
		for i := range db.Statuses {
			if db.Statuses[i].Repo == repo && db.Statuses[i].Commit == commit && db.Statuses[i].Context == status.Context {
				status.CreatedAt = db.Statuses[i].CreatedAt
				db.Statuses[i] = status
				return nil
			}
		}
		db.Statuses = append(db.Statuses, status)
		return nil
	})
}

func (s *store) listCommitStatuses(repo, commit string) ([]commitStatus, error) {
	db, err := s.loadStatuses()
	if err != nil {
		return nil, err
	}
	out := make([]commitStatus, 0)
	for _, status := range db.Statuses {
		if status.Repo == repo && status.Commit == commit {
			out = append(out, status)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}
