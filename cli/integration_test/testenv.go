//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/execx"
	"github.com/GrayCodeAI/trace/cli/jsonutil"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// testBinaryPath holds the path to the CLI binary built once in TestMain.
// All tests share this binary to avoid repeated builds.
var testBinaryPath string

// getTestBinary returns the path to the shared test binary.
// It panics if TestMain hasn't run (testBinaryPath is empty).
func getTestBinary() string {
	if testBinaryPath == "" {
		panic("testBinaryPath not set - TestMain must run before tests")
	}
	return testBinaryPath
}

// TestEnv manages an isolated test environment for integration tests.
type TestEnv struct {
	T                  *testing.T
	RepoDir            string
	ClaudeProjectDir   string
	GeminiProjectDir   string
	OpenCodeProjectDir string
	SessionCounter     int
	gitConfigSnapshot  string
	gitConfigGuardSet  bool

	// ExtraEnv holds additional environment variables appended to all CLI
	// invocations (RunPrePush, GitCommitWithShadowHooks, etc.). Use this to
	// pass TRACE_CHECKPOINT_TOKEN, GIT_SSL_CAINFO, and similar per-test env.
	ExtraEnv []string
}

// NewTestEnv creates a new isolated test environment.
// It creates temp directories for the git repo and agent project files.
// Note: Does NOT change working directory to allow parallel test execution.
// Note: Does NOT use t.Setenv to allow parallel test execution - CLI commands
// receive the env var via cmd.Env instead.
func NewTestEnv(t *testing.T) *TestEnv {
	t.Helper()

	// Resolve symlinks on macOS where /var -> /private/var
	// This ensures the CLI subprocess and test use consistent paths
	repoDir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(repoDir); err == nil {
		repoDir = resolved
	}
	claudeProjectDir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(claudeProjectDir); err == nil {
		claudeProjectDir = resolved
	}
	geminiProjectDir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(geminiProjectDir); err == nil {
		geminiProjectDir = resolved
	}
	openCodeProjectDir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(openCodeProjectDir); err == nil {
		openCodeProjectDir = resolved
	}

	env := &TestEnv{
		T:                  t,
		RepoDir:            repoDir,
		ClaudeProjectDir:   claudeProjectDir,
		GeminiProjectDir:   geminiProjectDir,
		OpenCodeProjectDir: openCodeProjectDir,
	}

	// Note: Don't use t.Setenv here - it's incompatible with t.Parallel()
	// CLI commands receive TRACE_TEST_*_PROJECT_DIR via cmd.Env instead

	return env
}

// Cleanup is a no-op retained for backwards compatibility.
//
// Previously this method restored the working directory after NewTestEnv changed it.
// With the refactor to remove os.Chdir from NewTestEnv:
// - Temp directories are now cleaned up automatically by t.TempDir()
// - Working directory is never changed, so no restoration is needed
//
// This method is kept to avoid breaking existing tests that call defer env.Cleanup().
// New tests should not call this method as it serves no purpose.
//
// Deprecated: This method is a no-op and will be removed in a future version.
func (env *TestEnv) Cleanup() {
	// No-op - temp dirs are cleaned up by t.TempDir()
}

// cliEnv returns the environment variables for CLI execution.
// Includes Claude, Gemini, and OpenCode project dirs so tests work for any agent.
// Delegates to testutil.GitIsolatedEnv() for git config isolation.
func (env *TestEnv) cliEnv() []string {
	base := append(
		testutil.GitIsolatedEnv(),
		"TRACE_TEST_CLAUDE_PROJECT_DIR="+env.ClaudeProjectDir,
		"TRACE_TEST_GEMINI_PROJECT_DIR="+env.GeminiProjectDir,
		"TRACE_TEST_OPENCODE_PROJECT_DIR="+env.OpenCodeProjectDir,
	)
	return append(base, env.ExtraEnv...)
}

