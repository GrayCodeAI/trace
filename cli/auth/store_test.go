package auth

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/api"
	"github.com/entireio/auth-go/tokens"
	"github.com/entireio/auth-go/tokenstore"
	"github.com/zalando/go-keyring"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func TestStoreSaveAndGetToken(t *testing.T) {
	// Not parallel: go-keyring's mock provider uses an unprotected map.
	store := NewStoreWithService("test-save-get")

	if err := store.SaveToken("https://trace.io", "prod-token"); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	got, err := store.GetToken("https://trace.io")
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}
	if got != "prod-token" {
		t.Fatalf("GetToken() = %q, want %q", got, "prod-token")
	}
}

func TestStoreGetToken_NotFound(t *testing.T) {
	// Not parallel: go-keyring's mock provider uses an unprotected map.
	store := NewStoreWithService("test-not-found")

	got, err := store.GetToken("https://missing.example.com")
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}
	if got != "" {
		t.Fatalf("GetToken() = %q, want empty string", got)
	}
}

func TestStoreSaveToken_PreservesOtherBaseURLs(t *testing.T) {
	// Not parallel: go-keyring's mock provider uses an unprotected map.
	store := NewStoreWithService("test-preserve")

	if err := store.SaveToken("https://trace.io", "prod-token"); err != nil {
		t.Fatalf("SaveToken(prod) error = %v", err)
	}

	if err := store.SaveToken("http://localhost:8787", "local-token"); err != nil {
		t.Fatalf("SaveToken(local) error = %v", err)
	}

	prod, err := store.GetToken("https://trace.io")
	if err != nil {
		t.Fatalf("GetToken(prod) error = %v", err)
	}
	if prod != "prod-token" {
		t.Fatalf("prod token = %q, want %q", prod, "prod-token")
	}

	local, err := store.GetToken("http://localhost:8787")
	if err != nil {
		t.Fatalf("GetToken(local) error = %v", err)
	}
	if local != "local-token" {
		t.Fatalf("local token = %q, want %q", local, "local-token")
	}
}

func TestStoreSaveToken_RejectsEmptyToken(t *testing.T) {
	// Not parallel: go-keyring's mock provider uses an unprotected map.
	store := NewStoreWithService("test-empty")

	if err := store.SaveToken("https://trace.io", ""); err == nil {
		t.Fatal("SaveToken() with empty token should fail")
	}

	if err := store.SaveToken("https://trace.io", "   "); err == nil {
		t.Fatal("SaveToken() with whitespace token should fail")
	}
}

func TestStoreSaveToken_TrimsWhitespace(t *testing.T) {
	// Not parallel: go-keyring's mock provider uses an unprotected map.
	store := NewStoreWithService("test-trim")

	if err := store.SaveToken("https://trace.io", "  my-token  "); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	got, err := store.GetToken("https://trace.io")
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}
	if got != "my-token" {
		t.Fatalf("GetToken() = %q, want %q (whitespace should be trimmed)", got, "my-token")
	}
}

func TestStoreDeleteToken(t *testing.T) {
	// Not parallel: go-keyring's mock provider uses an unprotected map.
	store := NewStoreWithService("test-delete")

	if err := store.SaveToken("https://trace.io", "tok"); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	if err := store.DeleteToken("https://trace.io"); err != nil {
		t.Fatalf("DeleteToken() error = %v", err)
	}

	got, err := store.GetToken("https://trace.io")
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}
	if got != "" {
		t.Fatalf("GetToken() after delete = %q, want empty", got)
	}
}

func TestStoreDeleteToken_NotFoundIsNoop(t *testing.T) {
	// Not parallel: go-keyring's mock provider uses an unprotected map.
	store := NewStoreWithService("test-delete-noop")

	if err := store.DeleteToken("https://nonexistent.example.com"); err != nil {
		t.Fatalf("DeleteToken() on missing key error = %v", err)
	}
}

func TestLookupCurrentToken(t *testing.T) {
	t.Setenv(api.BaseURLEnvVar, "http://localhost:8787")

	store := NewStore()
	if err := store.SaveToken("http://localhost:8787", "local-token"); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	got, err := LookupCurrentToken()
	if err != nil {
		t.Fatalf("LookupCurrentToken() error = %v", err)
	}
	if got != "local-token" {
		t.Fatalf("LookupCurrentToken() = %q, want %q", got, "local-token")
	}
}

// ---------------------------------------------------------------------------
// SaveTokens / LoadTokens / DeleteTokens (tokenstore.Store interface)
// ---------------------------------------------------------------------------

