package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type rateWindow struct {
	Started time.Time
	Count   int
}

type rateLimiter struct {
	root string
}

type rateDecision struct {
	Allowed   bool
	Limit     int
	Remaining int
	Reset     time.Time
}

func newRateLimiter(root string) *rateLimiter {
	return &rateLimiter{root: root}
}

func requestClientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if strings.TrimSpace(r.RemoteAddr) != "" {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return "local-unknown"
}

func requestRateLimit(r *http.Request) int {
	if r.URL.Path == "/login" && r.Method == http.MethodPost {
		return 20
	}
	return 300
}

func (l *rateLimiter) allow(r *http.Request) rateDecision {
	limit := requestRateLimit(r)
	return l.allowKey(requestClientKey(r)+"\x00http\x00"+r.URL.Path, limit)
}

func (l *rateLimiter) allowSSH(remoteAddr string) rateDecision {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil || host == "" {
		host = strings.TrimSpace(remoteAddr)
	}
	if host == "" {
		host = "local-unknown"
	}
	return l.allowKey("ssh\x00"+host, 60)
}

func (l *rateLimiter) allowKey(key string, limit int) rateDecision {
	now := time.Now().UTC()
	statePath := filepath.Join(l.root, "rate-state.json")
	lock, err := os.OpenFile(filepath.Join(l.root, ".rate-state.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return rateDecision{Allowed: true, Limit: limit, Remaining: limit, Reset: now.Add(time.Minute)}
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		return rateDecision{Allowed: true, Limit: limit, Remaining: limit, Reset: now.Add(time.Minute)}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	windows := make(map[string]rateWindow)
	if b, readErr := os.ReadFile(statePath); readErr == nil {
		_ = json.Unmarshal(b, &windows)
	}
	item, ok := windows[key]
	if !ok || item.Started.After(now) || now.Sub(item.Started) >= time.Minute {
		item = rateWindow{Started: now}
	}
	item.Count++
	windows[key] = item
	if len(windows) > 10000 {
		for oldKey, old := range windows {
			if now.Sub(old.Started) > 2*time.Minute {
				delete(windows, oldKey)
			}
		}
	}
	if b, marshalErr := json.Marshal(windows); marshalErr == nil {
		if tmp, createErr := os.CreateTemp(l.root, ".rate-state-"); createErr == nil {
			name := tmp.Name()
			if tmp.Chmod(0600) == nil {
				_, _ = tmp.Write(append(b, '\n'))
				_ = tmp.Sync()
			}
			_ = tmp.Close()
			_ = os.Rename(name, statePath)
			_ = os.Remove(name)
		}
	}
	remaining := limit - item.Count
	if remaining < 0 {
		remaining = 0
	}
	return rateDecision{Allowed: item.Count <= limit, Limit: limit, Remaining: remaining, Reset: item.Started.Add(time.Minute)}
}

func writeRateHeaders(w http.ResponseWriter, decision rateDecision) {
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(decision.Limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(decision.Reset.Unix(), 10))
}
