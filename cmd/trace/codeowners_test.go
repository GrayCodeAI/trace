package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodeOwnerMatchesRecursivePatterns(t *testing.T) {
	tests := []struct {
		pattern string
		file    string
		want    bool
	}{
		{"*.go", "cmd/main.go", true},
		{"src/**/auth/*.go", "src/auth/login.go", true},
		{"src/**/auth/*.go", "src/internal/security/auth/login.go", true},
		{"src/**/auth/*.go", "src/internal/auth/login.txt", false},
		{"docs/", "docs/guide/start.md", true},
		{"docs/", "src/docs/guide.md", false},
	}
	for _, test := range tests {
		if got := codeOwnerMatches(test.pattern, test.file); got != test.want {
			t.Errorf("codeOwnerMatches(%q, %q) = %v, want %v", test.pattern, test.file, got, test.want)
		}
	}
}

func TestCodeOwnerApprovalRequiredForChangedFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/owners", false); err != nil {
		t.Fatal(err)
	}
	repoPath, _ := a.store.repoPath("team/owners")
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "CODEOWNERS"), []byte("*.go @alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "base")
	gitAdminPush(t, work, repoPath, "main")
	gitTest(t, work, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n\nfunc changed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", "main.go")
	gitTest(t, work, "commit", "-m", "change")
	gitAdminPush(t, work, repoPath, "feature")
	pr, err := a.store.createPullRequest("team/owners", "Owner review", "", "bob", "feature", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.setRepoPolicy("team/owners", repoPolicy{RequiredApprovals: 0, RequireCodeOwners: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.mergePullRequest("team/owners", pr.ID, "admin"); err == nil || !strings.Contains(err.Error(), "code owner") {
		t.Fatalf("merge without code owner approval succeeded or returned wrong error: %v", err)
	}
	if _, err := a.store.approvePullRequest("team/owners", pr.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.mergePullRequest("team/owners", pr.ID, "admin"); err != nil {
		t.Fatalf("merge with code owner approval failed: %v", err)
	}
}
