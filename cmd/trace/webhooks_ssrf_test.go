package main

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookDestinationRules(t *testing.T) {
	cases := []struct {
		addr                        string
		allowLoopback, allowPrivate bool
		want                        bool
	}{
		{"93.184.216.34", false, false, true},
		{"2606:2800:220:1:248:1893:25c8:1946", false, false, true},
		{"127.0.0.1", false, false, false},
		{"127.0.0.1", true, false, true},
		{"::1", false, false, false},
		{"10.1.2.3", false, false, false},
		{"10.1.2.3", false, true, true},
		{"172.16.0.1", false, false, false},
		{"192.168.1.1", false, false, false},
		{"100.64.0.1", false, false, false},
		{"fd00::1", false, false, false},
		{"169.254.169.254", false, false, false},
		{"169.254.169.254", true, true, false},
		{"fe80::1", false, true, false},
		{"0.0.0.0", false, true, false},
		{"224.0.0.1", false, true, false},
		{"255.255.255.255", false, true, false},
		{"64:ff9b::a00:1", false, false, false},
		{"::ffff:10.0.0.1", false, false, false},
	}
	for _, tc := range cases {
		if got := webhookAddressAllowed(netip.MustParseAddr(tc.addr), tc.allowLoopback, tc.allowPrivate); got != tc.want {
			t.Fatalf("webhookAddressAllowed(%s, loopback=%v, private=%v) = %v, want %v", tc.addr, tc.allowLoopback, tc.allowPrivate, got, tc.want)
		}
	}
}

// TestWebhookDeliveryDoesNotReachInternalTargets checks the delivery client:
// it refuses to connect to non-public addresses for non-loopback hooks
// (checked at connect time, after DNS), ignores environment proxies, and does
// not follow redirects.
func TestWebhookDeliveryDoesNotReachInternalTargets(t *testing.T) {
	var internalHits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internalHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer internal.Close()

	// A hook configured with a public hostname must not be able to reach a
	// loopback listener, whatever the name resolves to.
	client := webhookHTTPClient(false, false)
	if _, err := client.Post(internal.URL, "application/json", strings.NewReader("{}")); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("delivery to a loopback address was not refused: %v", err)
	}
	if internalHits.Load() != 0 {
		t.Fatal("internal listener was reached")
	}

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
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/metadata", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	t.Setenv("HTTP_PROXY", internal.URL)
	t.Setenv("HTTPS_PROXY", internal.URL)
	if _, err := s.addWebhook("team/demo", redirector.URL, "0123456789abcdef0123456789abcdef", []string{"issue.created"}); err != nil {
		t.Fatal(err)
	}
	s.dispatchWebhook(auditEvent{ID: "e1", At: time.Now().UTC(), Actor: "alice", Action: "issue.create", Repo: "team/demo", Resource: "1"})
	if internalHits.Load() != 0 {
		t.Fatalf("webhook delivery followed a redirect or used an environment proxy (%d internal hits)", internalHits.Load())
	}
	db, err := s.loadWebhookDeliveries()
	if err != nil || len(db.Deliveries) != 1 || db.Deliveries[0].Status != "failed" || db.Deliveries[0].ResponseCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect should be recorded as a failed delivery: %+v %v", db.Deliveries, err)
	}
}
