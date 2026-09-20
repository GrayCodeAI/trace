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

const notificationFile = "notifications.json"

type notification struct {
	ID        int       `json:"id"`
	User      string    `json:"user"`
	Kind      string    `json:"kind"`
	Repo      string    `json:"repo,omitempty"`
	Resource  string    `json:"resource,omitempty"`
	Message   string    `json:"message"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

type notificationDB struct {
	Users map[string][]notification `json:"users"`
}

func (s *store) loadNotifications() (notificationDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, notificationFile))
	if errors.Is(err, os.ErrNotExist) {
		return notificationDB{Users: make(map[string][]notification)}, nil
	}
	if err != nil {
		return notificationDB{}, err
	}
	var db notificationDB
	if err := json.Unmarshal(b, &db); err != nil {
		return notificationDB{}, fmt.Errorf("read notifications: %w", err)
	}
	if db.Users == nil {
		db.Users = make(map[string][]notification)
	}
	return db, nil
}

func (s *store) updateNotifications(change func(*notificationDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".notifications.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadNotifications()
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
	tmp, err := os.CreateTemp(s.root, ".notifications-*")
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
	return os.Rename(name, filepath.Join(s.root, notificationFile))
}

func (s *store) addNotification(user, kind, repo, resource, message string) error {
	if !namePattern.MatchString(user) || strings.TrimSpace(message) == "" || len(message) > 500 {
		return errors.New("invalid notification")
	}
	return s.updateNotifications(func(db *notificationDB) error {
		id := 1
		for _, item := range db.Users[user] {
			if item.ID >= id {
				id = item.ID + 1
			}
		}
		db.Users[user] = append(db.Users[user], notification{ID: id, User: user, Kind: kind, Repo: repo, Resource: resource, Message: message, CreatedAt: time.Now().UTC()})
		return nil
	})
}

func (s *store) listNotifications(user string, unreadOnly bool) ([]notification, error) {
	db, err := s.loadNotifications()
	if err != nil {
		return nil, err
	}
	items := make([]notification, 0, len(db.Users[user]))
	for _, item := range db.Users[user] {
		if !unreadOnly || !item.Read {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	return items, nil
}

func (s *store) markNotificationRead(user string, id int) error {
	return s.updateNotifications(func(db *notificationDB) error {
		for i := range db.Users[user] {
			if db.Users[user][i].ID == id {
				db.Users[user][i].Read = true
				return nil
			}
		}
		return errors.New("notification not found")
	})
}

func notifyUsers(s *store, users []string, kind, repo, resource, message string) {
	seen := make(map[string]bool)
	for _, user := range users {
		if user == "" || seen[user] {
			continue
		}
		seen[user] = true
		_ = s.addNotification(user, kind, repo, resource, message)
	}
}

func notifyWatchers(s *store, repo, actor, kind, resource, message string) {
	users, err := s.listWatches(repo)
	if err != nil {
		return
	}
	for i := range users {
		if users[i] == actor {
			users[i] = ""
		}
	}
	notifyUsers(s, users, kind, repo, resource, message)
}