// RunCLI runs the trace CLI with the given arguments and returns stdout.
func (env *TestEnv) RunCLI(args ...string) string {
	env.T.Helper()
	output, err := env.RunCLIWithError(args...)
	if err != nil {
		env.T.Fatalf("CLI command failed: %v\nArgs: %v\nOutput: %s", err, args, output)
	}
	return output
}

// RunCLIWithError runs the trace CLI and returns output and error.
func (env *TestEnv) RunCLIWithError(args ...string) (string, error) {
	env.T.Helper()

	// Run CLI using the shared binary, detached from any controlling TTY
	// so interactive.CanPromptInteractively() returns false in the child.
	cmd := execx.NonInteractive(context.Background(), getTestBinary(), args...)
	cmd.Dir = env.RepoDir
	cmd.Env = env.cliEnv()

	output, err := cmd.CombinedOutput()
	return string(output), err
}

// RunCLIWithStdin runs the CLI with stdin input.
func (env *TestEnv) RunCLIWithStdin(stdin string, args ...string) string {
	env.T.Helper()

	// Run CLI with stdin using the shared binary, detached from controlling TTY.
	cmd := execx.NonInteractive(context.Background(), getTestBinary(), args...)
	cmd.Dir = env.RepoDir
	cmd.Env = env.cliEnv()
	cmd.Stdin = strings.NewReader(stdin)

	output, err := cmd.CombinedOutput()
	if err != nil {
		env.T.Fatalf("CLI command failed: %v\nArgs: %v\nOutput: %s", err, args, output)
	}
	return string(output)
}

// NewRepoEnv creates a TestEnv with an initialized git repo and Trace.
// This is a convenience factory for tests that need a basic repo setup.
func NewRepoEnv(t *testing.T) *TestEnv {
	t.Helper()
	env := NewTestEnv(t)
	env.InitRepo()
	env.InitTrace()
	return env
}

// NewRepoWithCommit creates a TestEnv with a git repo, Trace, and an initial commit.
// The initial commit contains a README.md and .gitignore (excluding .trace/).
func NewRepoWithCommit(t *testing.T) *TestEnv {
	t.Helper()
	env := NewRepoEnv(t)
	env.WriteFile(".gitignore", ".trace/\n")
	env.WriteFile("README.md", "# Test Repository")
	env.GitAdd(".gitignore")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")
	return env
}

// NewFeatureBranchEnv creates a TestEnv ready for session testing.
// It initializes the repo, creates an initial commit on main,
// and checks out a feature branch. This is the most common setup
// for session and rewind tests since Trace tracking skips main/master.
func NewFeatureBranchEnv(t *testing.T) *TestEnv {
	t.Helper()
	env := NewRepoWithCommit(t)
	env.GitCheckoutNewBranch("feature/test-branch")
	return env
}

// InitRepo initializes a git repository in the test environment.
func (env *TestEnv) InitRepo() {
	env.T.Helper()

	repo, err := git.PlainInit(env.RepoDir, false)
	if err != nil {
		env.T.Fatalf("failed to init git repo: %v", err)
	}

	// Configure git user for commits
	cfg, err := repo.Config()
	if err != nil {
		env.T.Fatalf("failed to get repo config: %v", err)
	}
	cfg.User.Name = "Test User"
	cfg.User.Email = "test@example.com"

	// Disable GPG signing for test commits (prevents failures if user has commit.gpgsign=true globally)
	if cfg.Raw == nil {
		cfg.Raw = config.New()
	}
	cfg.Raw.Section("commit").SetOption("gpgsign", "false")

	// Override any global core.hooksPath so tests use the repo-local hooks directory.
	cfg.Raw.Section("core").SetOption("hooksPath", filepath.Join(env.RepoDir, ".git", "hooks"))

	if err := repo.SetConfig(cfg); err != nil {
		env.T.Fatalf("failed to set repo config: %v", err)
	}

	env.setGitConfigBaseline()
}

