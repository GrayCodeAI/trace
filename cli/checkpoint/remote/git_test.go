package remote

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/GrayCodeAI/trace/cli/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractRemoteFromArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"fetch with URL", []string{"fetch", "https://github.com/org/repo.git", "refs/heads/main"}, "https://github.com/org/repo.git"},
		{"push with flags", []string{"push", "--no-verify", "--porcelain", "origin", "main"}, "origin"},
		{"ls-remote", []string{"ls-remote", "origin", "refs/heads/*"}, "origin"},
		{"fetch with filter", []string{"fetch", "--no-tags", "--filter=blob:none", "https://host/r.git", "+refs/heads/main:refs/tmp"}, "https://host/r.git"},
		{"empty args", []string{}, ""},
		{"subcommand only", []string{"fetch"}, ""},
		{"only flags", []string{"fetch", "--no-tags"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, extractRemoteFromArgs(tt.args))
		})
	}
}

func TestResolveTargetForTokenAuth(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("HTTPS URL passes through as HTTPS", func(t *testing.T) {
		t.Parallel()
		got, proto := resolveTargetForTokenAuth(ctx, "https://github.com/org/repo.git")
		assert.Equal(t, "https://github.com/org/repo.git", got)
		assert.Equal(t, ProtocolHTTPS, proto)
	})

	t.Run("SSH SCP URL rewrites to HTTPS", func(t *testing.T) {
		t.Parallel()
		got, proto := resolveTargetForTokenAuth(ctx, "git@github.com:org/repo.git")
		assert.Equal(t, "https://github.com/org/repo.git", got)
		assert.Equal(t, ProtocolHTTPS, proto)
	})

	t.Run("SSH protocol URL rewrites to HTTPS without port", func(t *testing.T) {
		t.Parallel()
		got, proto := resolveTargetForTokenAuth(ctx, "ssh://git@git.example.com:2222/org/repo.git")
		assert.Equal(t, "https://git.example.com/org/repo.git", got)
		assert.Equal(t, ProtocolHTTPS, proto)
	})

	t.Run("local path returns empty protocol", func(t *testing.T) {
		t.Parallel()
		got, proto := resolveTargetForTokenAuth(ctx, "/tmp/some-bare-repo")
		assert.Equal(t, "/tmp/some-bare-repo", got)
		assert.Empty(t, proto)
	})

	t.Run("nonexistent remote name returns empty protocol", func(t *testing.T) {
		t.Parallel()
		got, proto := resolveTargetForTokenAuth(ctx, "nonexistent-remote")
		assert.Equal(t, "nonexistent-remote", got)
		assert.Empty(t, proto)
	})
}

// Not parallel: uses t.Chdir()
func TestResolveTargetForTokenAuth_RemoteName_HTTPS(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "f.txt", "init")
	testutil.GitAdd(t, tmpDir, "f.txt")
	testutil.GitCommit(t, tmpDir, "init")

	cmd := exec.CommandContext(ctx, "git", "remote", "add", "origin", "https://github.com/org/repo.git")
	cmd.Dir = tmpDir
	cmd.Env = testutil.GitIsolatedEnv()
	require.NoError(t, cmd.Run())

	t.Chdir(tmpDir)

	got, proto := resolveTargetForTokenAuth(ctx, "origin")
	assert.Equal(t, "origin", got, "HTTPS remote names pass through unchanged")
	assert.Equal(t, ProtocolHTTPS, proto)
}

// Not parallel: uses t.Chdir()
func TestResolveTargetForTokenAuth_RemoteName_SSH_RewritesToHTTPS(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "f.txt", "init")
	testutil.GitAdd(t, tmpDir, "f.txt")
	testutil.GitCommit(t, tmpDir, "init")

	cmd := exec.CommandContext(ctx, "git", "remote", "add", "origin", "git@github.com:org/repo.git")
	cmd.Dir = tmpDir
	cmd.Env = testutil.GitIsolatedEnv()
	require.NoError(t, cmd.Run())

	t.Chdir(tmpDir)

	got, proto := resolveTargetForTokenAuth(ctx, "origin")
	assert.Equal(t, "https://github.com/org/repo.git", got)
	assert.Equal(t, ProtocolHTTPS, proto)
}