func TestSaveTokens_LoadTokens_RoundTrip(t *testing.T) {
	store := NewStoreWithService("test-savetokens-roundtrip")

	want := tokens.TokenSet{
		AccessToken:  "at-abc123",
		RefreshToken: "rt-xyz789",
		TokenType:    "Bearer",
		ExpiresAt:    time.Date(2026, 12, 25, 10, 0, 0, 0, time.UTC),
		Scope:        "cli",
	}

	if err := store.SaveTokens("https://trace.io", want); err != nil {
		t.Fatalf("SaveTokens() error = %v", err)
	}

	got, err := store.LoadTokens("https://trace.io")
	if err != nil {
		t.Fatalf("LoadTokens() error = %v", err)
	}

	if got.AccessToken != want.AccessToken {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, want.AccessToken)
	}
	if got.RefreshToken != want.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, want.RefreshToken)
	}
	if got.TokenType != want.TokenType {
		t.Errorf("TokenType = %q, want %q", got.TokenType, want.TokenType)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
	if got.Scope != want.Scope {
		t.Errorf("Scope = %q, want %q", got.Scope, want.Scope)
	}
}

func TestSaveTokens_LoadTokens_MinimalRoundTrip(t *testing.T) {
	// TokenSet with only AccessToken set (no refresh, no expiry).
	store := NewStoreWithService("test-savetokens-minimal")

	want := tokens.TokenSet{AccessToken: "bare-token"}

	if err := store.SaveTokens("https://trace.io", want); err != nil {
		t.Fatalf("SaveTokens() error = %v", err)
	}

	got, err := store.LoadTokens("https://trace.io")
	if err != nil {
		t.Fatalf("LoadTokens() error = %v", err)
	}
	if got.AccessToken != "bare-token" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "bare-token")
	}
	if !got.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v, want zero", got.ExpiresAt)
	}
}

func TestSaveTokens_RejectsEmptyAccessToken(t *testing.T) {
	store := NewStoreWithService("test-savetokens-empty")

	if err := store.SaveTokens("https://trace.io", tokens.TokenSet{}); err == nil {
		t.Fatal("SaveTokens() with zero TokenSet should fail")
	}
}

func TestLoadTokens_NotFound(t *testing.T) {
	store := NewStoreWithService("test-loadtokens-notfound")

	_, err := store.LoadTokens("https://missing.example.com")
	if !errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("LoadTokens() error = %v, want ErrNotFound", err)
	}
}

func TestLoadTokens_LegacyCompat_PlainString(t *testing.T) {
	// When a legacy plain-string token was saved via SaveToken,
	// LoadTokens must wrap it into a TokenSet with AccessToken set.
	store := NewStoreWithService("test-loadtokens-legacy")

	if err := store.SaveToken("https://trace.io", "legacy-tok"); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	got, err := store.LoadTokens("https://trace.io")
	if err != nil {
		t.Fatalf("LoadTokens() error = %v", err)
	}
	if got.AccessToken != "legacy-tok" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "legacy-tok")
	}
	if got.RefreshToken != "" {
		t.Errorf("RefreshToken = %q, want empty (legacy had no refresh)", got.RefreshToken)
	}
}

func TestDeleteTokens(t *testing.T) {
	store := NewStoreWithService("test-deletetokens")

	if err := store.SaveTokens("https://trace.io", tokens.TokenSet{AccessToken: "tok"}); err != nil {
		t.Fatalf("SaveTokens() error = %v", err)
	}

	if err := store.DeleteTokens("https://trace.io"); err != nil {
		t.Fatalf("DeleteTokens() error = %v", err)
	}

	_, err := store.LoadTokens("https://trace.io")
	if !errors.Is(err, tokenstore.ErrNotFound) {
		t.Fatalf("LoadTokens() after delete error = %v, want ErrNotFound", err)
	}
}

func TestDeleteTokens_MissingProfileIsNoop(t *testing.T) {
	store := NewStoreWithService("test-deletetokens-noop")

	if err := store.DeleteTokens("https://nonexistent.example.com"); err != nil {
		t.Fatalf("DeleteTokens() on missing profile error = %v", err)
	}
}

func TestSaveTokens_PreservesOtherProfiles(t *testing.T) {
	store := NewStoreWithService("test-savetokens-preserve")

	if err := store.SaveTokens("https://a.example.com", tokens.TokenSet{AccessToken: "tok-a"}); err != nil {
		t.Fatalf("SaveTokens(a) error = %v", err)
	}
	if err := store.SaveTokens("https://b.example.com", tokens.TokenSet{AccessToken: "tok-b"}); err != nil {
		t.Fatalf("SaveTokens(b) error = %v", err)
	}

	a, err := store.LoadTokens("https://a.example.com")
	if err != nil || a.AccessToken != "tok-a" {
		t.Fatalf("LoadTokens(a) = %q (err %v), want tok-a", a.AccessToken, err)
	}
	b, err := store.LoadTokens("https://b.example.com")
	if err != nil || b.AccessToken != "tok-b" {
		t.Fatalf("LoadTokens(b) = %q (err %v), want tok-b", b.AccessToken, err)
	}
}