func (env *TestEnv) setGitConfigBaseline() {
	env.T.Helper()

	configPath := env.gitConfigPath()
	data, err := os.ReadFile(configPath)
	if err != nil {
		env.T.Fatalf("failed to read %s: %v", configPath, err)
	}

	env.gitConfigSnapshot = string(data)
	if env.gitConfigGuardSet {
		return
	}

	env.gitConfigGuardSet = true
	env.T.Cleanup(func() {
		configPath := env.gitConfigPath()
		currentData, err := os.ReadFile(configPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if _, statErr := os.Stat(env.RepoDir); errors.Is(statErr, os.ErrNotExist) {
					return
				}
			}
			env.T.Fatalf(".git/config guard failed: could not read %s during cleanup: %v", configPath, err)
		}

		current := string(currentData)
		if normalizeGitConfigForGuard(current) == normalizeGitConfigForGuard(env.gitConfigSnapshot) {
			return
		}

		env.T.Fatalf(
			".git/config changed unexpectedly during integration test\nBaseline:\n%s\nCurrent:\n%s",
			env.gitConfigSnapshot,
			current,
		)
	})
}

// AcceptGitConfigChanges updates the .git/config guard baseline after verifying
// the config matches the exact content the test intended to write.
func (env *TestEnv) AcceptGitConfigChanges(expected string) {
	env.T.Helper()

	actual, err := os.ReadFile(env.gitConfigPath())
	if err != nil {
		env.T.Fatalf("failed to read %s: %v", env.gitConfigPath(), err)
	}
	if string(actual) != expected {
		env.T.Fatalf(
			".git/config did not match expected test mutation\nExpected:\n%s\nActual:\n%s",
			expected,
			string(actual),
		)
	}

	env.gitConfigSnapshot = expected
}

func (env *TestEnv) gitConfigPath() string {
	return filepath.Join(env.RepoDir, ".git", "config")
}

var gitConfigGuardRepositoryFormatVersionRE = regexp.MustCompile(`(?m)^([ \t]*)repositoryformatversion = [01]$`)

var gitConfigGuardTransportPromisorRemoteRE = regexp.MustCompile(
	`(?m)^\[remote "(?:(?:https?|ssh|file)://|/|[A-Za-z]:[\\/]|[^"\n]+@[^"\n]+:[^"\n]+).+"\]\n(?:[ \t]+promisor = true\n[ \t]+partialclonefilter = blob:none\n?|[ \t]+partialclonefilter = blob:none\n[ \t]+promisor = true\n?)`,
)

func normalizeGitConfigForGuard(content string) string {
	content = gitConfigGuardRepositoryFormatVersionRE.ReplaceAllString(content, `${1}repositoryformatversion = <normalized>`)
	// Deliberately ignore only the full promisor+partialclonefilter pair that
	// git writes for transport-keyed remotes during filtered fetches. If git ever
	// writes a partial section, the guard should still fail loudly.
	content = gitConfigGuardTransportPromisorRemoteRE.ReplaceAllString(content, "")
	return content
}

// InitTrace initializes the .trace directory with the specified strategy.
func (env *TestEnv) InitTrace() {
	env.InitTraceWithOptions(nil)
}

// InitTraceWithOptions initializes the .trace directory with the specified strategy and options.
func (env *TestEnv) InitTraceWithOptions(strategyOptions map[string]any) {
	env.T.Helper()
	env.initTraceInternal(strategyOptions)
}

// InitTraceWithAgent initializes an Trace test environment with a specific agent.
// The agent name is for test documentation only — the CLI resolves the agent from
// hook commands and checkpoint metadata, not from settings.json.
func (env *TestEnv) InitTraceWithAgent(_ types.AgentName) {
	env.T.Helper()
	env.initTraceInternal(nil)
}

// InitTraceWithAgentAndOptions initializes Trace with the specified strategy, agent, and options.
func (env *TestEnv) InitTraceWithAgentAndOptions(_ types.AgentName, strategyOptions map[string]any) {
	env.T.Helper()
	env.initTraceInternal(strategyOptions)
}