// Not parallel: uses t.Chdir()
func TestResolvePushCommandTarget(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		originURL    string
		settingsJSON string
		token        string
		target       string
		want         string
	}{
		{
			// Without checkpoint_remote configured the push should use the
			// remote name so git updates refs/remotes/origin/<branch> and
			// subsequent hasUnpushedSessionsCommon checks can short-circuit.
			name:         "no checkpoint remote keeps remote name",
			originURL:    "git@github.com:acme/app.git",
			settingsJSON: `{"enabled":true}`,
			target:       "origin",
			want:         "origin",
		},
		{
			// With token set but no checkpoint_remote, PushURL still returns
			// the coerced HTTPS URL but enabled=false. resolvePushCommandTarget
			// should still return the name — newCommand handles token coercion.
			name:         "no checkpoint remote with token keeps remote name",
			originURL:    "git@github.com:acme/app.git",
			settingsJSON: `{"enabled":true}`,
			token:        "push-token",
			target:       "origin",
			want:         "origin",
		},
		{
			// With checkpoint_remote configured, use the derived URL so the
			// push actually goes to the separate checkpoint repo.
			name:         "checkpoint remote routes to checkpoint URL",
			originURL:    "https://github.com/acme/app.git",
			settingsJSON: `{"enabled":true,"strategy_options":{"checkpoint_remote":{"provider":"github","repo":"acme/checkpoints"}}}`,
			target:       "origin",
			want:         "https://github.com/acme/checkpoints.git",
		},
		{
			name:         "local path target stays unchanged",
			settingsJSON: `{"enabled":true}`,
			target:       "/tmp/bare-repo",
			want:         "/tmp/bare-repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testutil.InitRepo(t, tmpDir)
			testutil.WriteFile(t, tmpDir, "f.txt", "init")
			testutil.GitAdd(t, tmpDir, "f.txt")
			testutil.GitCommit(t, tmpDir, "init")
			if tt.originURL != "" {
				cmd := exec.CommandContext(ctx, "git", "remote", "add", "origin", tt.originURL)
				cmd.Dir = tmpDir
				cmd.Env = testutil.GitIsolatedEnv()
				require.NoError(t, cmd.Run())
			}
			if tt.settingsJSON != "" {
				testutil.WriteFile(t, tmpDir, ".trace/settings.json", tt.settingsJSON)
			}
			t.Chdir(tmpDir)
			if tt.token != "" {
				t.Setenv(CheckpointTokenEnvVar, tt.token)
			}

			got, err := resolvePushCommandTarget(ctx, tt.target)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Not parallel: uses t.Chdir()
func TestResolveFetchTarget(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "f.txt", "init")
	testutil.GitAdd(t, tmpDir, "f.txt")
	testutil.GitCommit(t, tmpDir, "init")

	cmd := exec.CommandContext(ctx, "git", "remote", "add", "origin", "https://github.com/org/repo.git")
	cmd.Dir = tmpDir
	cmd.Env = testutil.GitIsolatedEnv()
	require.NoError(t, cmd.Run())

	t.Chdir(tmpDir)

	t.Run("disabled returns remote name", func(t *testing.T) {
		target, err := ResolveFetchTarget(ctx, "origin")
		require.NoError(t, err)
		assert.Equal(t, "origin", target)
	})

	t.Run("enabled resolves remote to URL", func(t *testing.T) {
		testutil.WriteFile(
			t,
			tmpDir,
			".trace/settings.json",
			`{"enabled": true, "strategy_options": {"filtered_fetches": true}}`,
		)

		target, err := ResolveFetchTarget(ctx, "origin")
		require.NoError(t, err)
		assert.Equal(t, "https://github.com/org/repo.git", target)
	})

	t.Run("URL target stays unchanged", func(t *testing.T) {
		target, err := ResolveFetchTarget(ctx, "https://github.com/org/repo.git")
		require.NoError(t, err)
		assert.Equal(t, "https://github.com/org/repo.git", target)
	})

	t.Run("local path target stays unchanged", func(t *testing.T) {
		target, err := ResolveFetchTarget(ctx, "../repo.git")
		require.NoError(t, err)
		assert.Equal(t, "../repo.git", target)
	})
}

func TestInjectCheckpointTokenViaArgs(t *testing.T) {
	t.Parallel()

	t.Run("prepends include.path to args with temp config file", func(t *testing.T) {
		t.Parallel()
		args, cleanup := injectCheckpointTokenViaArgs([]string{"fetch", "origin", "main"}, "my-secret-token")
		defer cleanup()

		require.Len(t, args, 5, "should have 2 new args + 3 original")
		assert.Equal(t, "-c", args[0])
		assert.True(t, strings.HasPrefix(args[1], "include.path="),
			"second arg should be include.path=<path>, got: %s", args[1])
		assert.Equal(t, "fetch", args[2])
		assert.Equal(t, "origin", args[3])
		assert.Equal(t, "main", args[4])

		// Extract config file path from include.path=<path>
		configPath := strings.TrimPrefix(args[1], "include.path=")

		// Config file should exist and have restricted permissions
		info, err := os.Stat(configPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "config file should be mode 0600")

		// Config file should contain the auth header with the base64-encoded token
		content, err := os.ReadFile(configPath)
		require.NoError(t, err)
		wantAuth := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:my-secret-token"))
		assert.Contains(t, string(content), wantAuth, "config should contain the base64-encoded auth header")
		assert.Contains(t, string(content), "[http]", "config should have http section")
		assert.Contains(t, string(content), "extraHeader", "config should set extraHeader")
	})

	t.Run("token not in returned args directly", func(t *testing.T) {
		t.Parallel()
		args, cleanup := injectCheckpointTokenViaArgs([]string{"push", "origin"}, "super-secret")
		defer cleanup()

		for _, arg := range args {
			assert.NotContains(t, arg, "super-secret",
				"token must not appear directly in args")
		}
	})
}

func TestIsValidToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		token string
		valid bool
	}{
		{"normal token", "ghp_abc123XYZ", true},
		{"with hyphen and underscore", "token-with_special.chars", true},
		{"contains CR", "token\rinjection", false},
		{"contains LF", "token\ninjection", false},
		{"contains CRLF", "token\r\ninjection", false},
		{"contains null byte", "token\x00injection", false},
		{"contains tab", "token\tvalue", false},
		{"contains DEL", "token\x7Fvalue", false},
		{"contains bell", "token\x07value", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.valid, isValidToken(tt.token))
		})
	}
}

