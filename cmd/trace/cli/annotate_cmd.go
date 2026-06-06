package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/session"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/strategy"
	"github.com/spf13/cobra"
)

// newAnnotateCmd creates the `trace annotate` command, which attaches free-form
// user comments to a session (optionally scoped to a specific checkpoint). The
// annotations are persisted in the session state JSON via MutateSessionState,
// matching how other session metadata is stored.
func newAnnotateCmd() *cobra.Command {
	var (
		commentFlag    string
		checkpointFlag string
		listFlag       bool
	)

	cmd := &cobra.Command{
		Use:   "annotate <session-id>",
		Short: "Attach a comment to a session or checkpoint",
		Long: `Attach a free-form comment to a session, optionally scoped to a
specific checkpoint. Annotations are stored in the session state and can be
listed later.

Examples:
  trace annotate <session-id> --comment "reproduced the bug here"
  trace annotate <session-id> --checkpoint <id> --comment "this broke the build"
  trace annotate <session-id> --list`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAnnotate(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], commentFlag, checkpointFlag, listFlag)
		},
	}

	cmd.Flags().StringVar(&commentFlag, "comment", "", "The annotation text to attach")
	cmd.Flags().StringVar(&checkpointFlag, "checkpoint", "", "Scope the annotation to a specific checkpoint ID")
	cmd.Flags().BoolVar(&listFlag, "list", false, "List existing annotations for the session")

	return cmd
}

func runAnnotate(ctx context.Context, w, errW io.Writer, sessionID, comment, checkpointID string, list bool) error {
	if _, err := paths.WorktreeRoot(ctx); err != nil {
		return errors.New("not a git repository")
	}

	if list {
		return runAnnotateList(ctx, w, errW, sessionID)
	}

	if comment == "" {
		return errors.New("--comment is required (or use --list to view annotations)")
	}

	// Confirm the session exists before attempting to mutate it, so we can
	// surface a clear "not found" message instead of ErrStateNotFound.
	state, err := strategy.LoadSessionState(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to load session: %w", err)
	}
	if state == nil {
		fmt.Fprintln(errW, "Session not found.")
		return NewSilentError(fmt.Errorf("session not found: %s", sessionID))
	}

	annotation := session.Annotation{
		Comment:      comment,
		CheckpointID: checkpointID,
		CreatedAt:    time.Now().UTC(),
	}
	if author, aerr := GetGitAuthor(ctx); aerr == nil && author != nil {
		annotation.Author = author.Name
	}

	if err := strategy.MutateSessionState(ctx, sessionID, func(s *strategy.SessionState) error {
		s.Annotations = append(s.Annotations, annotation)
		return nil
	}); err != nil {
		return fmt.Errorf("failed to save annotation: %w", err)
	}

	sty := newStatusStyles(w)
	rows := []explainRow{
		{Label: "session", Value: sessionID},
	}
	if checkpointID != "" {
		rows = append(rows, explainRow{Label: "checkpoint", Value: checkpointID})
	}
	rows = append(rows, explainRow{Label: "comment", Value: comment})
	fmt.Fprint(w, sty.renderSuccess("Annotation added", rows))
	return nil
}

func runAnnotateList(ctx context.Context, w, errW io.Writer, sessionID string) error {
	state, err := strategy.LoadSessionState(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to load session: %w", err)
	}
	if state == nil {
		fmt.Fprintln(errW, "Session not found.")
		return NewSilentError(fmt.Errorf("session not found: %s", sessionID))
	}

	if len(state.Annotations) == 0 {
		fmt.Fprintf(w, "No annotations for session %s.\n", sessionID)
		return nil
	}

	// Show oldest first for a chronological reading order.
	annotations := make([]session.Annotation, len(state.Annotations))
	copy(annotations, state.Annotations)
	sort.SliceStable(annotations, func(i, j int) bool {
		return annotations[i].CreatedAt.Before(annotations[j].CreatedAt)
	})

	sty := newStatusStyles(w)
	fmt.Fprintln(w, sty.sectionRule(fmt.Sprintf("Annotations for %s", sessionID), sty.width))
	fmt.Fprintln(w)

	for _, a := range annotations {
		var meta []string
		meta = append(meta, a.CreatedAt.Local().Format("2006-01-02 15:04"))
		if a.Author != "" {
			meta = append(meta, a.Author)
		}
		if a.CheckpointID != "" {
			meta = append(meta, "checkpoint "+a.CheckpointID)
		}
		fmt.Fprintln(w, sty.render(sty.dim, joinDot(sty, meta)))
		fmt.Fprintf(w, "  %s\n\n", a.Comment)
	}

	return nil
}

// joinDot joins parts with a dimmed " · " separator, matching the session card style.
func joinDot(sty statusStyles, parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sty.render(sty.dim, " · ")
		}
		out += p
	}
	return out
}
