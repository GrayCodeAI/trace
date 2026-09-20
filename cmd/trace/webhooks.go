package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const webhookFile = "webhooks.json"
const webhookDeliveryFile = "webhook-deliveries.json"

type webhook struct {
	ID        int       `json:"id"`
	Repo      string    `json:"repo"`
	URL       string    `json:"url"`
	Secret    string    `json:"secret"`
	Events    []string  `json:"events"`
	CreatedAt time.Time `json:"created_at"`
}

type webhookDB struct {
	Repos map[string][]webhook `json:"repos"`
}

type webhookDelivery struct {
	ID           int       `json:"id"`
	WebhookID    int       `json:"webhook_id"`
	Repo         string    `json:"repo"`
	Event        string    `json:"event"`
	Status       string    `json:"status"`
	Attempts     int       `json:"attempts"`
	ResponseCode int       `json:"response_code,omitempty"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
}

type webhookDeliveryDB struct {
	NextID     int               `json:"next_id"`
	Deliveries []webhookDelivery `json:"deliveries"`
}

var webhookEvents = map[string]bool{
	"repo.created":           true,
	"pull_request.created":   true,
	"pull_request.commented": true,
	"pull_request.approved":  true,
	"pull_request.merged":    true,
	"issue.created":          true,
	"issue.commented":        true,
	"issue.updated":          true,
	"webhook.created":        true,
	"webhook.deleted":        true,
}

func (s *store) loadWebhooks() (webhookDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, webhookFile))
	if errors.Is(err, os.ErrNotExist) {
		return webhookDB{Repos: make(map[string][]webhook)}, nil
	}
	if err != nil {
		return webhookDB{}, err
	}
	var db webhookDB
	if err := json.Unmarshal(b, &db); err != nil {
		return webhookDB{}, fmt.Errorf("read webhooks: %w", err)
	}
	if db.Repos == nil {
		db.Repos = make(map[string][]webhook)
	}
	return db, nil
}

func (s *store) updateWebhooks(change func(*webhookDB) error) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".webhooks.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadWebhooks()
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
	tmp, err := os.CreateTemp(s.root, ".webhooks-*")
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
	return os.Rename(name, filepath.Join(s.root, webhookFile))
}

func validWebhookURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && isLoopback(u.Hostname()))
}

func validWebhookEvent(event string) bool { return webhookEvents[event] }

func (s *store) addWebhook(repo, rawURL, secret string, events []string) (webhook, error) {
	path, err := s.repoPath(repo)
	if err != nil {
		return webhook{}, err
	}
	if _, err := os.Stat(path); err != nil {
		return webhook{}, errors.New("repository not found")
	}
	if !validWebhookURL(rawURL) {
		return webhook{}, errors.New("webhook URL must use HTTPS (HTTP is allowed only on loopback)")
	}
	if len(secret) < 16 || len(secret) > 256 {
		return webhook{}, errors.New("webhook secret must be between 16 and 256 characters")
	}
	if len(events) == 0 || len(events) > len(webhookEvents) {
		return webhook{}, errors.New("at least one webhook event is required")
	}
	seen := make(map[string]bool)
	cleanEvents := make([]string, 0, len(events))
	for _, event := range events {
		if !validWebhookEvent(event) {
			return webhook{}, fmt.Errorf("unsupported webhook event %q", event)
		}
		if !seen[event] {
			seen[event] = true
			cleanEvents = append(cleanEvents, event)
		}
	}
	var created webhook
	err = s.updateWebhooks(func(db *webhookDB) error {
		id := 1
		for _, existing := range db.Repos[repo] {
			if existing.ID >= id {
				id = existing.ID + 1
			}
		}
		created = webhook{ID: id, Repo: repo, URL: rawURL, Secret: secret, Events: cleanEvents, CreatedAt: time.Now().UTC()}
		db.Repos[repo] = append(db.Repos[repo], created)
		return nil
	})
	return created, err
}

func (s *store) listWebhooks(repo string) ([]webhook, error) {
	db, err := s.loadWebhooks()
	if err != nil {
		return nil, err
	}
	items := append([]webhook(nil), db.Repos[repo]...)
	for i := range items {
		items[i].Secret = ""
	}
	if items == nil {
		items = []webhook{}
	}
	return items, nil
}

func (s *store) removeWebhook(repo string, id int) error {
	return s.updateWebhooks(func(db *webhookDB) error {
		items := db.Repos[repo]
		for i, item := range items {
			if item.ID == id {
				db.Repos[repo] = append(items[:i], items[i+1:]...)
				return nil
			}
		}
		return errors.New("webhook not found")
	})
}

func (s *store) loadWebhookDeliveries() (webhookDeliveryDB, error) {
	b, err := os.ReadFile(filepath.Join(s.root, webhookDeliveryFile))
	if errors.Is(err, os.ErrNotExist) {
		return webhookDeliveryDB{NextID: 1, Deliveries: []webhookDelivery{}}, nil
	}
	if err != nil {
		return webhookDeliveryDB{}, err
	}
	var db webhookDeliveryDB
	if err := json.Unmarshal(b, &db); err != nil {
		return webhookDeliveryDB{}, fmt.Errorf("read webhook deliveries: %w", err)
	}
	if db.NextID < 1 {
		db.NextID = 1
	}
	if db.Deliveries == nil {
		db.Deliveries = []webhookDelivery{}
	}
	return db, nil
}

func (s *store) saveWebhookDeliveries(db webhookDeliveryDB) error {
	b, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".webhook-deliveries-*")
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
	return os.Rename(name, filepath.Join(s.root, webhookDeliveryFile))
}

func (s *store) recordWebhookDelivery(delivery webhookDelivery) error {
	lock, err := os.OpenFile(filepath.Join(s.root, ".webhook-deliveries.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.loadWebhookDeliveries()
	if err != nil {
		return err
	}
	if delivery.ID == 0 {
		delivery.ID = db.NextID
		db.NextID++
	}
	db.Deliveries = append(db.Deliveries, delivery)
	if len(db.Deliveries) > 10000 {
		db.Deliveries = db.Deliveries[len(db.Deliveries)-10000:]
	}
	return s.saveWebhookDeliveries(db)
}

func (s *store) listWebhookDeliveries(repo string, webhookID int) ([]webhookDelivery, error) {
	db, err := s.loadWebhookDeliveries()
	if err != nil {
		return nil, err
	}
	out := make([]webhookDelivery, 0)
	for _, item := range db.Deliveries {
		if item.Repo == repo && (webhookID == 0 || item.WebhookID == webhookID) {
			out = append(out, item)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *store) dispatchWebhook(event auditEvent) {
	if event.Repo == "" {
		return
	}
	db, err := s.loadWebhooks()
	if err != nil {
		return
	}
	eventName := webhookEventName(event.Action)
	payload, err := json.Marshal(map[string]any{"event": eventName, "id": event.ID, "at": event.At, "actor": event.Actor, "repo": event.Repo, "resource": event.Resource, "metadata": event.Metadata})
	if err != nil {
		return
	}
	for _, hook := range db.Repos[event.Repo] {
		if !containsString(hook.Events, eventName) {
			continue
		}
		mac := hmac.New(sha256.New, []byte(hook.Secret))
		_, _ = mac.Write(payload)
		req, err := http.NewRequest(http.MethodPost, hook.URL, bytes.NewReader(payload))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Trace-Event", eventName)
		req.Header.Set("X-Trace-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		status, attempts, deliveryErr := deliverWebhook(req, clientForWebhook())
		delivery := webhookDelivery{WebhookID: hook.ID, Repo: event.Repo, Event: eventName, Attempts: attempts, ResponseCode: status, CreatedAt: time.Now().UTC(), CompletedAt: time.Now().UTC()}
		if deliveryErr == nil {
			delivery.Status = "delivered"
		} else {
			delivery.Status = "failed"
			delivery.Error = deliveryErr.Error()
		}
		_ = s.recordWebhookDelivery(delivery)
	}
}

func clientForWebhook() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

func deliverWebhook(req *http.Request, client *http.Client) (int, int, error) {
	var status int
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt-1) * 100 * time.Millisecond)
		}
		attemptReq := req
		if attempt > 1 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				lastErr = err
				continue
			}
			attemptReq = req.Clone(req.Context())
			attemptReq.Body = body
		}
		resp, err := client.Do(attemptReq)
		if err != nil {
			lastErr = err
			continue
		}
		status = resp.StatusCode
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if status >= 200 && status < 300 {
			return status, attempt, nil
		}
		lastErr = fmt.Errorf("webhook returned HTTP %d", status)
	}
	return status, 3, lastErr
}

func webhookEventName(action string) string {
	parts := strings.Split(action, ".")
	if len(parts) != 2 {
		return action
	}
	suffix := map[string]string{"create": "created", "comment": "commented", "approve": "approved", "merge": "merged", "update": "updated", "close": "closed"}[parts[1]]
	if suffix == "" {
		return action
	}
	return parts[0] + "." + suffix
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