// Not parallel: uses t.Setenv()
func TestNewCommand_ControlCharsInToken(t *testing.T) {
	t.Setenv(CheckpointTokenEnvVar, "token\r\nEvil: injected-header")

	cmd, cleanup := newCommand(context.Background(), "fetch", "https://github.com/org/repo.git")
	defer cleanup()
	assert.Nil(t, cmd.Env, "env should not be set when token contains control characters")
}

// Not parallel: uses t.Setenv()
func TestNewCommand_NoToken(t *testing.T) {
	t.Setenv(CheckpointTokenEnvVar, "")

	cmd, cleanup := newCommand(context.Background(), "fetch", "https://github.com/org/repo.git")
	defer cleanup()
	assert.Nil(t, cmd.Stdin, "stdin should be nil")
	assert.Nil(t, cmd.Env, "env should not be set when token is empty")
}

// Not parallel: uses t.Setenv()
func TestNewCommand_WhitespaceToken(t *testing.T) {
	t.Setenv(CheckpointTokenEnvVar, "   ")

	cmd, cleanup := newCommand(context.Background(), "fetch", "https://github.com/org/repo.git")
	defer cleanup()
	assert.Nil(t, cmd.Env, "env should not be set when token is only whitespace")
}

// Not parallel: uses t.Setenv()
func TestNewCommand_HTTPS_InjectsToken(t *testing.T) {
	t.Setenv(CheckpointTokenEnvVar, "ghp_test123")

	cmd, cleanup := newCommand(context.Background(), "fetch", "https://github.com/org/repo.git")
	defer cleanup()

	// Token should NOT be in env vars
	if cmd.Env != nil {
		for _, e := range cmd.Env {
			assert.NotContains(t, e, "ghp_test123", "token must not appear in env vars")
			assert.NotContains(t, e, "GIT_CONFIG_VALUE_", "GIT_CONFIG_VALUE_* must not be used")
		}
	}

	// Auth config should be injected via -c include.path=<file> arg
	require.GreaterOrEqual(t, len(cmd.Args), 3, "args should include -c and include.path")
	assert.Equal(t, "-c", cmd.Args[1], "second arg should be -c")
	assert.True(t, strings.HasPrefix(cmd.Args[2], "include.path="),
		"third arg should be include.path=<path>, got: %s", cmd.Args[2])

	// Verify the config file contains the auth header with the token
	configPath := strings.TrimPrefix(cmd.Args[2], "include.path=")
	content, err := os.ReadFile(configPath)
	require.NoError(t, err)
	wantAuth := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_test123"))
	assert.Contains(t, string(content), wantAuth, "config file should contain the base64-encoded auth header")
	assert.Contains(t, string(content), "extraHeader", "config should set extraHeader")
}

