package claudecode

import (
	"context"
)

// GenerateStreamRequest holds the request for streaming generation.
type GenerateStreamRequest struct{}

// GenerateStream generates a streaming response.
func GenerateStream(ctx context.Context, req GenerateStreamRequest) (<-chan struct{}, error) {
	ch := make(chan struct{})
	close(ch)
	return ch, nil
}
