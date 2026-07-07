package codex

import (
	"context"
	"os/exec"
	"strings"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/spawn"
)

// codexSpawner produces argv:
//
//	codex exec --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox -
//
// Prompt is piped on stdin. The "dangerously-bypass" flag is codex's
// documented way to run autonomously without sandbox + approval gates.
// Less aggressive options (-s workspace-write, --add-dir) are NOT
// sufficient for `trace investigate`: codex's workspace-write policy
// excludes `.git/` regardless of --add-dir, so the agent could not
// write to <git-common-dir>/trace-investigations/<run-id>/
// (findings.md / state.json) even when that path was added. The user
// explicitly invoked the agent; the prompt forbids destructive commands.
type codexSpawner struct{}

// NewSpawner returns a Spawner for codex's non-interactive review/investigate mode.
func NewSpawner() spawn.Spawner { //nolint:ireturn // factory returns interface by design
	return codexSpawner{}
}

func (codexSpawner) Name() string { return string(agent.AgentNameCodex) }

func (codexSpawner) BuildCmd(ctx context.Context, env []string, prompt string) *exec.Cmd {
	cmd := exec.CommandContext( // #nosec G204 -- fixed "codex" binary name and fixed argv flags; prompt is piped via stdin, not an argument
		ctx, string(agent.AgentNameCodex),
		codexExecCommand,
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
		"-",
	)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = env
	return cmd
}
