package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureGitIdentity_NonInteractiveNoGh_Errors(t *testing.T) {
	r := newFakeRunner()
	r.set("git", []string{"config", "--get", "user.name"}, "", errors.New("not set"))
	r.set("git", []string{"config", "--get", "user.email"}, "", errors.New("not set"))
	r.set("gh", []string{"--version"}, "", errors.New("not found"))

	err := ensureGitIdentity(context.Background(), io.Discard, io.Discard, r, t.TempDir())
	if err == nil {
		t.Fatal("expected error when identity missing and gh unavailable")
	}
	if !strings.Contains(err.Error(), "git config --global user.name") {
		t.Fatalf("expected guidance to set git config, got %v", err)
	}
}

func TestGhUserIdentity_NameFallsBackToLogin(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.set("gh", []string{"api", "user"}, `{"id":7,"login":"dev","name":"","email":"dev@example.com"}`, nil)
	name, email, err := ghUserIdentity(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "dev" {
		t.Fatalf("name = %q", name)
	}
	if email != "dev@example.com" {
		t.Fatalf("email = %q", email)
	}
}

// TestBootstrap_FreshMachine_RealGit is an integration-style test that runs
// real git via execRunner on a temp dir isolated from the user's global git
// config. Regression guard for the issue where bootstrap commits failed
// without a configured identity or because of commit.gpgsign=true.
func TestBootstrap_FreshMachine_RealGit(t *testing.T) {
	// Isolate from any global git config: point HOME + GIT_CONFIG_* at
	// empty/missing locations, and force a broken GPG signing config that
	// would fail any commit if we did not pass -c commit.gpgsign=false.
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	// A global config that demands signing with a non-existent program. If
	// our bootstrap did not override gpgsign for its commit, git would
	// error out here.
	globalCfg := filepath.Join(emptyHome, ".gitconfig")
	globalContent := "[user]\n\tname = Fresh User\n\temail = fresh@example.com\n[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = /does/not/exist\n"
	if err := writeTempFile(globalCfg, globalContent); err != nil {
		t.Fatalf("write global gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)
	// Ensure no system config interferes.
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	projectDir := t.TempDir()
	restoreCwd(t, projectDir)
	// Create a file to commit.
	if err := writeTempFile(filepath.Join(projectDir, "README.md"), "hello\n"); err != nil {
		t.Fatalf("write file: %v", err)
	}

	opts := GitHubBootstrapOptions{
		InitRepo:             true,
		NoGitHub:             true,
		InitialCommitMessage: "Initial",
	}
	err := runGitHubBootstrapWith(context.Background(), io.Discard, io.Discard, opts, execRunner{})
	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}

	// Verify a commit actually landed on HEAD.
	out, err := execRunner{}.RunInDir(context.Background(), projectDir, "git", "log", "--oneline")
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	if !strings.Contains(out, "Initial") {
		t.Fatalf("expected 'Initial' commit in log, got: %q", out)
	}
}

func writeTempFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// ghFailingRunner wraps another bootstrapRunner and forces all `gh`
// invocations to fail, while letting real `git` calls through. This
// lets tests deterministically exercise the "gh unavailable" path
// regardless of whether `gh` is installed/authenticated on the host.
type ghFailingRunner struct {
	inner bootstrapRunner
}

func (r ghFailingRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	if name == "gh" {
		return "", errors.New("gh not available (test)")
	}
	return r.inner.Run(ctx, name, args...)
}

func (r ghFailingRunner) RunInDir(ctx context.Context, dir, name string, args ...string) (string, error) {
	if name == "gh" {
		return "", errors.New("gh not available (test)")
	}
	return r.inner.RunInDir(ctx, dir, name, args...)
}

// TestBootstrap_FreshMachine_NoIdentity_RealGit verifies that a fresh
// machine without any git identity configured fails cleanly in
// non-interactive mode with a helpful error message, instead of letting
// `git commit` fail with a confusing "please tell me who you are" stderr.
//
// Uses a gh-failing runner wrapper rather than PATH manipulation so the
// test isn't sensitive to whether `gh` + GH_TOKEN/GITHUB_TOKEN are set
// on the host.
func TestBootstrap_FreshMachine_NoIdentity_RealGit(t *testing.T) {
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	// Empty global config: no user.name/user.email.
	globalCfg := filepath.Join(emptyHome, ".gitconfig")
	if err := writeTempFile(globalCfg, ""); err != nil {
		t.Fatalf("write global gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	// Belt-and-suspenders: unset any GitHub tokens so a wrapper bypass
	// would still not find credentials.
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	projectDir := t.TempDir()
	restoreCwd(t, projectDir)
	if err := writeTempFile(filepath.Join(projectDir, "README.md"), "hi\n"); err != nil {
		t.Fatalf("write file: %v", err)
	}

	opts := GitHubBootstrapOptions{
		InitRepo:             true,
		NoGitHub:             true,
		InitialCommitMessage: "x",
	}
	runner := ghFailingRunner{inner: execRunner{}}
	err := runGitHubBootstrapWith(context.Background(), io.Discard, io.Discard, opts, runner)
	if err == nil {
		t.Fatal("expected error when identity missing and gh unavailable")
	}
	if !strings.Contains(err.Error(), "git config --global user.name") {
		t.Fatalf("expected guidance to set git config, got: %v", err)
	}
}

// TestErrSentinels_DistinctPrePostInit documents the contract that the two
// error sentinels signal: errBootstrapDeclined before `git init`,
// errBootstrapInterrupted after. setup.go relies on this to show the
// right user-facing message.
func TestErrSentinels_DistinctPrePostInit(t *testing.T) {
	t.Parallel()
	if errors.Is(errBootstrapDeclined, errBootstrapInterrupted) {
		t.Fatal("errBootstrapDeclined and errBootstrapInterrupted must not match as the same sentinel")
	}
}

func TestEnableCmd_InitCommitMessageFlagsMutuallyExclusive(t *testing.T) {
	setupTestRepo(t)

	cmd := newEnableCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--initial-commit-message", "foo", "--skip-initial-commit"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when both --initial-commit-message and --skip-initial-commit are set")
	}
	if !strings.Contains(err.Error(), "initial-commit-message") || !strings.Contains(err.Error(), "skip-initial-commit") {
		t.Fatalf("expected error to mention both flags, got: %v", err)
	}
}

func TestEnableCmd_InitRepoFlagsMutuallyExclusive(t *testing.T) {
	setupTestRepo(t)

	cmd := newEnableCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--init-repo", "--no-init-repo"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when both --init-repo and --no-init-repo are set")
	}
	if !strings.Contains(err.Error(), "init-repo") || !strings.Contains(err.Error(), "no-init-repo") {
		t.Fatalf("expected error to mention both flags, got: %v", err)
	}
}

