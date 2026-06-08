package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProductionAuthStoreIsKeyringOnly enforces that the file-backed auth
// store backend stays fully gated behind //go:build authfilestore. The
// production CLI binary (no build tag) must not contain code that reads
// the test env var or persists tokens to disk — otherwise an end user
// can flip the variable in their shell and silently bypass the OS
// keyring.
//
// This runs as part of the regular (untagged) test suite, so any new
// production-compiled .go file in this package that mentions the
// forbidden symbols will fail CI immediately. The remediation is to
// move the offending code into a //go:build authfilestore file
// alongside store_filebackend.go.
func TestProductionAuthStoreIsKeyringOnly(t *testing.T) {
	t.Parallel()

	// Symbols that may only appear in authfilestore-tagged files.
	// Keep this list tight — these are the load-bearing markers of
	// "we are persisting auth tokens to a file from production code".
	forbidden := []string{
		"TRACE_TEST_AUTH_STORE_FILE", // the test env-var hook
		"os.WriteFile",               // token-on-disk write
		"os.ReadFile",                // token-on-disk read
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir auth pkg: %v", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// _test.go files are never in the production binary, so they
		// cannot reintroduce the file backend regardless of contents.
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(data)

		if hasAuthFileStoreBuildTag(src) {
			continue
		}

		for _, sym := range forbidden {
			if strings.Contains(src, sym) {
				t.Errorf(
					"%s references %q outside a //go:build authfilestore file. "+
						"File-backed auth storage must stay gated so production "+
						"builds cannot opt into it. Move this code into a tagged "+
						"file (see store_filebackend.go).",
					name, sym,
				)
			}
		}
	}
}

// hasAuthFileStoreBuildTag reports whether the file's build constraint
// requires the authfilestore tag. Build constraints must appear before
// the package clause, so we only scan up to that point.
func hasAuthFileStoreBuildTag(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			return false
		}
		if strings.HasPrefix(trimmed, "//go:build ") &&
			strings.Contains(trimmed, "authfilestore") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// resolveProvider tests
// ---------------------------------------------------------------------------

func TestResolveProvider_V1(t *testing.T) {
	t.Parallel()
	p := resolveProvider("v1")

	if p.ClientID != "trace-cli" {
		t.Errorf("ClientID = %q, want %q", p.ClientID, "trace-cli")
	}
	if p.DeviceCodePath != "/oauth/device/code" {
		t.Errorf("DeviceCodePath = %q, want %q", p.DeviceCodePath, "/oauth/device/code")
	}
	if p.TokenPath != "/oauth/token" {
		t.Errorf("TokenPath = %q, want %q", p.TokenPath, "/oauth/token")
	}
	if p.STSPath != "" {
		t.Errorf("STSPath = %q, want empty (v1 uses same-host shortcut)", p.STSPath)
	}
	if p.AuthTokensPath != "/api/v1/auth/tokens" {
		t.Errorf("AuthTokensPath = %q, want %q", p.AuthTokensPath, "/api/v1/auth/tokens")
	}
}

func TestResolveProvider_V2(t *testing.T) {
	t.Parallel()
	p := resolveProvider("v2")

	if p.ClientID != "trace-cli" {
		t.Errorf("ClientID = %q, want %q", p.ClientID, "trace-cli")
	}
	if p.DeviceCodePath != "/device_authorization" {
		t.Errorf("DeviceCodePath = %q, want %q", p.DeviceCodePath, "/device_authorization")
	}
	if p.STSPath != "/oauth/token" {
		t.Errorf("STSPath = %q, want %q", p.STSPath, "/oauth/token")
	}
}

func TestResolveProvider_DefaultFallsBackToV1(t *testing.T) {
	t.Parallel()
	// Unrecognised / empty version strings should default to v1.
	for _, v := range []string{"", "v3", "unknown", "  ", "V1"} {
		p := resolveProvider(v)
		if p.DeviceCodePath != "/oauth/device/code" {
			t.Errorf("resolveProvider(%q): DeviceCodePath = %q, want v1 path", v, p.DeviceCodePath)
		}
		if p.STSPath != "" {
			t.Errorf("resolveProvider(%q): STSPath = %q, want empty (v1)", v, p.STSPath)
		}
	}
}

func TestResolveProvider_V1V2Differ(t *testing.T) {
	t.Parallel()
	v1 := resolveProvider("v1")
	v2 := resolveProvider("v2")

	if v1.DeviceCodePath == v2.DeviceCodePath {
		t.Error("v1 and v2 should have different DeviceCodePaths")
	}
	if v2.STSPath == "" {
		t.Error("v2 should have a non-empty STSPath")
	}
}

// ---------------------------------------------------------------------------
// isLoopbackHTTP tests
// ---------------------------------------------------------------------------

func TestIsLoopbackHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"localhost http", "http://localhost:8787", true},
		{"localhost no port", "http://localhost", true},
		{"127.0.0.1", "http://127.0.0.1:8787", true},
		{"127.0.0.1 no port", "http://127.0.0.1", true},
		{"ipv6 loopback", "http://[::1]:8787", true},
		{"ipv6 no port", "http://[::1]", true},
		{"https localhost", "https://localhost:8787", false},
		{"https 127.0.0.1", "https://127.0.0.1:8787", false},
		{"http external", "http://example.com:8787", false},
		{"https external", "https://example.com", false},
		{"empty string", "", false},
		{"garbage", "not-a-url", false},
		{"scheme only", "http://", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isLoopbackHTTP(tt.url)
			if got != tt.want {
				t.Errorf("isLoopbackHTTP(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
