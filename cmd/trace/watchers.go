package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

type watchDB struct {
	Repos map[string]map[string]bool `json:"repos"`
}

func (s *store) loadWatches() (watchDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, "watchers.json"))
	if errors.Is(err, os.ErrNotExist) {
		return watchDB{Repos: map[string]map[string]bool{}}, nil
	}
	if err != nil {
		return watchDB{}, err
	}
	var db watchDB
	if err := json.Unmarshal(b, &db); err != nil {
		return watchDB{}, err
	}
	if db.Repos == nil {
		db.Repos = map[string]map[string]bool{}
	}
	return db, nil
}

func (s *store) updateWatches(change func(*watchDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".watchers.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadWatches()
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
	tmp, err := os.CreateTemp(s.root, ".watchers-")
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
	return os.Rename(name, filepath.Join(s.root, "watchers.json"))
}

func (s *store) setWatch(repo, user string, watching bool) error {
	if _, err := s.repoPath(repo); err != nil {
		return err
	}
	return s.updateWatches(func(db *watchDB) error {
		members := db.Repos[repo]
		if members == nil {
			members = map[string]bool{}
		}
		if watching {
			members[user] = true
		} else {
			delete(members, user)
		}
		if len(members) == 0 {
			delete(db.Repos, repo)
		} else {
			db.Repos[repo] = members
		}
		return nil
	})
}

func (s *store) listWatches(repo string) ([]string, error) {
	db, err := s.loadWatches()
	if err != nil {
		return nil, err
	}
	users := make([]string, 0, len(db.Repos[repo]))
	for user := range db.Repos[repo] {
		users = append(users, user)
	}
	sort.Strings(users)
	return users, nil
}
