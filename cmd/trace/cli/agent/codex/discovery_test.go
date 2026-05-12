package codex_test

import (
	"context"
	"testing"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent/codex"
)

// Compile-time pin: CodexAgent must satisfy SkillDiscoverer.
var _ agent.SkillDiscoverer = (*codex.CodexAgent)(nil)

func TestCodexAgent_DiscoverReviewSkills_Stub(t *testing.T) {
	t.Parallel()
	a := &codex.CodexAgent{}
	skills, err := a.DiscoverReviewSkills(context.Background())
	if err != nil {
		t.Fatalf("stub should not error; got %v", err)
	}
	if skills != nil {
		t.Errorf("stub should return nil skills; got %+v", skills)
	}
}