// restoreCwd chdirs into dir for the duration of the test.
func restoreCwd(t *testing.T, dir string) {
	t.Helper()
	// macOS resolves /tmp → /private/tmp; canonicalize for safety.
	canon, err := filepath.EvalSymlinks(dir)
	if err != nil {
		canon = dir
	}
	t.Chdir(canon)
}

func TestRunGitHubBootstrap_YesAcceptsAllDefaults(t *testing.T) {
	// --yes should init repo, create GitHub repo under user's account (private),
	// and use default commit message — without any interactive prompts.
	dir := t.TempDir()
	restoreCwd(t, dir)

	r := newFakeRunner()
	r.setIdentityConfigured()
	r.set("gh", []string{"--version"}, "gh 2.81.0", nil)
	r.set("gh", []string{"auth", "status"}, "Logged in", nil)
	r.set("gh", []string{"api", "user", "--jq", ".login"}, "myuser\n", nil)
	r.set("gh", []string{"api", "user/orgs", "--jq", ".[].login"}, "myorg\n", nil)
	r.set("git", []string{"init"}, "", nil)
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"status", "--porcelain"}, " M f\n", nil)
	r.set("git", []string{"-c", "commit.gpgsign=false", "commit", "-m", "Initial commit"}, "", nil)

	// Expect repo created under the user's account (not org), private
	repoName := filepath.Base(dir)
	fullName := "myuser/" + repoName
	r.set("gh", []string{
		"repo", "create", fullName,
		"--private",
		"--source=.",
		"--remote=origin",
	}, "", nil)
	r.set("git", []string{"push", "-q", "--no-verify", "-u", "origin", "HEAD"}, "", nil)

	opts := GitHubBootstrapOptions{Yes: true}
	var stdout bytes.Buffer
	err := runGitHubBootstrapWith(context.Background(), &stdout, io.Discard, opts, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have used user's account, not org
	output := stdout.String()
	if !strings.Contains(output, "Using GitHub owner: myuser") {
		t.Errorf("expected owner to be user's account, got: %s", output)
	}
	// Should have committed with default message
	if !r.hasCall(argsMatch("git", []string{"-c", "commit.gpgsign=false", "commit", "-m", "Initial commit"})) {
		t.Error("expected commit with default 'Initial commit' message")
	}
	// Should have created the repo
	if !r.hasCall(func(c fakeCall) bool {
		return c.name == "gh" && len(c.args) > 3 && c.args[0] == ghSubcmdRepo && c.args[1] == ghActCreate
	}) {
		t.Error("expected gh repo create call")
	}
}

