package main

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func rateRequest(path, remote string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

// TestRateLimitIsPerClientNotPerPath reproduces the evasion: varying the URL
// path used to give each request a fresh bucket.
func TestRateLimitIsPerClientNotPerPath(t *testing.T) {
	root := t.TempDir()
	limiter := newRateLimiter(root)
	for i := 0; i < 300; i++ {
		if d := limiter.allow(rateRequest("/repos/team/p"+strconv.Itoa(i), "203.0.113.20:5000", nil)); !d.Allowed {
			t.Fatalf("request %d blocked early", i+1)
		}
	}
	if d := limiter.allow(rateRequest("/repos/team/another-path", "203.0.113.20:5000", nil)); d.Allowed {
		t.Fatal("varying the path evaded the per-client limit")
	}
	if d := limiter.allow(rateRequest("/app", "203.0.113.21:5000", nil)); !d.Allowed {
		t.Fatal("a different client shared the exhausted bucket")
	}
	if _, err := os.Stat(filepath.Join(root, "rate-state.json")); !os.IsNotExist(err) {
		t.Fatalf("limiter still rewrites rate-state.json per request: %v", err)
	}
}

func TestRateLimitTrustedProxyForwardedFor(t *testing.T) {
	proxy := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	limiter := newRateLimiter(t.TempDir())
	limiter.trustedProxies = proxy
	cases := []struct {
		remote  string
		headers map[string]string
		want    string
	}{
		{"10.0.0.5:1234", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "198.51.100.7"},
		// A client cannot choose its key by prepending addresses: the
		// rightmost address not belonging to a trusted proxy wins.
		{"10.0.0.5:1234", map[string]string{"X-Forwarded-For": "192.0.2.99, 198.51.100.7, 10.0.0.9"}, "198.51.100.7"},
		{"10.0.0.5:1234", map[string]string{"X-Real-IP": "198.51.100.8"}, "198.51.100.8"},
		// Headers from an untrusted peer are ignored.
		{"203.0.113.9:1234", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "203.0.113.9"},
		// IPv6 clients are grouped by /64.
		{"[2001:db8:1:2:aaaa::1]:443", nil, "2001:db8:1:2::/64"},
		{"[2001:db8:1:2:bbbb::9]:443", nil, "2001:db8:1:2::/64"},
		{"[::ffff:198.51.100.10]:443", nil, "198.51.100.10"},
	}
	for _, tc := range cases {
		if got := limiter.clientKey(rateRequest("/", tc.remote, tc.headers)); got != tc.want {
			t.Fatalf("clientKey(%s, %v) = %q, want %q", tc.remote, tc.headers, got, tc.want)
		}
	}
	for i := 0; i < 300; i++ {
		limiter.allow(rateRequest("/app", "10.0.0.5:1", map[string]string{"X-Forwarded-For": "198.51.100.30"}))
	}
	if d := limiter.allow(rateRequest("/app", "10.0.0.5:1", map[string]string{"X-Forwarded-For": "198.51.100.30"})); d.Allowed {
		t.Fatal("client behind the proxy was not limited")
	}
	if d := limiter.allow(rateRequest("/app", "10.0.0.5:1", map[string]string{"X-Forwarded-For": "198.51.100.31"})); !d.Allowed {
		t.Fatal("one busy client behind the proxy throttled everyone")
	}
}

func TestParseTrustedProxies(t *testing.T) {
	got, err := parseTrustedProxies("10.0.0.0/8, 127.0.0.1,::1")
	if err != nil || len(got) != 3 || got[1].Bits() != 32 || got[2].Bits() != 128 {
		t.Fatalf("parseTrustedProxies: %v %v", got, err)
	}
	if _, err := parseTrustedProxies("not-an-ip"); err == nil {
		t.Fatal("invalid proxy accepted")
	}
	if got, err := parseTrustedProxies(""); err != nil || len(got) != 0 {
		t.Fatalf("empty list: %v %v", got, err)
	}
}
