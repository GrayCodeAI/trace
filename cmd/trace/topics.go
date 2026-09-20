package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const maxTopics = 20

type topicDB struct {
	Repos map[string][]string `json:"repos"`
}

func normalizeTopics(values []string) ([]string, error) {
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		first := value[0]
		if len(value) > 50 || !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
			return nil, fmt.Errorf("invalid topic %q", value)
		}
		for _, c := range value {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
				return nil, fmt.Errorf("invalid topic %q", value)
			}
		}
		seen[value] = true
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) > maxTopics {
		return nil, fmt.Errorf("at most %d topics are allowed", maxTopics)
	}
	return result, nil
}

func (s *store) loadTopics() (topicDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, "topics.json"))
	if os.IsNotExist(err) {
		return topicDB{Repos: map[string][]string{}}, nil
	}
	if err != nil {
		return topicDB{}, err
	}
	var db topicDB
	if err := json.Unmarshal(b, &db); err != nil {
		return topicDB{}, err
	}
	if db.Repos == nil {
		db.Repos = map[string][]string{}
	}
	return db, nil
}

func (s *store) setTopics(repo string, values []string) error {
	if _, err := s.repoPath(repo); err != nil {
		return err
	}
	normalized, err := normalizeTopics(values)
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.root, ".topics.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadTopics()
	if err != nil {
		return err
	}
	if len(normalized) == 0 {
		delete(db.Repos, repo)
	} else {
		db.Repos[repo] = normalized
	}
	b, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".topics-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0600)
	if _, err = tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(s.root, "topics.json"))
}

func (s *store) getTopics(repo string) ([]string, error) {
	db, err := s.loadTopics()
	if err != nil {
		return nil, err
	}
	if db.Repos[repo] == nil {
		return []string{}, nil
	}
	return append([]string{}, db.Repos[repo]...), nil
}