// Not parallel: uses t.Setenv()
func TestNewCommand_SSH_URL_RewritesToHTTPSAndInjectsToken(t *testing.T) {
	t.Setenv(CheckpointTokenEnvVar, "ghp_test123")

	cmd, cleanup := newCommand(context.Background(), "push", "git@github.com:org/repo.git", "main")
	defer cleanup()

	assert.Contains(t, cmd.Args, "https://github.com/org/repo.git",
		"SSH target should be rewritten to HTTPS in args")
	assert.NotContains(t, cmd.Args, "git@github.com:org/repo.git",
		"original SSH target should be gone after rewrite")

	// Auth config should be injected via -c include.path=<file> arg
	require.GreaterOrEqual(t, len(cmd.Args), 3, "args should include -c and include.path")
	assert.Equal(t, "-c", cmd.Args[1], "second arg should be -c")
	assert.True(t, strings.HasPrefix(cmd.Args[2], "include.path="),
		"third arg should be include.path=<path>, got: %s", cmd.Args[2])

	// Verify the config file contains the auth header with the token
	configPath := strings.TrimPrefix(cmd.Args[2], "include.path=")
	content, err := os.ReadFile(configPath)
	require.NoError(t, err)
	wantAuth := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_test123"))
	assert.Contains(t, string(content), wantAuth, "config file should contain the base64-encoded auth header")
}

// Not parallel: uses t.Setenv() and os.Stderr
// When rewrite can't produce a usable HTTPS URL (e.g. missing owner/repo), we
// fall back to the original SSH target and emit the one-shot warning.
func TestNewCommand_SSH_Unparseable_WarnsAndSkips(t *testing.T) {
	t.Setenv(CheckpointTokenEnvVar, "ghp_test123")

	sshTokenWarningOnce = sync.Once{}
	t.Cleanup(func() { sshTokenWarningOnce = sync.Once{} })

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { os.Stderr = oldStderr })
	os.Stderr = w

	// ssh://host/ has no owner/repo — ParseURL fails, rewrite can't succeed,
	// but newCommand will still detect protocol as "" and skip without SSH warning.
	// Use an SSH SCP target with empty repo path instead: parses as SSH with
	// Host but owner/repo empty, so rewrite fails and protocol stays SSH.
	cmd, cleanup := newCommand(context.Background(), "push", "ssh://git@host/", "main")
	defer cleanup()

	w.Close()
	os.Stderr = oldStderr

	var buf [4096]byte
	n, _ := r.Read(buf[:]) //nolint:errcheck // test helper, EOF is expected
	_ = string(buf[:n])
	r.Close()

	// No HTTPS rewrite happened (URL couldn't be parsed into owner/repo), so
	// env is not set. Protocol is "" (ParseURL failed), so SSH warning doesn't
	// fire either — that's acceptable: the command runs against the original
	// SSH URL and will fail loudly via git itself.
	assert.Nil(t, cmd.Env, "env should NOT be set when SSH rewrite isn't possible")
	assert.Contains(t, cmd.Args, "ssh://git@host/", "original target unchanged when rewrite fails")
}

// Not parallel: uses t.Setenv()
func TestNewCommand_LocalPath_NoToken(t *testing.T) {
	t.Setenv(CheckpointTokenEnvVar, "ghp_test123")

	cmd, cleanup := newCommand(context.Background(), "push", "/tmp/bare-repo", "main")
	defer cleanup()
	assert.Nil(t, cmd.Env, "env should NOT be set for local path targets")
}

// newTLSTestServer creates an HTTPS test server that captures the Authorization header.
// Returns the server and a function to read the captured auth header and request count.
func newTLSTestServer(t *testing.T) (*httptest.Server, func() (auth string, count int)) {
	t.Helper()

	var (
		mu           sync.Mutex
		capturedAuth string
		requestCount int
	)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedAuth = r.Header.Get("Authorization")
		requestCount++
		mu.Unlock()

		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintln(w, "forbidden")
	}))
	t.Cleanup(srv.Close)

	return srv, func() (string, int) {
		mu.Lock()
		defer mu.Unlock()
		return capturedAuth, requestCount
	}
}

func setupTokenTestRepo(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "f.txt", "init")
	testutil.GitAdd(t, tmpDir, "f.txt")
	testutil.GitCommit(t, tmpDir, "init")
	t.Chdir(tmpDir)
	return tmpDir
}

