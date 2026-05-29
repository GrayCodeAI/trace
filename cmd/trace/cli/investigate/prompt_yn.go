package investigate

import (
	"context"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/uiform"
)

func realPromptYN(ctx context.Context, question string, def bool) (bool, error) {
	return uiform.PromptYN(ctx, question, def) //nolint:wrapcheck // uiform already wraps
}
