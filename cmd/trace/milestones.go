package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type milestone struct {
	ID          int       `json:"id"`
	Repo        string    `json:"repo"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	State       string    `json:"state"`
	DueDate     string    `json:"due_date,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *store) createMilestone(repo, title, description, dueDate string) (milestone, error) {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 200 {
		return milestone{}, errors.New("milestone title must be between 1 and 200 characters")
	}
	if len(description) > 20000 {
		return milestone{}, errors.New("milestone description is too long")
	}
	if dueDate != "" {
		if _, err := time.Parse("2006-01-02", dueDate); err != nil {
			return milestone{}, errors.New("due_date must use YYYY-MM-DD")
		}
	}
	var created milestone
	err := s.updateIssues(func(db *issueDB) error {
		id := 1
		for _, item := range db.Milestones[repo] {
			if item.ID >= id {
				id = item.ID + 1
			}
		}
		now := time.Now().UTC()
		created = milestone{ID: id, Repo: repo, Title: title, Description: description, State: "open", DueDate: dueDate, CreatedAt: now, UpdatedAt: now}
		db.Milestones[repo] = append(db.Milestones[repo], created)
		return nil
	})
	return created, err
}

func (s *store) listMilestones(repo, state string) ([]milestone, error) {
	db, err := s.loadIssues()
	if err != nil {
		return nil, err
	}
	items := append([]milestone(nil), db.Milestones[repo]...)
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
	if items == nil {
		items = []milestone{}
	}
	return items, nil
}

func findMilestone(db *issueDB, repo string, id int) (*milestone, error) {
	items := db.Milestones[repo]
	for i := range items {
		if items[i].ID == id {
			return &items[i], nil
		}
	}
	return nil, errors.New("milestone not found")
}

func (s *store) updateMilestone(repo string, id int, title, description, state, dueDate string) (milestone, error) {
	var updated milestone
	err := s.updateIssues(func(db *issueDB) error {
		item, err := findMilestone(db, repo, id)
		if err != nil {
			return err
		}
		if title != "" {
			if len(title) > 200 {
				return errors.New("milestone title is too long")
			}
			item.Title = strings.TrimSpace(title)
		}
		if description != "" {
			if len(description) > 20000 {
				return errors.New("milestone description is too long")
			}
			item.Description = description
		}
		if state != "" {
			if state != "open" && state != "closed" {
				return errors.New("state must be open or closed")
			}
			item.State = state
		}
		if dueDate != "" {
			if _, err := time.Parse("2006-01-02", dueDate); err != nil {
				return errors.New("due_date must use YYYY-MM-DD")
			}
			item.DueDate = dueDate
		}
		item.UpdatedAt = time.Now().UTC()
		updated = *item
		return nil
	})
	return updated, err
}

func (s *store) deleteMilestone(repo string, id int) error {
	return s.updateIssues(func(db *issueDB) error {
		items := db.Milestones[repo]
		for i, item := range items {
			if item.ID == id {
				db.Milestones[repo] = append(items[:i], items[i+1:]...)
				for index := range db.Repos[repo] {
					if db.Repos[repo][index].Milestone == fmt.Sprint(id) {
						db.Repos[repo][index].Milestone = ""
					}
				}
				return nil
			}
		}
		return errors.New("milestone not found")
	})
}

func (s *store) setIssueMilestone(repo string, issueID int, actor string, admin bool, milestoneID string) (issue, error) {
	var updated issue
	err := s.updateIssues(func(db *issueDB) error {
		item, err := findIssue(db, repo, issueID)
		if err != nil {
			return err
		}
		if !admin && item.Author != actor && item.Assignee != actor {
			return errors.New("only the author, assignee, or an admin can update an issue")
		}
		if milestoneID != "" {
			id := 0
			if _, err := fmt.Sscanf(milestoneID, "%d", &id); err != nil || id < 1 {
				return errors.New("invalid milestone")
			}
			milestone, err := findMilestone(db, repo, id)
			if err != nil || milestone.State != "open" {
				return errors.New("milestone not found or closed")
			}
			item.Milestone = milestoneID
		} else {
			item.Milestone = ""
		}
		item.UpdatedAt = time.Now().UTC()
		updated = *item
		return nil
	})
	return updated, err
}
