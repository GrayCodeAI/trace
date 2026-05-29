package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/trailers"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/checkpoint/remote"
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
	execCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "log", "-1", "--format=%B")
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
	v1Store := checkpoint.NewGitStore(repo)
	v2URL, urlErr := remote.FetchURL(ctx)
	if urlErr != nil {
		logging.Debug(ctx, "head investigate check: no configured v2 fetch remote", slog.String("error", urlErr.Error()))
		v2URL = ""
	}
	v2Store := checkpoint.NewV2GitStore(repo, v2URL)
	_, summary, err := checkpoint.ResolveCommittedReaderForCheckpoint(ctx, cpID, v1Store, v2Store, settings.IsCheckpointsV2Enabled(ctx))
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