// initTraceInternal is the common implementation for InitTrace variants.
func (env *TestEnv) initTraceInternal(strategyOptions map[string]any) {
	env.T.Helper()

	// Create .trace directory structure
	traceDir := filepath.Join(env.RepoDir, ".trace")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		env.T.Fatalf("failed to create .trace directory: %v", err)
	}

	// Create tmp directory
	tmpDir := filepath.Join(traceDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		env.T.Fatalf("failed to create .trace/tmp directory: %v", err)
	}

	// Write settings.json
	// Note: The agent name is NOT stored in settings.json — the CLI determines
	// the agent from installed hooks (detect presence) or checkpoint metadata.
	// The settings parser uses DisallowUnknownFields(), so only recognized fields are allowed.
	settings := map[string]any{
		"enabled":   true,
		"local_dev": true, // Note: git-triggered hooks won't work (path is relative); tests call hooks via getTestBinary() instead
	}
	if strategyOptions == nil {
		strategyOptions = make(map[string]any)
	}
	if _, exists := strategyOptions["filtered_fetches"]; !exists {
		strategyOptions["filtered_fetches"] = true
	}
	if len(strategyOptions) > 0 {
		settings["strategy_options"] = strategyOptions
	}
	data, err := jsonutil.MarshalIndentWithNewline(settings, "", "  ")
	if err != nil {
		env.T.Fatalf("failed to marshal settings: %v", err)
	}
	settingsPath := filepath.Join(traceDir, paths.SettingsFileName)
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		env.T.Fatalf("failed to write %s: %v", paths.SettingsFileName, err)
	}
}

// WriteFile creates a file with the given content in the test repo.
// It creates parent directories as needed.
func (env *TestEnv) WriteFile(path, content string) {
	env.T.Helper()

	fullPath := filepath.Join(env.RepoDir, path)

	// Create parent directories
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		env.T.Fatalf("failed to create directory %s: %v", dir, err)
	}

	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		env.T.Fatalf("failed to write file %s: %v", path, err)
	}
}

// ReadFile reads a file from the test repo.
func (env *TestEnv) ReadFile(path string) string {
	env.T.Helper()

	fullPath := filepath.Join(env.RepoDir, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		env.T.Fatalf("failed to read file %s: %v", path, err)
	}
	return string(data)
}

// ReadFileAbsolute reads a file using an absolute path.
func (env *TestEnv) ReadFileAbsolute(path string) string {
	env.T.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		env.T.Fatalf("failed to read file %s: %v", path, err)
	}
	return string(data)
}

// FileExists checks if a file exists in the test repo.
func (env *TestEnv) FileExists(path string) bool {
	env.T.Helper()

	fullPath := filepath.Join(env.RepoDir, path)
	_, err := os.Stat(fullPath)
	return err == nil
}

// GitAdd stages files for commit.
func (env *TestEnv) GitAdd(paths ...string) {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		env.T.Fatalf("failed to get worktree: %v", err)
	}

	for _, path := range paths {
		if _, err := worktree.Add(path); err != nil {
			env.T.Fatalf("failed to add file %s: %v", path, err)
		}
	}
}

// GitCommit creates a commit with all staged files.
func (env *TestEnv) GitCommit(message string) {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		env.T.Fatalf("failed to get worktree: %v", err)
	}

	_, err = worktree.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		env.T.Fatalf("failed to commit: %v", err)
	}
}

// GitCommitWithMetadata creates a commit with Trace-Metadata trailer.
// This simulates commits created by the commit strategy.
func (env *TestEnv) GitCommitWithMetadata(message, metadataDir string) {
	env.T.Helper()

	// Format message with metadata trailer
	fullMessage := message + "\n\nTrace-Metadata: " + metadataDir + "\n"

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		env.T.Fatalf("failed to get worktree: %v", err)
	}

	_, err = worktree.Commit(fullMessage, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		env.T.Fatalf("failed to commit: %v", err)
	}
}

