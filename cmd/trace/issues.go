package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const issueFile = "issues.json"

var issueLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,49}$`)

type issue struct {
	ID        int            `json:"id"`
	Repo      string         `json:"repo"`
	Title     string         `json:"title"`
	Body      string         `json:"body,omitempty"`
	Author    string         `json:"author"`
	Assignee  string         `json:"assignee,omitempty"`
	Milestone string         `json:"milestone,omitempty"`
	Labels    []string       `json:"labels,omitempty"`
	State     string         `json:"state"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Comments  []issueComment `json:"comments,omitempty"`
}

type issueComment struct {
	ID        int       `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type issueDB struct {
	Repos      map[string][]issue     `json:"repos"`
	Milestones map[string][]milestone `json:"milestones"`
}

func (s *store) loadIssues() (issueDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, issueFile))
	if errors.Is(err, os.ErrNotExist) {
		return issueDB{Repos: make(map[string][]issue), Milestones: make(map[string][]milestone)}, nil
	}
	if err != nil {
		return issueDB{}, err
	}
	var db issueDB
	if err := json.Unmarshal(b, &db); err != nil {
		return issueDB{}, fmt.Errorf("read issues: %w", err)
	}
	if db.Repos == nil {
		db.Repos = make(map[string][]issue)
	}
	if db.Milestones == nil {
		db.Milestones = make(map[string][]milestone)
	}
	return db, nil
}

func (s *store) updateIssues(change func(*issueDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".issues.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadIssues()
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
	tmp, err := os.CreateTemp(s.root, ".issues-*")
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
	return os.Rename(name, filepath.Join(s.root, issueFile))
}

func validateIssueLabels(labels []string) ([]string, error) {
	if len(labels) > 20 {
		return nil, errors.New("at most 20 labels are allowed")
	}
	seen := make(map[string]bool, len(labels))
	clean := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" || !issueLabelPattern.MatchString(label) {
			return nil, errors.New("invalid label")
		}
		if !seen[label] {
			seen[label] = true
			clean = append(clean, label)
		}
	}
	sort.Strings(clean)
	return clean, nil
}

func (s *store) createIssue(repo, title, body, author, assignee string, labels []string) (issue, error) {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 200 {
		return issue{}, errors.New("title must be between 1 and 200 characters")
	}
	if len(body) > 20000 {
		return issue{}, errors.New("body is too long")
	}
	cleanLabels, err := validateIssueLabels(labels)
	if err != nil {
		return issue{}, err
	}
	if len(assignee) > 63 || strings.ContainsAny(assignee, " /\\") {
		return issue{}, errors.New("invalid assignee")
	}
	var created issue
	err = s.updateIssues(func(db *issueDB) error {
		id := 1
		for _, existing := range db.Repos[repo] {
			if existing.ID >= id {
				id = existing.ID + 1
			}
		}
		now := time.Now().UTC()
		created = issue{ID: id, Repo: repo, Title: title, Body: body, Author: author, Assignee: assignee, Labels: cleanLabels, State: "open", CreatedAt: now, UpdatedAt: now}
		db.Repos[repo] = append(db.Repos[repo], created)
		return nil
	})
	return created, err
}

func findIssue(db *issueDB, repo string, id int) (*issue, error) {
	items := db.Repos[repo]
	for i := range items {
		if items[i].ID == id {
			return &items[i], nil
		}
	}
	return nil, errors.New("issue not found")
}

func (s *store) listIssues(repo, state string) ([]issue, error) {
	db, err := s.loadIssues()
	if err != nil {
		return nil, err
	}
	items := make([]issue, 0, len(db.Repos[repo]))
	items = append(items, db.Repos[repo]...)
	if state != "" && state != "all" {
		filtered := items[:0]
		for _, item := range items {
			if item.State == state {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	return items, nil
}

func (s *store) updateIssue(repo string, id int, actor string, admin bool, title, body, assignee, state string, labels []string) (issue, error) {
	var updated issue
	err := s.updateIssues(func(db *issueDB) error {
		item, err := findIssue(db, repo, id)
		if err != nil {
			return err
		}
		if !admin && item.Author != actor && item.Assignee != actor {
			return errors.New("only the author, assignee, or an admin can update an issue")
		}
		if title != "" {
			if len(title) > 200 {
				return errors.New("title is too long")
			}
			item.Title = strings.TrimSpace(title)
		}
		if body != "" {
			if len(body) > 20000 {
				return errors.New("body is too long")
			}
			item.Body = body
		}
		if assignee != "" {
			if len(assignee) > 63 || strings.ContainsAny(assignee, " /\\") {
				return errors.New("invalid assignee")
			}
			item.Assignee = assignee
		}
		if state != "" {
			if state != "open" && state != "closed" {
				return errors.New("state must be open or closed")
			}
			item.State = state
		}
		if labels != nil {
			clean, err := validateIssueLabels(labels)
			if err != nil {
				return err
			}
			item.Labels = clean
		}
		item.UpdatedAt = time.Now().UTC()
		updated = *item
		return nil
	})
	return updated, err
}

func (s *store) addIssueComment(repo string, id int, author, body string) (issue, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 20000 {
		return issue{}, errors.New("comment must be between 1 and 20000 characters")
	}
	var updated issue
	err := s.updateIssues(func(db *issueDB) error {
		item, err := findIssue(db, repo, id)
		if err != nil {
			return err
		}
		commentID := 1
		for _, comment := range item.Comments {
			if comment.ID >= commentID {
				commentID = comment.ID + 1
			}
		}
		item.Comments = append(item.Comments, issueComment{ID: commentID, Author: author, Body: body, CreatedAt: time.Now().UTC()})
		item.UpdatedAt = time.Now().UTC()
		updated = *item
		return nil
	})
	return updated, err
}
