package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type projectColumn struct {
	Name string `json:"name"`
}
type projectCard struct {
	ID     int    `json:"id"`
	Kind   string `json:"kind"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Column string `json:"column"`
}
type project struct {
	ID          int             `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Columns     []projectColumn `json:"columns"`
	Cards       []projectCard   `json:"cards"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}
type projectDB struct {
	Repos map[string][]project `json:"repos"`
}

func defaultProjectColumns() []projectColumn {
	return []projectColumn{{Name: "Todo"}, {Name: "In progress"}, {Name: "Done"}}
}

func (s *store) loadProjects() (projectDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, "projects.json"))
	if os.IsNotExist(err) {
		return projectDB{Repos: map[string][]project{}}, nil
	}
	if err != nil {
		return projectDB{}, err
	}
	var db projectDB
	if err := json.Unmarshal(b, &db); err != nil {
		return projectDB{}, err
	}
	if db.Repos == nil {
		db.Repos = map[string][]project{}
	}
	return db, nil
}

func (s *store) updateProjects(change func(*projectDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".projects.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadProjects()
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
	tmp, err := os.CreateTemp(s.root, ".projects-")
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
	return os.Rename(name, filepath.Join(s.root, "projects.json"))
}

func projectByID(db *projectDB, repo string, id int) (*project, error) {
	for i := range db.Repos[repo] {
		if db.Repos[repo][i].ID == id {
			return &db.Repos[repo][i], nil
		}
	}
	return nil, fmt.Errorf("project %d not found", id)
}

func (s *store) listProjects(repo string) ([]project, error) {
	db, err := s.loadProjects()
	if err != nil {
		return nil, err
	}
	return append([]project(nil), db.Repos[repo]...), nil
}

func (s *store) createProject(repo, name, description string) (project, error) {
	if _, err := s.repoPath(repo); err != nil {
		return project{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return project{}, fmt.Errorf("project name must be 1-120 characters")
	}
	var created project
	err := s.updateProjects(func(db *projectDB) error {
		projects := db.Repos[repo]
		next := 1
		for _, item := range projects {
			if item.ID >= next {
				next = item.ID + 1
			}
			if strings.EqualFold(item.Name, name) {
				return fmt.Errorf("project already exists")
			}
		}
		now := time.Now().UTC()
		created = project{ID: next, Name: name, Description: strings.TrimSpace(description), Columns: defaultProjectColumns(), Cards: []projectCard{}, CreatedAt: now, UpdatedAt: now}
		db.Repos[repo] = append(projects, created)
		return nil
	})
	return created, err
}

func (s *store) addProjectCard(repo string, projectID int, kind string, number int, title, column string) (projectCard, error) {
	if kind != "issue" && kind != "pull" {
		return projectCard{}, fmt.Errorf("card kind must be issue or pull")
	}
	if number < 1 || strings.TrimSpace(title) == "" {
		return projectCard{}, fmt.Errorf("card number and title are required")
	}
	var created projectCard
	err := s.updateProjects(func(db *projectDB) error {
		p, err := projectByID(db, repo, projectID)
		if err != nil {
			return err
		}
		if column == "" {
			column = p.Columns[0].Name
		}
		valid := false
		for _, c := range p.Columns {
			if c.Name == column {
				valid = true
			}
		}
		if !valid {
			return fmt.Errorf("unknown project column %q", column)
		}
		for i := range p.Cards {
			if p.Cards[i].Kind == kind && p.Cards[i].Number == number {
				return fmt.Errorf("card already exists")
			}
		}
		next := 1
		for _, c := range p.Cards {
			if c.ID >= next {
				next = c.ID + 1
			}
		}
		created = projectCard{ID: next, Kind: kind, Number: number, Title: strings.TrimSpace(title), Column: column}
		p.Cards = append(p.Cards, created)
		p.UpdatedAt = time.Now().UTC()
		return nil
	})
	return created, err
}

func (s *store) moveProjectCard(repo string, projectID, cardID int, column string) (projectCard, error) {
	var moved projectCard
	err := s.updateProjects(func(db *projectDB) error {
		p, err := projectByID(db, repo, projectID)
		if err != nil {
			return err
		}
		valid := false
		for _, c := range p.Columns {
			if c.Name == column {
				valid = true
			}
		}
		if !valid {
			return fmt.Errorf("unknown project column %q", column)
		}
		for i := range p.Cards {
			if p.Cards[i].ID == cardID {
				p.Cards[i].Column = column
				moved = p.Cards[i]
				p.UpdatedAt = time.Now().UTC()
				return nil
			}
		}
		return fmt.Errorf("card %d not found", cardID)
	})
	return moved, err
}
