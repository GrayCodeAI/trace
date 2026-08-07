package cli

import (
	"context"
	"io"

	"github.com/GrayCodeAI/trace/cli/api"
)

// apiFlags holds flags for the api command.
type apiFlags struct {
	insecureHTTP bool
	jurisdiction string
	to           string
}

// runAPI runs the API command.
func runAPI(ctx context.Context, w, errW io.Writer, rawPath string, f *apiFlags, toExplicit bool) error {
	return nil
}

// resolveAPIClient resolves the API client.
func resolveAPIClient(ctx context.Context, to, jurisdiction string, insecure bool) (*api.Client, error) {
	return &api.Client{}, nil
}