func TestRunGitHubBootstrap_YesRepoExistsNoTTY_Fails(t *testing.T) {
	// When --yes is set, the repo name is taken, and there's no TTY,
	// we should get a clear error instead of a silent gh failure.
	dir := t.TempDir()
	restoreCwd(t, dir)

	r := newFakeRunner()
	r.setIdentityConfigured()
	r.set("gh", []string{"--version"}, "gh 2.81.0", nil)
	r.set("gh", []string{"auth", "status"}, "Logged in", nil)
	r.set("gh", []string{"api", "user", "--jq", ".login"}, "myuser\n", nil)
	r.set("gh", []string{"api", "user/orgs", "--jq", ".[].login"}, "", nil)
	r.set("git", []string{"init"}, "", nil)

	// The suggested repo name already exists.
	repoName := filepath.Base(dir)
	r.set("gh", []string{"repo", "view", "myuser/" + repoName, "--json", "name"}, `{"name":"`+repoName+`"}`, nil)

	opts := GitHubBootstrapOptions{Yes: true}
	err := runGitHubBootstrapWith(context.Background(), io.Discard, io.Discard, opts, r)
	if err == nil {
		t.Fatal("expected error when repo name exists and no TTY")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

func TestResolveRepoName_YesRepoExistsWithTTY_FallsBackToPrompt(t *testing.T) {
	// When --yes is set, the name is taken, and a TTY is available,
	// resolveRepoName should print a conflict message and fall through
	// to the interactive prompt. We verify the conflict message was
	// printed (proving the fallback path was taken).
	t.Setenv("TRACE_TEST_TTY", "1")

	// Force accessible (text-based) mode so the huh form reads from
	// os.Stdin instead of trying to open /dev/tty via bubbletea.
	// Pipe a unique name so the form completes instead of blocking.
	t.Setenv("ACCESSIBLE", "1")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Close() })
	go func() {
		// The form reads one line; provide a unique name so it exits the loop.
		pw.WriteString("unique-test-repo\n") //nolint:errcheck // test helper
		pw.Close()
	}()
	oldStdin := os.Stdin
	os.Stdin = pr
	t.Cleanup(func() { os.Stdin = oldStdin })

	dir := t.TempDir()
	restoreCwd(t, dir)

	r := newFakeRunner()
	repoName := filepath.Base(dir)
	// The suggested name exists.
	r.set("gh", []string{"repo", "view", "myuser/" + repoName, "--json", "name"}, `{"name":"`+repoName+`"}`, nil)
	// The unique name typed at the prompt does not exist (fakeRunner returns
	// an error for unknown calls, which ghRepoExists treats as "proceed").

	var stdout bytes.Buffer
	opts := GitHubBootstrapOptions{Yes: true}
	name, err := resolveRepoName(context.Background(), &stdout, io.Discard, r, "myuser", dir, opts)

	output := stdout.String()
	if !strings.Contains(output, "already exists on GitHub") {
		t.Errorf("expected conflict message in output, got: %s", output)
	}
	// The form should complete with the unique name (fakeRunner can't verify
	// the name, so resolveRepoName proceeds with a warning).
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if name != "unique-test-repo" {
		t.Errorf("expected name %q, got %q", "unique-test-repo", name)
	}
}

func TestRunGitHubBootstrap_YesWithNoGitHub(t *testing.T) {
	// --yes combined with --no-github should skip GitHub but still init + commit.
	dir := t.TempDir()
	restoreCwd(t, dir)

	r := newFakeRunner()
	r.setIdentityConfigured()
	r.set("git", []string{"init"}, "", nil)
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"status", "--porcelain"}, " M f\n", nil)
	r.set("git", []string{"-c", "commit.gpgsign=false", "commit", "-m", "Initial commit"}, "", nil)

	opts := GitHubBootstrapOptions{Yes: true, NoGitHub: true}
	err := runGitHubBootstrapWith(context.Background(), io.Discard, io.Discard, opts, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT have called gh at all
	if r.hasCall(func(c fakeCall) bool { return c.name == "gh" }) {
		t.Error("expected no gh calls with --no-github")
	}
	// Should have committed
	if !r.hasCall(argsMatch("git", []string{"-c", "commit.gpgsign=false", "commit", "-m", "Initial commit"})) {
		t.Error("expected commit with default message")
	}
}
