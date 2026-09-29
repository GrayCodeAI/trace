package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runReceiveHook feeds one ref update to a generated pre-receive hook.
func runReceiveHook(t *testing.T, dir, hook, ref string, admin bool) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pre-receive")
	if err := os.WriteFile(path, []byte(hook), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("0000000000000000000000000000000000000000 1111111111111111111111111111111111111111 " + ref + "\n")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TRACE_ADMIN=0"}
	if admin {
		cmd.Env[1] = "TRACE_ADMIN=1"
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestProtectedBranchPatternsCannotInjectShell(t *testing.T) {
	// The hook runs in dir; payloads create the relative file "pwned" there
	// (a short name keeps them within the 100-character pattern limit).
	dir := t.TempDir()
	marker := "pwned"
	injections := []string{
		// No spaces: the pre-fix validator accepted this and the hook ran it.
		"main)exit${IFS}0;;esac;touch${IFS}" + marker + ";case${IFS}x${IFS}in(x",
		"main) exit 0 ;; esac; touch " + marker + "; case x in (x",
		"main|refs/heads/*",
		"release/$(touch " + marker + ")",
		"release/`touch " + marker + "`",
		"main;touch " + marker,
		"re'lease",
		"../main",
		"release//x",
		"-main",
		"release/*/x",
	}
	for _, pattern := range injections {
		if _, err := normalizePolicy(repoPolicy{ProtectedBranches: []string{pattern}}); err == nil {
			t.Fatalf("protected branch pattern %q was accepted", pattern)
		}
		// Even a policy that bypassed validation must not execute as shell.
		hook := receiveHook(repoPolicy{ProtectedBranches: []string{pattern}})
		out, err := runReceiveHook(t, dir, hook, "refs/heads/feature", false)
		if _, statErr := os.Stat(filepath.Join(dir, marker)); statErr == nil {
			t.Fatalf("pattern %q executed shell code (hook output %q, err %v)", pattern, out, err)
		}
	}
	for _, pattern := range []string{"main", "release/*", "*", "v1.2_x-y", "team/feature"} {
		if _, err := normalizePolicy(repoPolicy{ProtectedBranches: []string{pattern}}); err != nil {
			t.Fatalf("valid pattern %q rejected: %v", pattern, err)
		}
	}
}

func TestReceiveHookEnforcesQuotedPatterns(t *testing.T) {
	hook := receiveHook(repoPolicy{ProtectedBranches: []string{"main", "release/*"}})
	for ref, allowed := range map[string]bool{
		"refs/heads/main":        false,
		"refs/heads/release/v1":  false,
		"refs/heads/feature/x":   true,
		"refs/heads/mainline":    true,
		"refs/tags/v1":           false,
		"refs/trace/agents/node": false,
		"refs/notes/commits":     false,
	} {
		_, err := runReceiveHook(t, t.TempDir(), hook, ref, false)
		if (err == nil) != allowed {
			t.Fatalf("non-admin update of %s: allowed=%v, want %v", ref, err == nil, allowed)
		}
		if _, err := runReceiveHook(t, t.TempDir(), hook, ref, true); err != nil {
			t.Fatalf("admin update of %s was refused: %v", ref, err)
		}
	}
	everything := receiveHook(repoPolicy{ProtectedBranches: []string{"*"}})
	if _, err := runReceiveHook(t, t.TempDir(), everything, "refs/heads/anything", false); err == nil {
		t.Fatal("wildcard protection allowed a non-admin branch update")
	}
}

// TestServerStartKeepsConfiguredBranchProtection covers ensureHooks, which
// `trace serve` runs at startup: it must reinstall each repository's hook
// from its stored policy instead of the default (main only), and it must
// fail closed when a stored policy is no longer valid.
func TestServerStartKeepsConfiguredBranchProtection(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"team/configured", "team/tampered"} {
		if err := s.createRepo(name, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.setRepoPolicy("team/configured", repoPolicy{RequiredApprovals: 1, ProtectedBranches: []string{"main", "release/*"}}); err != nil {
		t.Fatal(err)
	}
	// A policy stored by an older version with a pattern that is now invalid.
	if err := s.updatePolicies(func(db *policyDB) error {
		db.Repos["team/tampered"] = repoPolicy{ProtectedBranches: []string{"main)exit${IFS}0;;esac;case${IFS}x${IFS}in(x"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureHooks(); err != nil {
		t.Fatal(err)
	}
	hookFor := func(name string) string {
		path, _ := s.repoPath(name)
		b, err := os.ReadFile(filepath.Join(path, "hooks", "pre-receive"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if _, err := runReceiveHook(t, t.TempDir(), hookFor("team/configured"), "refs/heads/release/v1", false); err == nil {
		t.Fatal("configured release/* protection was dropped at server start")
	}
	if _, err := runReceiveHook(t, t.TempDir(), hookFor("team/configured"), "refs/heads/feature", false); err != nil {
		t.Fatalf("feature branch unexpectedly protected: %v", err)
	}
	if _, err := runReceiveHook(t, t.TempDir(), hookFor("team/tampered"), "refs/heads/feature", false); err == nil {
		t.Fatal("an invalid stored policy must fail closed and protect every branch")
	}
}
