package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/trailers"
)

// agentIdentityEmailDomain is the noreply domain used for synthesized agent
// commit identities. It mirrors the convention used by hosted git providers
// for bot/no-reply addresses so the attributed author/committer/co-author does
// not resolve to a real mailbox.
const agentIdentityEmailDomain = "trace.noreply.graycode.ai"

// AgentIdentity is the name/email pair used to attribute Trace commits to the
// agent (as opposed to the human git user). It is derived from the agent type.
type AgentIdentity struct {
	Name  string
	Email string
}

// String renders the identity in git "Name <email>" trailer form.
func (a AgentIdentity) String() string {
	return fmt.Sprintf("%s <%s>", a.Name, a.Email)
}

// agentIdentity derives the AgentIdentity for the given agent type. The name is
// the human-readable agent name (e.g. "Claude Code"); the email is a stable,
// slugified noreply address (e.g. "claude-code@trace.noreply.graycode.ai").
func agentIdentity(agentType types.AgentType) AgentIdentity {
	name := string(agentType)
	if name == "" {
		name = "Agent"
	}
	return AgentIdentity{
		Name:  name,
		Email: slugifyEmailLocalPart(name) + "@" + agentIdentityEmailDomain,
	}
}

// slugifyEmailLocalPart converts an agent display name into a safe email
// local-part: lowercase, with runs of non-alphanumeric characters collapsed to
// a single dash and leading/trailing dashes trimmed. Falls back to "agent" when
// the result would be empty.
func slugifyEmailLocalPart(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "agent"
	}
	return slug
}

// CommitAttribution is the resolved attribution to apply to a single
// Trace-created commit. CommitMessage already has the Co-authored-by trailer
// applied (when enabled). When AttributeAuthor / AttributeCommitter are true,
// the corresponding identity fields hold the agent identity to use; otherwise
// they carry the human git user passed in, so callers can use them
// unconditionally.
type CommitAttribution struct {
	CommitMessage  string
	AuthorName     string
	AuthorEmail    string
	CommitterName  string
	CommitterEmail string
}

// applyCommitAttribution resolves the three attribution flags against the agent
// identity and the human git author, producing the message and author/committer
// identity to record on the commit. It does not mutate its inputs.
//
//   - attribute_co_authored_by (default on): appends "Co-authored-by: <agent>"
//     to the commit message.
//   - attribute_author (default off): sets the git author to the agent.
//   - attribute_committer (default off): sets the git committer to the agent.
//
// The human GitAuthor is always used as the base for both author and committer,
// so a disabled flag leaves that identity untouched.
func applyCommitAttribution(s *settings.TraceSettings, agentType types.AgentType, human GitAuthor, message string) CommitAttribution {
	identity := agentIdentity(agentType)

	result := CommitAttribution{
		CommitMessage:  message,
		AuthorName:     human.Name,
		AuthorEmail:    human.Email,
		CommitterName:  human.Name,
		CommitterEmail: human.Email,
	}

	if s.AttributeCoAuthoredBy() {
		result.CommitMessage = trailers.AppendCoAuthoredBy(message, identity.String())
	}
	if s.AttributeAuthor() {
		result.AuthorName = identity.Name
		result.AuthorEmail = identity.Email
	}
	if s.AttributeCommitter() {
		result.CommitterName = identity.Name
		result.CommitterEmail = identity.Email
	}

	return result
}

// resolveCommitAttribution loads the trace settings and applies attribution to
// the given message and human author. On a settings load error it logs and
// falls back to defaults (co-authored-by on, author/committer off) so commit
// creation is never blocked by a malformed settings file.
func resolveCommitAttribution(ctx context.Context, agentType types.AgentType, human GitAuthor, message string) CommitAttribution {
	s, err := LoadTraceSettings(ctx)
	if err != nil {
		logging.Warn(logging.WithComponent(ctx, "attribution"),
			"failed to load settings for commit attribution; using defaults",
			slog.String("error", err.Error()))
		s = nil // accessors treat nil as the default behavior
	}
	return applyCommitAttribution(s, agentType, human, message)
}