// GitCommitWithCheckpointID creates a commit with Trace-Checkpoint trailer.
// This simulates commits.
func (env *TestEnv) GitCommitWithCheckpointID(message, checkpointID string) {
	env.T.Helper()

	// Format message with checkpoint trailer
	fullMessage := message + "\n\nTrace-Checkpoint: " + checkpointID + "\n"

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		env.T.Fatalf("failed to get worktree: %v", err)
	}

	_, err = worktree.Commit(fullMessage, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		env.T.Fatalf("failed to commit: %v", err)
	}
}

// GitCommitWithMultipleSessions creates a commit with multiple Trace-Session trailers.
// This simulates merge commits that combine work from multiple sessions.
func (env *TestEnv) GitCommitWithMultipleSessions(message string, sessionIDs []string) {
	env.T.Helper()

	// Format message with multiple session trailers
	fullMessage := message + "\n\n"
	var fullMessageSb404 strings.Builder
	for _, sessionID := range sessionIDs {
		fullMessageSb404.WriteString("Trace-Session: " + sessionID + "\n")
	}
	fullMessage += fullMessageSb404.String()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		env.T.Fatalf("failed to get worktree: %v", err)
	}

	_, err = worktree.Commit(fullMessage, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		env.T.Fatalf("failed to commit: %v", err)
	}
}

// GitCommitWithMultipleCheckpoints creates a commit with multiple Trace-Checkpoint trailers.
// This simulates a GitHub squash merge commit where multiple individual commits with
// checkpoint trailers are combined into a single commit message.
func (env *TestEnv) GitCommitWithMultipleCheckpoints(message string, checkpointIDs []string) {
	env.T.Helper()

	// Format message with multiple checkpoint trailers (simulating squash merge format)
	var sb strings.Builder
	sb.WriteString(message)
	sb.WriteString("\n\n")
	for _, cpID := range checkpointIDs {
		sb.WriteString("Trace-Checkpoint: " + cpID + "\n")
	}

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		env.T.Fatalf("failed to get worktree: %v", err)
	}

	_, err = worktree.Commit(sb.String(), &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		env.T.Fatalf("failed to commit: %v", err)
	}
}

// GetHeadHash returns the current HEAD commit hash.
func (env *TestEnv) GetHeadHash() string {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	head, err := repo.Head()
	if err != nil {
		env.T.Fatalf("failed to get HEAD: %v", err)
	}

	return head.Hash().String()
}

// GetShadowBranchName returns the worktree-specific shadow branch name for the current HEAD.
// Format: trace/<commit[:7]>-<hash(worktreeID)[:6]>
func (env *TestEnv) GetShadowBranchName() string {
	env.T.Helper()

	headHash := env.GetHeadHash()
	worktreeID, err := paths.GetWorktreeID(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to get worktree ID: %v", err)
	}
	return checkpoint.ShadowBranchNameForCommit(headHash, worktreeID)
}

// GetShadowBranchNameForCommit returns the worktree-specific shadow branch name for a given commit.
// Format: trace/<commit[:7]>-<hash(worktreeID)[:6]>
func (env *TestEnv) GetShadowBranchNameForCommit(commitHash string) string {
	env.T.Helper()

	worktreeID, err := paths.GetWorktreeID(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to get worktree ID: %v", err)
	}
	return checkpoint.ShadowBranchNameForCommit(commitHash, worktreeID)
}

// GetGitLog returns a list of commit hashes from HEAD.
func (env *TestEnv) GetGitLog() []string {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	head, err := repo.Head()
	if err != nil {
		env.T.Fatalf("failed to get HEAD: %v", err)
	}

	commitIter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		env.T.Fatalf("failed to get log: %v", err)
	}

	var commits []string
	err = commitIter.ForEach(func(c *object.Commit) error {
		commits = append(commits, c.Hash.String())
		return nil
	})
	if err != nil {
		env.T.Fatalf("failed to iterate commits: %v", err)
	}

	return commits
}