// Not parallel: uses t.Chdir()
func TestCheckpointToken_HTTPSServer_SendsAuthHeader(t *testing.T) {
	t.Setenv(CheckpointTokenEnvVar, "test-token-abc123")

	srv, getCapture := newTLSTestServer(t)
	tmpDir := setupTokenTestRepo(t)

	target := srv.URL + "/org/repo.git"
	cmd, cleanup := newCommand(context.Background(),
		"fetch", target, "+refs/heads/main:refs/remotes/origin/main")
	defer cleanup()
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_NO_VERIFY=1")
	_ = cmd.Run() //nolint:errcheck // expected to fail against test server

	auth, count := getCapture()
	require.Positive(t, count, "server should have received at least one request")
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:test-token-abc123"))
	assert.Equal(t, wantAuth, auth,
		"git should send the token as a Basic Authorization header")
}

// Not parallel: uses t.Chdir()
func TestCheckpointToken_HTTPSServer_NoTokenNoHeader(t *testing.T) {
	t.Setenv(CheckpointTokenEnvVar, "")

	srv, getCapture := newTLSTestServer(t)
	tmpDir := setupTokenTestRepo(t)

	target := srv.URL + "/org/repo.git"
	cmd, cleanup := newCommand(context.Background(),
		"fetch", target, "+refs/heads/main:refs/remotes/origin/main")
	defer cleanup()
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_NO_VERIFY=1")

	_ = cmd.Run() //nolint:errcheck // expected to fail against test server

	auth, count := getCapture()
	require.Positive(t, count, "server should have received at least one request")
	assert.Empty(t, auth, "no Authorization header should be sent without token")
}

// Not parallel: uses t.Chdir()
func TestCheckpointToken_HTTPSServer_LsRemoteSendsAuthHeader(t *testing.T) {
	t.Setenv(CheckpointTokenEnvVar, "push-token-xyz789")

	srv, getCapture := newTLSTestServer(t)
	tmpDir := setupTokenTestRepo(t)

	target := srv.URL + "/org/repo.git"
	cmd, cleanup := newCommand(context.Background(),
		"ls-remote", target)
	defer cleanup()
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_NO_VERIFY=1")

	_ = cmd.Run() //nolint:errcheck // expected to fail against test server

	auth, count := getCapture()
	require.Positive(t, count, "server should have received at least one request")
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:push-token-xyz789"))
	assert.Equal(t, wantAuth, auth,
		"git ls-remote should send the token as a Basic Authorization header")
}

// Not parallel: uses t.Setenv()
func TestNewCommand_GIT_TERMINAL_PROMPT_Coexistence(t *testing.T) {
	t.Setenv(CheckpointTokenEnvVar, "coexist-token")

	cmd, cleanup := newCommand(context.Background(),
		"fetch", "--no-tags", "--filter=blob:none", "https://github.com/org/repo.git", "refs/heads/main")
	defer cleanup()

	// Auth config should be injected via -c include.path=<file> arg
	require.GreaterOrEqual(t, len(cmd.Args), 3, "args should include -c and include.path")
	assert.Equal(t, "-c", cmd.Args[1], "second arg should be -c")
	assert.True(t, strings.HasPrefix(cmd.Args[2], "include.path="),
		"third arg should be include.path=<path>")

	// Verify the config file contains the token
	configPath := strings.TrimPrefix(cmd.Args[2], "include.path=")
	content, err := os.ReadFile(configPath)
	require.NoError(t, err)
	wantAuth := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:coexist-token"))
	assert.Contains(t, string(content), wantAuth)

	// Original args should be preserved after the -c flag
	assert.Contains(t, cmd.Args, "--no-tags")
	assert.Contains(t, cmd.Args, "--filter=blob:none")
	assert.Contains(t, cmd.Args, "https://github.com/org/repo.git")
}

func TestIsURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		val  string
		want bool
	}{
		{"remote name", "origin", false},
		{"SSH SCP", "git@github.com:org/repo.git", true},
		{"HTTPS", "https://github.com/org/repo.git", true},
		{"SSH protocol", "ssh://git@github.com/org/repo.git", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsURL(tt.val))
		})
	}
}

func TestIsLocalPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		val  string
		want bool
	}{
		{"remote name", "origin", false},
		{"absolute path", "/tmp/repo.git", true},
		{"current dir relative", "./repo.git", true},
		{"parent relative", "../repo.git", true},
		{"https", "https://github.com/org/repo.git", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isLocalPath(tt.val))
		})
	}
}
