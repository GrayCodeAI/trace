package agent_test

import (
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	_ "github.com/GrayCodeAI/trace/cli/agent/claudecode"
	_ "github.com/GrayCodeAI/trace/cli/agent/codex"
	_ "github.com/GrayCodeAI/trace/cli/agent/copilotcli"
	_ "github.com/GrayCodeAI/trace/cli/agent/factoryaidroid"
	_ "github.com/GrayCodeAI/trace/cli/agent/geminicli"
	_ "github.com/GrayCodeAI/trace/cli/agent/opencode"
	_ "github.com/GrayCodeAI/trace/cli/agent/pi"
	"github.com/GrayCodeAI/trace/cli/agent/types"
)

func TestResumeCommandSpecMatchesFormattedResumeCommand(t *testing.T) {
	t.Parallel()

	sessionID := "session-123"
	for _, name := range []types.AgentName{
		agent.AgentNameClaudeCode,
		agent.AgentNameCodex,
		agent.AgentNameCopilotCLI,
		agent.AgentNameFactoryAIDroid,
		agent.AgentNameGemini,
		agent.AgentNameOpenCode,
		agent.AgentNamePi,
	} {
		t.Run(string(name), func(t *testing.T) {
			t.Parallel()

			ag, err := agent.Get(name)
			if err != nil {
				t.Fatalf("Get(%s): %v", name, err)
			}
			spec, ok := agent.ResumeCommandSpecFor(name, sessionID)
			if !ok {
				t.Fatalf("ResumeCommandSpecFor(%s) ok = false, want true", name)
			}
			got := strings.Join(append([]string{spec.Binary}, spec.Args...), " ")
			if want := ag.FormatResumeCommand(sessionID); got != want {
				t.Fatalf("resume command spec = %q, FormatResumeCommand = %q", got, want)
			}
		})
	}
}