// GitCheckoutNewBranch creates and checks out a new branch.
// Uses git CLI instead of go-git to work around go-git v5 bug where Checkout
// deletes untracked files (see https://github.com/go-git/go-git/issues/970).
func (env *TestEnv) GitCheckoutNewBranch(branchName string) {
	env.T.Helper()

	cmd := exec.Command("git", "checkout", "-b", branchName)
	cmd.Dir = env.RepoDir
	cmd.Env = testutil.GitIsolatedEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		env.T.Fatalf("failed to checkout new branch %s: %v\nOutput: %s", branchName, err, output)
	}
}

// GetCurrentBranch returns the current branch name.
func (env *TestEnv) GetCurrentBranch() string {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	head, err := repo.Head()
	if err != nil {
		env.T.Fatalf("failed to get HEAD: %v", err)
	}

	if !head.Name().IsBranch() {
		return "" // Detached HEAD
	}

	return head.Name().Short()
}

// RewindPoint mirrors strategy.RewindPoint for test assertions.
type RewindPoint struct {
	ID               string
	Message          string
	MetadataDir      string
	Date             time.Time
	IsTaskCheckpoint bool
	ToolUseID        string
	IsLogsOnly       bool
	CondensationID   string
}

// GetRewindPoints returns available rewind points using the CLI.
func (env *TestEnv) GetRewindPoints() []RewindPoint {
	env.T.Helper()

	// Run rewind --list using the shared binary
	cmd := exec.Command(getTestBinary(), "checkpoint", "rewind", "--list")
	cmd.Dir = env.RepoDir
	cmd.Env = env.cliEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		env.T.Fatalf("rewind --list failed: %v\nOutput: %s", err, output)
	}

	// Parse JSON output
	var jsonPoints []struct {
		ID               string `json:"id"`
		Message          string `json:"message"`
		MetadataDir      string `json:"metadata_dir"`
		Date             string `json:"date"`
		IsTaskCheckpoint bool   `json:"is_task_checkpoint"`
		ToolUseID        string `json:"tool_use_id"`
		IsLogsOnly       bool   `json:"is_logs_only"`
		CondensationID   string `json:"condensation_id"`
	}

	if err := json.Unmarshal(output, &jsonPoints); err != nil {
		env.T.Fatalf("failed to parse rewind points: %v\nOutput: %s", err, output)
	}

	points := make([]RewindPoint, len(jsonPoints))
	for i, jp := range jsonPoints {
		date, _ := time.Parse(time.RFC3339, jp.Date)
		points[i] = RewindPoint{
			ID:               jp.ID,
			Message:          jp.Message,
			MetadataDir:      jp.MetadataDir,
			Date:             date,
			IsTaskCheckpoint: jp.IsTaskCheckpoint,
			ToolUseID:        jp.ToolUseID,
			IsLogsOnly:       jp.IsLogsOnly,
			CondensationID:   jp.CondensationID,
		}
	}

	return points
}

// Rewind performs a rewind to the specified commit ID using the CLI.
func (env *TestEnv) Rewind(commitID string) error {
	env.T.Helper()

	// Run rewind --to <commitID> using the shared binary
	cmd := exec.Command(getTestBinary(), "checkpoint", "rewind", "--to", commitID)
	cmd.Dir = env.RepoDir
	cmd.Env = env.cliEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New("rewind failed: " + string(output))
	}

	env.T.Logf("Rewind output: %s", output)
	return nil
}

// RewindLogsOnly performs a logs-only rewind using the CLI.
// This restores session logs without modifying the working directory.
func (env *TestEnv) RewindLogsOnly(commitID string) error {
	env.T.Helper()

	// Run rewind --to <commitID> --logs-only using the shared binary
	cmd := exec.Command(getTestBinary(), "checkpoint", "rewind", "--to", commitID, "--logs-only")
	cmd.Dir = env.RepoDir
	cmd.Env = env.cliEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New("rewind logs-only failed: " + string(output))
	}

	env.T.Logf("Rewind logs-only output: %s", output)
	return nil
}
