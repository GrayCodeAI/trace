package cli

import (
	"context"
	"io"

	"github.com/GrayCodeAI/trace/cli/api"
)

// runAuthenticatedActivityAPI runs fn with an authenticated client for the
// activity/recap surface. It prefers the caller's home entire-api cell (the same
// shared client the experts commands use), which serves the /me/* endpoints
// these commands call.
//
// Cell routing is a best-effort upgrade: any failure building the cell client —
// the region has no cell yet (ErrNoCellForJurisdiction), not logged in, or a
// discovery/exchange error — falls back to the data API, which also serves
// /me/* and yields the canonical auth errors (e.g. the "not logged in" hint).
// This keeps the migration transparent and non-regressive; non-obvious
// fallbacks are logged for diagnosis. Both backends expose the same /me/* paths,
// so fn is agnostic to which client it receives.
func runAuthenticatedActivityAPI(ctx context.Context, errW io.Writer, insecureHTTP bool, fn func(context.Context, *api.Client) error) error {
	var err error
	if err != nil {
		// logCellClientFallback
		return runAuthenticatedDataAPI(ctx, errW, insecureHTTP, fn)
	}
	return fn(ctx, &api.Client{})
}
