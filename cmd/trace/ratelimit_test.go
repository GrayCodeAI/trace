package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestRateLimitReturnsHeadersAnd429(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
		req.RemoteAddr = "203.0.113.9:4242"
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("request %d: got %d", i+1, res.Code)
		}
		if res.Header().Get("X-RateLimit-Limit") != "300" {
			t.Fatalf("request %d missing rate headers", i+1)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
	req.RemoteAddr = "203.0.113.9:4242"
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests || res.Header().Get("Retry-After") == "" {
		t.Fatalf("expected rate limit response, got %d headers=%v", res.Code, res.Header())
	}
}

func TestRateLimitSharedAcrossAppInstances(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 150; i++ {
		for _, app := range []*app{a, b} {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
			req.RemoteAddr = "198.51.100.7:4242"
			res := httptest.NewRecorder()
			app.ServeHTTP(res, req)
			if res.Code != http.StatusUnauthorized {
				t.Fatalf("request %d: got %d", i, res.Code)
			}
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
	req.RemoteAddr = "198.51.100.7:4242"
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("shared limiter did not count across instances: %d", res.Code)
	}
}

func TestSSHRateLimitSharedByRemoteHost(t *testing.T) {
	root := t.TempDir()
	limiter := newRateLimiter(root)
	for i := 0; i < 60; i++ {
		if decision := limiter.allowSSH("192.0.2.10:2200"); !decision.Allowed {
			t.Fatalf("SSH request %d unexpectedly blocked", i+1)
		}
	}
	if decision := limiter.allowSSH("192.0.2.10:2200"); decision.Allowed {
		t.Fatal("61st SSH connection from one host was not throttled")
	}
	if decision := limiter.allowSSH("192.0.2.11:2200"); !decision.Allowed {
		t.Fatal("different SSH host was incorrectly throttled")
	}
}
