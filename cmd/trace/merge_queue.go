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

const mergeQueueFile = "merge-queue.json"

type mergeQueueEntry struct {
	ID            int       `json:"id"`
	Repo          string    `json:"repo"`
	PullRequestID int       `json:"pull_request_id"`
	Strategy      string    `json:"strategy"`
	State         string    `json:"state"`
	EnqueuedBy    string    `json:"enqueued_by"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type mergeQueueDB struct {
	Entries []mergeQueueEntry `json:"entries"`
}

func (s *store) loadMergeQueue() (mergeQueueDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, mergeQueueFile))
	if errors.Is(err, os.ErrNotExist) {
		return mergeQueueDB{Entries: []mergeQueueEntry{}}, nil
	}
	if err != nil {
		return mergeQueueDB{}, err
	}
	var db mergeQueueDB
	if err := json.Unmarshal(b, &db); err != nil {
		return mergeQueueDB{}, fmt.Errorf("read merge queue: %w", err)
	}
	return db, nil
}

func (s *store) updateMergeQueue(change func(*mergeQueueDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".merge-queue.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadMergeQueue()
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
	tmp, err := os.CreateTemp(s.root, ".merge-queue-")
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
	return os.Rename(name, filepath.Join(s.root, mergeQueueFile))
}

func (s *store) enqueueMerge(repo string, prID int, strategy, user string) (mergeQueueEntry, error) {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		strategy = "ff"
	}
	if strategy != "ff" && strategy != "squash" && strategy != "merge" {
		return mergeQueueEntry{}, errors.New("merge strategy must be ff, squash, or merge")
	}
	db, err := s.loadPullRequests()
	if err != nil {
		return mergeQueueEntry{}, err
	}
	pr, err := findPullRequest(&db, repo, prID)
	if err != nil {
		return mergeQueueEntry{}, err
	}
	if pr.State != "open" {
		return mergeQueueEntry{}, errors.New("pull request is not open")
	}
	var created mergeQueueEntry
	err = s.updateMergeQueue(func(queue *mergeQueueDB) error {
		for _, entry := range queue.Entries {
			if entry.Repo == repo && entry.PullRequestID == prID && (entry.State == "queued" || entry.State == "processing") {
				return errors.New("pull request is already in the merge queue")
			}
		}
		id := 1
		for _, entry := range queue.Entries {
			if entry.ID >= id {
				id = entry.ID + 1
			}
		}
		now := time.Now().UTC()
		created = mergeQueueEntry{ID: id, Repo: repo, PullRequestID: prID, Strategy: strategy, State: "queued", EnqueuedBy: user, CreatedAt: now, UpdatedAt: now}
		queue.Entries = append(queue.Entries, created)
		return nil
	})
	return created, err
}

func (s *store) listMergeQueue(repo string) ([]mergeQueueEntry, error) {
	db, err := s.loadMergeQueue()
	if err != nil {
		return nil, err
	}
	entries := make([]mergeQueueEntry, 0)
	for _, entry := range db.Entries {
		if repo == "" || entry.Repo == repo {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt.Before(entries[j].CreatedAt) })
	return entries, nil
}

func (s *store) processMergeQueue(repo, user string) (mergeQueueEntry, error) {
	var entry mergeQueueEntry
	err := s.updateMergeQueue(func(queue *mergeQueueDB) error {
		for i := range queue.Entries {
			if queue.Entries[i].Repo == repo && queue.Entries[i].State == "queued" {
				queue.Entries[i].State = "processing"
				queue.Entries[i].UpdatedAt = time.Now().UTC()
				entry = queue.Entries[i]
				return nil
			}
		}
		return errors.New("merge queue is empty")
	})
	if err != nil {
		return mergeQueueEntry{}, err
	}
	_, mergeErr := s.mergePullRequestWithStrategy(repo, entry.PullRequestID, user, entry.Strategy)
	_ = s.updateMergeQueue(func(queue *mergeQueueDB) error {
		for i := range queue.Entries {
			if queue.Entries[i].ID == entry.ID {
				queue.Entries[i].UpdatedAt = time.Now().UTC()
				if mergeErr != nil {
					queue.Entries[i].State = "blocked"
					queue.Entries[i].Error = mergeErr.Error()
				} else {
					queue.Entries[i].State = "merged"
					queue.Entries[i].Error = ""
				}
				entry = queue.Entries[i]
				return nil
			}
		}
		return nil
	})
	if mergeErr != nil {
		return entry, mergeErr
	}
	return entry, nil
}
