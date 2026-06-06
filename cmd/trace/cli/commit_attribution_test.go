package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/settings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

func attrBoolPtr(b bool) *bool { return &b }

const (
	attrAgentName  = "Claude Code"
	attrAgentEmail = "claude-code@trace.noreply.graycode.ai"
)

var attrHuman = GitAuthor{Name: "Alice", Email: "alice@example.com"}

func TestAgentIdentity_SlugifiesEmail(t *testing.T) {
	id := agentIdentity(agent.AgentTypeClaudeCode)
	if id.Name != attrAgentName {
		t.Errorf("Name = %q, want %q", id.Name, attrAgentName)
	}
	if id.Email != attrAgentEmail {
		t.Errorf("Email = %q, want %q", id.Email, attrAgentEmail)
	}
	if got, want := id.String(), attrAgentName+" <"+attrAgentEmail+">"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestApplyCommitAttribution_Defaults(t *testing.T) {
	// nil settings -> co-authored-by on, author/committer untouched (human).
	got := applyCommitAttribution(nil, agent.AgentTypeClaudeCode, attrHuman, "Add feature")

	if !strings.Contains(got.CommitMessage, "Co-authored-by: "+attrAgentName+" <"+attrAgentEmail+">") {
		t.Errorf("default should append co-authored-by trailer, got %q", got.CommitMessage)
	}
	if got.AuthorName != attrHuman.Name || got.AuthorEmail != attrHuman.Email {
		t.Errorf("default author should remain human, got %s <%s>", got.AuthorName, got.AuthorEmail)
	}
	if got.CommitterName != attrHuman.Name || got.CommitterEmail != attrHuman.Email {
		t.Errorf("default committer should remain human, got %s <%s>", got.CommitterName, got.CommitterEmail)
	}
}

func TestApplyCommitAttribution_CoAuthoredByOnly(t *testing.T) {
	s := &settings.TraceSettings{Attribution: &settings.AttributionSettings{
		AttributeCoAuthoredBy: attrBoolPtr(true),
		AttributeAuthor:       attrBoolPtr(false),
		AttributeCommitter:    attrBoolPtr(false),
	}}
	got := applyCommitAttribution(s, agent.AgentTypeClaudeCode, attrHuman, "Subj")
	if !strings.Contains(got.CommitMessage, "Co-authored-by: "+attrAgentName) {
		t.Errorf("expected co-authored-by trailer, got %q", got.CommitMessage)
	}
	if got.AuthorName != attrHuman.Name || got.CommitterName != attrHuman.Name {
		t.Errorf("author/committer should be human when only co-authored-by is on")
	}
}

func TestApplyCommitAttribution_AuthorOnly(t *testing.T) {
	s := &settings.TraceSettings{Attribution: &settings.AttributionSettings{
		AttributeCoAuthoredBy: attrBoolPtr(false),
		AttributeAuthor:       attrBoolPtr(true),
		AttributeCommitter:    attrBoolPtr(false),
	}}
	got := applyCommitAttribution(s, agent.AgentTypeClaudeCode, attrHuman, "Subj")
	if strings.Contains(got.CommitMessage, "Co-authored-by:") {
		t.Errorf("co-authored-by off: message should not contain trailer, got %q", got.CommitMessage)
	}
	if got.AuthorName != attrAgentName || got.AuthorEmail != attrAgentEmail {
		t.Errorf("author should be agent, got %s <%s>", got.AuthorName, got.AuthorEmail)
	}
	if got.CommitterName != attrHuman.Name || got.CommitterEmail != attrHuman.Email {
		t.Errorf("committer should remain human, got %s <%s>", got.CommitterName, got.CommitterEmail)
	}
}

func TestApplyCommitAttribution_CommitterOnly(t *testing.T) {
	s := &settings.TraceSettings{Attribution: &settings.AttributionSettings{
		AttributeCoAuthoredBy: attrBoolPtr(false),
		AttributeAuthor:       attrBoolPtr(false),
		AttributeCommitter:    attrBoolPtr(true),
	}}
	got := applyCommitAttribution(s, agent.AgentTypeClaudeCode, attrHuman, "Subj")
	if got.CommitterName != attrAgentName || got.CommitterEmail != attrAgentEmail {
		t.Errorf("committer should be agent, got %s <%s>", got.CommitterName, got.CommitterEmail)
	}
	if got.AuthorName != attrHuman.Name || got.AuthorEmail != attrHuman.Email {
		t.Errorf("author should remain human, got %s <%s>", got.AuthorName, got.AuthorEmail)
	}
}

// TestApplyCommitAttribution_RealCommit verifies that, for each flag in
// isolation, a real go-git commit built from the resolved attribution carries
// the expected trailer/author/committer.
func TestApplyCommitAttribution_RealCommit(t *testing.T) {
	tests := []struct {
		name           string
		attr           *settings.AttributionSettings
		wantTrailer    bool
		wantAuthorName string
		wantCommitName string
	}{
		{
			name:           "co_authored_by_only",
			attr:           &settings.AttributionSettings{AttributeCoAuthoredBy: attrBoolPtr(true)},
			wantTrailer:    true,
			wantAuthorName: attrHuman.Name,
			wantCommitName: attrHuman.Name,
		},
		{
			name:           "author_only",
			attr:           &settings.AttributionSettings{AttributeCoAuthoredBy: attrBoolPtr(false), AttributeAuthor: attrBoolPtr(true)},
			wantTrailer:    false,
			wantAuthorName: attrAgentName,
			wantCommitName: attrHuman.Name,
		},
		{
			name:           "committer_only",
			attr:           &settings.AttributionSettings{AttributeCoAuthoredBy: attrBoolPtr(false), AttributeCommitter: attrBoolPtr(true)},
			wantTrailer:    false,
			wantAuthorName: attrHuman.Name,
			wantCommitName: attrAgentName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			repo := initOpenedTestRepo(t, tmpDir)
			w, err := repo.Worktree()
			require.NoError(t, err)

			require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("hi"), 0o644))
			_, err = w.Add("f.txt")
			require.NoError(t, err)

			s := &settings.TraceSettings{Attribution: tt.attr}
			a := applyCommitAttribution(s, agent.AgentTypeClaudeCode, attrHuman, "Add feature")

			hash, err := w.Commit(a.CommitMessage, &git.CommitOptions{
				Author:    &object.Signature{Name: a.AuthorName, Email: a.AuthorEmail},
				Committer: &object.Signature{Name: a.CommitterName, Email: a.CommitterEmail},
			})
			require.NoError(t, err)

			c, err := repo.CommitObject(hash)
			require.NoError(t, err)

			if c.Author.Name != tt.wantAuthorName {
				t.Errorf("author name = %q, want %q", c.Author.Name, tt.wantAuthorName)
			}
			if c.Committer.Name != tt.wantCommitName {
				t.Errorf("committer name = %q, want %q", c.Committer.Name, tt.wantCommitName)
			}
			hasTrailer := strings.Contains(c.Message, "Co-authored-by: "+attrAgentName)
			if hasTrailer != tt.wantTrailer {
				t.Errorf("co-authored-by trailer present = %v, want %v (message %q)", hasTrailer, tt.wantTrailer, c.Message)
			}
		})
	}
}
