package cli

import (
	"errors"
	"fmt"

	"github.com/GrayCodeAI/trace/cli/api"
	"github.com/GrayCodeAI/trace/cli/auth"
)

// NewAuthenticatedAPIClient creates an API client using the bearer token
// from the CLI login flow. Returns an error if the user is not logged in.
// Pass insecureHTTP=true to allow plain HTTP base URLs (for local development).
func NewAuthenticatedAPIClient(insecureHTTP bool) (*api.Client, error) {
	token, err := auth.LookupCurrentToken()
	if err != nil {
		return nil, fmt.Errorf("lookup auth token: %w", err)
	}
	if token == "" {
		return nil, errors.New("not logged in (run 'trace login' first)")
	}

	if !insecureHTTP {
		if err := api.RequireSecureURL(api.BaseURL()); err != nil {
			return nil, fmt.Errorf("base URL check: %w", err)
		}
	}
	return api.NewClient(token), nil
}
