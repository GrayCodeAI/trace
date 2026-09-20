package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSignedWebhookDelivery(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.createRepo("team/demo", false); err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"
	received := make(chan struct {
		body  string
		sig   string
		event string
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- struct {
			body  string
			sig   string
			event string
		}{string(body), r.Header.Get("X-Trace-Signature"), r.Header.Get("X-Trace-Event")}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if _, err := s.addWebhook("team/demo", server.URL, secret, []string{"issue.created"}); err != nil {
		t.Fatal(err)
	}
	event := auditEvent{ID: "event-1", At: time.Now().UTC(), Actor: "alice", Action: "issue.create", Repo: "team/demo", Resource: "1"}
	s.dispatchWebhook(event)
	select {
	case got := <-received:
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(got.body))
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if got.sig != want || got.event != "issue.created" || !strings.Contains(got.body, `"repo":"team/demo"`) {
			t.Fatalf("unexpected webhook: %+v want signature %s", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook was not delivered")
	}
	items, err := s.listWebhooks("team/demo")
	if err != nil || len(items) != 1 || items[0].Secret != "" {
		t.Fatalf("webhook secret was exposed: %+v %v", items, err)
	}
	deliveries, err := s.listWebhookDeliveries("team/demo", items[0].ID)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "delivered" || deliveries[0].Attempts != 1 {
		t.Fatalf("delivery history: %+v %v", deliveries, err)
	}
}

func TestWebhookRetriesAndRecordsFailure(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.createRepo("team/demo", false); err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	hook, err := s.addWebhook("team/demo", server.URL, secret, []string{"issue.created"})
	if err != nil {
		t.Fatal(err)
	}
	s.dispatchWebhook(auditEvent{ID: "event-2", At: time.Now().UTC(), Actor: "alice", Action: "issue.create", Repo: "team/demo", Resource: "2"})
	if calls.Load() != 3 {
		t.Fatalf("expected three delivery attempts, got %d", calls.Load())
	}
	deliveries, err := s.listWebhookDeliveries("team/demo", hook.ID)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "delivered" || deliveries[0].Attempts != 3 {
		t.Fatalf("retry history: %+v %v", deliveries, err)
	}
}
