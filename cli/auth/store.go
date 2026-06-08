package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/GrayCodeAI/trace/cli/api"
	"github.com/entireio/auth-go/tokens"
	"github.com/entireio/auth-go/tokenstore"
	"github.com/zalando/go-keyring"
)

const keyringService = "trace-cli"

// Store manages CLI authentication tokens in the OS keyring.
// Implements tokenstore.Store for use with the tokenmanager library.
type Store struct {
	service string
}

// NewStore returns a Store backed by the system keyring.
func NewStore() *Store {
	return &Store{service: keyringService}
}

// NewStoreWithService returns a Store with a custom keyring service name (for testing).
func NewStoreWithService(service string) *Store {
	return &Store{service: service}
}

// SaveToken persists an access token for the given base URL.
// Legacy method for backward compatibility with plain-string tokens.
func (s *Store) SaveToken(baseURL, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("refusing to save empty token")
	}

	if err := keyring.Set(s.service, baseURL, token); err != nil {
		return fmt.Errorf("save token to keyring: %w", err)
	}

	return nil
}

// GetToken retrieves a stored token for the given base URL.
// Returns an empty string (and no error) if no token is stored.
// Legacy method for backward compatibility with plain-string tokens.
func (s *Store) GetToken(baseURL string) (string, error) {
	token, err := keyring.Get(s.service, baseURL)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get token from keyring: %w", err)
	}

	return token, nil
}

// DeleteToken removes a stored token for the given base URL.
// Legacy method for backward compatibility with plain-string tokens.
func (s *Store) DeleteToken(baseURL string) error {
	err := keyring.Delete(s.service, baseURL)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete token from keyring: %w", err)
	}

	return nil
}

// LookupCurrentToken retrieves the token for the current base URL.
func LookupCurrentToken() (string, error) {
	store := NewStore()
	return store.GetToken(api.BaseURL())
}

// SaveTokens persists a TokenSet for the given profile (typically a base URL).
// Implements tokenstore.Store. The TokenSet is stored as JSON in the keyring.
func (s *Store) SaveTokens(profile string, t tokens.TokenSet) error {
	if t.AccessToken == "" {
		return errors.New("refusing to save empty access token")
	}

	data, err := json.Marshal(t) //nolint:gosec // TokenSet is stored in OS keyring, not logged
	if err != nil {
		return fmt.Errorf("marshal token set: %w", err)
	}

	if err := keyring.Set(s.service, profile, string(data)); err != nil {
		return fmt.Errorf("save tokens to keyring: %w", err)
	}

	return nil
}

// LoadTokens retrieves a stored TokenSet for the given profile.
// Returns tokenstore.ErrNotFound if no token is stored.
// Handles legacy plain-string entries by wrapping them in a TokenSet.
// Implements tokenstore.Store.
func (s *Store) LoadTokens(profile string) (tokens.TokenSet, error) {
	raw, err := keyring.Get(s.service, profile)
	if errors.Is(err, keyring.ErrNotFound) {
		return tokens.TokenSet{}, tokenstore.ErrNotFound
	}
	if err != nil {
		return tokens.TokenSet{}, fmt.Errorf("load tokens from keyring: %w", err)
	}

	// Try to parse as JSON TokenSet first
	var ts tokens.TokenSet
	if err := json.Unmarshal([]byte(raw), &ts); err == nil && ts.AccessToken != "" {
		return ts, nil
	}

	// Legacy plain-string token — wrap in TokenSet for backward compat
	return tokens.TokenSet{AccessToken: raw}, nil
}

// DeleteTokens removes a stored TokenSet for the given profile.
// Treats missing profiles as a no-op. Implements tokenstore.Store.
func (s *Store) DeleteTokens(profile string) error {
	err := keyring.Delete(s.service, profile)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete tokens from keyring: %w", err)
	}

	return nil
}
