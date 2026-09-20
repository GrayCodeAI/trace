package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportLocalRepositoryInstallsTraceProtection(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, source, "init", "--initial-branch=main")
	gitTest(t, source, "config", "user.name", "Importer")
	gitTest(t, source, "config", "user.email", "importer@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("imported\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, source, "add", ".")
	gitTest(t, source, "commit", "-m", "initial")
	store, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.importRepo(source, "team/imported"); err != nil {
		t.Fatal(err)
	}
	repoPath, _ := store.repoPath("team/imported")
	if got := strings.TrimSpace(gitTest(t, t.TempDir(), "--git-dir", repoPath, "show", "--format=%s", "--no-patch", "main")); got != "initial" {
		t.Fatalf("imported commit: %q", got)
	}
	if !hasManagedHook(repoPath) {
		t.Fatal("import did not install Trace receive hook")
	}
	if err := store.importRepo(source, "team/imported"); err == nil {
		t.Fatal("duplicate import was accepted")
	}
}
