package investigate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/gitexec"
	"github.com/GrayCodeAI/trace/cli/interactive"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/mdrender"
)

// parseAgentsCSV splits a comma-separated agent list, trimming whitespace
// and dropping empty entries.
func parseAgentsCSV(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// verifyAgentsLaunchable confirms each agent has a non-nil Spawner AND has
// hooks installed in the current repo.
func verifyAgentsLaunchable(ctx context.Context, agents []string, deps Deps) error {
	if deps.SpawnerFor == nil {
		return errors.New("investigate: SpawnerFor not wired")
	}
	if deps.GetAgentsWithHooksInstalled == nil {
		return errors.New("investigate: GetAgentsWithHooksInstalled not wired")
	}
	installed := deps.GetAgentsWithHooksInstalled(ctx)
	installedSet := make(map[string]struct{}, len(installed))
	for _, n := range installed {
		installedSet[string(n)] = struct{}{}
	}
	for _, name := range agents {
		if deps.SpawnerFor(name) == nil {
			return fmt.Errorf("agent %q is not launchable (spawner missing)", name)
		}
		if _, ok := installedSet[name]; !ok {
			return fmt.Errorf("agent %q is not launchable (run `entire configure --agent %s` first)", name, name)
		}
	}
	return nil
}

// resolveTopicAndSeed turns the user's input args into a topic + (seed
// doc path | issue link seed bytes + topic). pickerPrompt is the
// "Investigation prompt" collected from the spawn-time multipicker; it
// becomes the topic only when no seed-doc / --issue-link was supplied.
// Exactly one of seedDoc / issueSeed / topic-only is set on return.
func resolveTopicAndSeed(ctx context.Context, args []string, f runFlags, pickerPrompt string) (topic, seedDoc string, issueSeed []byte, issueTopic string, err error) {
	switch {
	case len(args) == 1:
		seedDoc = args[0]
		// #nosec G304 -- seedDoc is a user-supplied positional CLI argument, standard trusted CLI input
		body, readErr := os.ReadFile(seedDoc) //nolint:gosec // path is user-supplied positional arg
		if readErr != nil {
			return "", "", nil, "", fmt.Errorf("read seed doc %s: %w", seedDoc, readErr)
		}
		topic = DeriveTopicFromSeed(body, seedDoc)
		return topic, seedDoc, nil, "", nil
	case strings.TrimSpace(f.issueLink) != "":
		res, resErr := ResolveIssueLink(ctx, f.issueLink)
		if resErr != nil {
			return "", "", nil, "", resErr
		}
		return res.Topic, "", res.SeedDoc, res.Topic, nil
	case strings.TrimSpace(pickerPrompt) != "":
		topic = strings.TrimSpace(pickerPrompt)
		return topic, "", nil, "", nil
	default:
		return "", "", nil, "", errors.New("missing investigation input: pass [seed-doc] or --issue-link, or enter an investigation prompt in the picker")
	}
}

// topicForBootstrap returns the topic value to embed in the bootstrap
// scaffold. The seed-doc path takes precedence (Bootstrap re-derives from
// the seed body), and the issue-link path uses IssueLinkTopic; only the
// topic-only path puts the resolved topic into BootstrapInput.Topic.
func topicForBootstrap(topic, seedDoc string, issueSeed []byte) string {
	if seedDoc != "" || len(issueSeed) > 0 {
		return ""
	}
	return topic
}

// resolveDocPaths returns the absolute findings path for a run. The
// findings doc lives alongside state.json in the per-run directory under
// the git common dir:
//
//	<commonDir>/trace-investigations/<run-id>/findings.md
//	<commonDir>/trace-investigations/<run-id>/state.json
//
// Putting the per-run artefacts under the git common dir (rather than the
// worktree's .trace/investigations/) keeps the worktree's working tree
// clean — investigation findings are session-scoped scratch space, not
// part of the user's source tree.
func resolveDocPaths(commonDir, runID string) string {
	return filepath.Join(commonDir, InvestigationsDirName, runID, "findings.md")
}

// executeLoopAndCapture runs the loop and returns the LoopResult so the
// caller can use it to compose a post-run manifest / footer.
func executeLoopAndCapture(ctx context.Context, cmd *cobra.Command, in LoopInput, deps Deps) (LoopResult, error) {
	stateStore, err := NewStateStore(ctx)
	if err != nil {
		return LoopResult{}, fmt.Errorf("open run state store: %w", err)
	}

	out := cmd.OutOrStdout()
	progress, tuiSink, runCtx, cancelTUI := buildProgressSink(ctx, in, out)
	// Defers run LIFO. Register Wait first so cancelTUI fires BEFORE Wait
	// — Wait blocks on the Bubble Tea program exiting, and the ctx-watcher
	// in Start() needs ctx cancelled to push tea.Quit when no RunFinished
	// arrives (early loop return, validation error, etc.).
	if tuiSink != nil {
		tuiSink.Start(runCtx)
		defer tuiSink.Wait()
	}
	if cancelTUI != nil {
		defer cancelTUI()
	}

	ldeps := LoopDeps{
		SpawnerFor: deps.SpawnerFor,
		States:     stateStore,
		Progress:   progress,
	}

	runner := deps.LoopRun
	if runner == nil {
		runner = RunInvestigateLoop
	}
	result, runErr := runner(runCtx, in, ldeps)
	if runErr != nil {
		return result, fmt.Errorf("investigate loop: %w", runErr)
	}
	return result, nil
}

// buildProgressSink chooses between the Bubble Tea TUI and the plain-text
// fallback based on terminal capability. In TTY mode ctx is wrapped in a
// cancellable child so the in-TUI Ctrl+C handler can stop the run via the
// same cancel function the cobra root would use on SIGINT. In non-TTY mode
// the caller's ctx is returned unchanged and cancelTUI is nil.
func buildProgressSink(ctx context.Context, in LoopInput, out io.Writer) (ProgressSink, *tuiProgressSink, context.Context, context.CancelFunc) { //nolint:ireturn // returns interface by design
	if !interactive.IsTerminalWriter(out) || !interactive.CanPromptInteractively() {
		return newTextProgressSink(out), nil, ctx, nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	maxTurns := in.MaxTurns
	if maxTurns == 0 {
		maxTurns = defaultMaxTurns
	}
	quorum := in.Quorum
	if quorum == 0 {
		quorum = len(in.Agents)
	}
	sink := newTUIProgressSink(in.Topic, in.RunID, in.Agents, maxTurns, quorum, cancel, out)
	return sink, sink, runCtx, cancel
}

// writeRunManifest builds a LocalManifest from the loop result and
// persists it. Failures are logged but do not error — the docs themselves
// are the deliverable.
//
// On terminal outcomes (Quorum/Stalled) the manifest captures the final
// findings.md content into FindingsContent and the per-run directory is
// removed — the manifest becomes the durable record of the run. On
// Paused/Cancelled the per-run directory is left in place so `--continue`
// can pick up where the run left off.
func writeRunManifest(
	ctx context.Context,
	out io.Writer,
	runID, topic string,
	agents []string,
	startingSHA, worktreePath, findingsDoc string,
	startedAt, endedAt time.Time,
	result LoopResult,
) {
	manifestStore, err := NewLocalManifestStore(ctx)
	if err != nil {
		logging.Debug(ctx, "investigate: open manifest store",
			slog.String("err", err.Error()), slog.String("run_id", runID))
		return
	}
	stancesByAgent := map[string]string{}
	if result.State != nil {
		for _, s := range result.State.Stances {
			stancesByAgent[s.Agent] = s.Stance
		}
	}
	if startedAt.IsZero() && result.State != nil {
		startedAt = result.State.StartedAt
	}
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}

	// Capture findings into the manifest on terminal outcomes so the
	// content survives even after the per-run dir is deleted. Failure to
	// read is logged but non-fatal — the manifest still records that
	// the run happened, just without the findings body. The per-run dir
	// is NOT cleaned up if the read fails: leaving the file behind gives
	// the user a chance to recover it manually.
	terminal := result.Outcome == OutcomeQuorum || result.Outcome == OutcomeStalled
	findingsContent := ""
	captured := false
	if terminal && findingsDoc != "" {
		// #nosec G304 -- path computed from runID + git common dir, not external input
		data, readErr := os.ReadFile(findingsDoc) //nolint:gosec // path computed from runID + git common dir
		if readErr != nil {
			logging.Debug(ctx, "investigate: read findings for manifest capture",
				slog.String("err", readErr.Error()), slog.String("run_id", runID))
		} else {
			findingsContent = string(data)
			captured = true
		}
	}

	m := LocalManifest{
		RunID:           runID,
		Topic:           topic,
		Slug:            SlugifyTopic(topic),
		StartingSHA:     startingSHA,
		WorktreePath:    worktreePath,
		FindingsDoc:     findingsDoc,
		FindingsContent: findingsContent,
		Agents:          append([]string(nil), agents...),
		Outcome:         string(result.Outcome),
		StancesByAgent:  stancesByAgent,
		StartedAt:       startedAt,
		EndedAt:         endedAt,
	}
	if writeErr := manifestStore.Write(ctx, m); writeErr != nil {
		logging.Debug(ctx, "investigate: manifest write failed",
			slog.String("err", writeErr.Error()), slog.String("run_id", runID))
		return
	}

	// Clean up the per-run dir only AFTER the manifest write succeeds
	// and only when the findings body was captured. This keeps failure
	// modes safe: a manifest write failure leaves the per-run dir intact
	// (for retry/inspection); a read failure leaves the file on disk so
	// the user can recover it.
	if terminal && captured && findingsDoc != "" {
		runDir := filepath.Dir(findingsDoc)
		if rmErr := os.RemoveAll(runDir); rmErr != nil {
			logging.Debug(ctx, "investigate: cleanup per-run dir",
				slog.String("err", rmErr.Error()), slog.String("run_id", runID))
		}
	}

	writeInvestigateFooter(out, m)
}

// writeInvestigateFooter prints the post-run summary, the findings
// content, and how to run `trace investigate fix`. The findings
// content comes from the manifest's embedded FindingsContent on
// terminal outcomes (Quorum/Stalled — the per-run dir is gone); on
// paused/cancelled outcomes findings.md is read from the per-run dir.
func writeInvestigateFooter(w io.Writer, m LocalManifest) {
	fmt.Fprintln(w)
	if m.Outcome != "" {
		fmt.Fprintf(w, "Outcome: %s\n", m.Outcome)
	}
	// Quorum/Stalled are terminal (per-run dir cleaned, findings captured);
	// Paused/Cancelled are resumable. "complete" would mislead users into
	// thinking a paused run can't be picked up.
	switch m.Outcome {
	case string(OutcomePaused), string(OutcomeCancelled):
		fmt.Fprintln(w, "Investigation ended (resumable with `trace investigate --continue "+m.RunID+"`).")
	default:
		fmt.Fprintln(w, "Investigation complete.")
	}
	fmt.Fprintln(w)

	body := findingsContentFor(m)
	if body != "" {
		rendered, renderErr := mdrender.RenderForWriter(w, body)
		if renderErr != nil {
			// Fall back to raw markdown when glamour fails (malformed
			// style config, unexpected runtime).
			rendered = body
		}
		fmt.Fprint(w, rendered)
		if !strings.HasSuffix(rendered, "\n") {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w)
	}

	// For terminal outcomes, suggest `fix` (which feeds findings into a
	// coding agent). For paused/cancelled, `fix` would launch off stale
	// partial findings; the resume hint above is the right next step
	// instead.
	switch m.Outcome {
	case string(OutcomePaused), string(OutcomeCancelled):
		// Resume hint already emitted above.
	default:
		fmt.Fprintln(w, "To apply these findings:")
		fmt.Fprintf(w, "  trace investigate fix %s\n", m.RunID)
	}
}

// findingsContentFor returns the findings body to render in the footer.
// Prefers the manifest's embedded content (set on terminal outcomes
// when the per-run dir has been cleaned); falls back to reading the
// on-disk findings.md for paused/cancelled outcomes. Errors and
// missing files both yield "" — the caller prints a shorter footer.
func findingsContentFor(m LocalManifest) string {
	if m.FindingsContent != "" {
		return m.FindingsContent
	}
	if m.FindingsDoc == "" {
		return ""
	}
	data, err := os.ReadFile(m.FindingsDoc)
	if err != nil {
		return ""
	}
	return string(data)
}

// newRunID returns a fresh 12-hex-char run identifier, sharing the
// checkpoint-id format used by the strategy package.
func newRunID() (string, error) {
	cid, err := id.Generate()
	if err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}
	return cid.String(), nil
}

// currentHeadSHA returns the current HEAD commit hash as a 40-char hex
// string.
func currentHeadSHA(ctx context.Context, repoRoot string) (string, error) {
	return gitexec.HeadSHA(ctx, repoRoot) //nolint:wrapcheck // gitexec already wraps
}

// wrapSilent applies the silent-error wrapper if it is non-nil.
func wrapSilent(fn func(error) error, err error) error {
	if fn == nil {
		return err
	}
	return fn(err)
}
