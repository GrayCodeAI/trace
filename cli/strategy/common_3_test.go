package strategy

import (
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	_ "github.com/GrayCodeAI/trace/cli/agent/claudecode"
	"github.com/GrayCodeAI/trace/cli/testutil"

	"github.com/stretchr/testify/assert"
)

func TestReadAgentTypeFromTree_OnlyCodex(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, ".codex/config.json", `{}`)
	testutil.GitAdd(t, dir, ".codex/config.json")
	testutil.GitCommit(t, dir, "init")

	tree := openRepoHeadTree(t, dir)
	result := ReadAgentTypeFromTree(tree, "nonexistent-path")
	assert.Equal(t, agent.AgentTypeCodex, result)
}

func TestReadAgentTypeFromTree_OnlyCursor(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, ".cursor/settings.json", `{}`)
	testutil.GitAdd(t, dir, ".cursor/settings.json")
	testutil.GitCommit(t, dir, "init")

	tree := openRepoHeadTree(t, dir)
	result := ReadAgentTypeFromTree(tree, "nonexistent-path")
	assert.Equal(t, agent.AgentTypeCursor, result)
}

func TestReadAgentTypeFromTree_OnlyFactory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, ".factory/settings.json", `{}`)
	testutil.GitAdd(t, dir, ".factory/settings.json")
	testutil.GitCommit(t, dir, "init")

	tree := openRepoHeadTree(t, dir)
	result := ReadAgentTypeFromTree(tree, "nonexistent-path")
	assert.Equal(t, agent.AgentTypeFactoryAIDroid, result)
}

func TestReadAgentTypeFromTree_ClaudeAndCodex_ReturnsUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, ".claude/settings.json", `{}`)
	testutil.GitAdd(t, dir, ".claude/settings.json")
	testutil.WriteFile(t, dir, ".codex/config.json", `{}`)
	testutil.GitAdd(t, dir, ".codex/config.json")
	testutil.GitCommit(t, dir, "init")

	tree := openRepoHeadTree(t, dir)
	result := ReadAgentTypeFromTree(tree, "nonexistent-path")
	assert.Equal(t, agent.AgentTypeUnknown, result)
}

func TestReadAgentTypeFromTree_ClaudeAndGemini_ReturnsUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, ".claude/settings.json", `{}`)
	testutil.GitAdd(t, dir, ".claude/settings.json")
	testutil.WriteFile(t, dir, ".gemini/settings.json", `{}`)
	testutil.GitAdd(t, dir, ".gemini/settings.json")
	testutil.GitCommit(t, dir, "init")

	tree := openRepoHeadTree(t, dir)
	result := ReadAgentTypeFromTree(tree, "nonexistent-path")
	assert.Equal(t, agent.AgentTypeUnknown, result)
}

func TestReadAgentTypeFromTree_NoAgentDirs_ReturnsUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, "f.txt", "init")
	testutil.GitAdd(t, dir, "f.txt")
	testutil.GitCommit(t, dir, "init")

	tree := openRepoHeadTree(t, dir)
	result := ReadAgentTypeFromTree(tree, "nonexistent-path")
	assert.Equal(t, agent.AgentTypeUnknown, result)
}

func TestReadAgentTypeFromTree_MetadataJSON_OverridesDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, ".claude/settings.json", `{}`)
	testutil.GitAdd(t, dir, ".claude/settings.json")
	testutil.WriteFile(t, dir, "cp/metadata.json", `{"agent":"Cursor"}`)
	testutil.GitAdd(t, dir, "cp/metadata.json")
	testutil.GitCommit(t, dir, "init")

	tree := openRepoHeadTree(t, dir)
	result := ReadAgentTypeFromTree(tree, "cp")
	assert.Equal(t, agent.AgentTypeCursor, result)
}
