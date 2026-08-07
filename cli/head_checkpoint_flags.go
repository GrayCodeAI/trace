package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/trailers"
	"github.com/go-git/go-git/v6"
)

// headHasInvestigateCheckpoint reports whether the current HEAD commit
// carries a checkpoint trailer whose summary has HasInvestigation set.
// Returns (true, info) when found; (false, "") otherwise.
func headHasInvestigateCheckpoint(ctx context.Context) (bool, string) {
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		logging.Debug(ctx, "head investigate check: locate worktree root", slog.String("error", err.Error()))
		return false, ""
	}
	execCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "log", "-1", "--format=%B") // #nosec G204 -- repoRoot is the resolved worktree root, not user input
	output, err := execCmd.Output()
	if err != nil {
		logging.Debug(ctx, "head investigate check: read HEAD commit message", slog.String("error", err.Error()))
		return false, ""
	}
	cpID, ok := trailers.ParseCheckpoint(string(output))
	if !ok {
		logging.Debug(ctx, "head investigate check: no checkpoint trailer on HEAD")
		return false, ""
	}
	repo, err := git.PlainOpen(repoRoot)
	if err != nil {
		logging.Debug(ctx, "head investigate check: open repository", slog.String("error", err.Error()))
		return false, ""
	}
	stores, err := checkpoint.Open(ctx, repo, checkpoint.OpenOptions{})
	if err != nil {
		logging.Debug(ctx, "head investigate check: open checkpoint store", slog.String("error", err.Error()))
		return false, ""
	}
	summary, err := stores.Persistent.Read(ctx, cpID)
	if err != nil || summary == nil {
		logging.Debug(ctx, "head investigate check: resolve checkpoint summary",
			slog.String("checkpoint_id", cpID.String()),
			slog.Any("error", err))
		return false, ""
	}
	if !summary.HasInvestigation {
		logging.Debug(ctx, "head investigate check: summary HasInvestigation is false", slog.String("checkpoint_id", cpID.String()))
		return false, ""
	}
	return true, fmt.Sprintf("checkpoint %s", cpID)
}
