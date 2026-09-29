package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTransferRewritesOnlyRepositoryReferences reproduces the byte-level
// rewrite: free text equal to the old name was changed, and the append-only
// audit ledger was rewritten.
func TestTransferRewritesOnlyRepositoryReferences(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.createRepo("team/old", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addUser("alice", false); err != nil {
		t.Fatal(err)
	}
	if err := s.grantUser("alice", "team/old", "write"); err != nil {
		t.Fatal(err)
	}
	created, err := s.createIssue("team/old", "team/old", "Rename team/old soon", "alice", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.setStar("team/old", "alice", true); err != nil {
		t.Fatal(err)
	}
	if err := s.setSecret("team/old", "DEPLOY", "value"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishPackage("team/old", "lib", "1.0.0", "lib.tgz", "alice", strings.NewReader("package bytes"), int64(len("package bytes"))); err != nil {
		t.Fatal(err)
	}
	if err := s.recordAudit("alice", "issue.create", "team/old", "1", map[string]any{"title": "team/old"}); err != nil {
		t.Fatal(err)
	}
	actions := actionDB{NextRunID: 2, LastScheduledAt: map[string]time.Time{"team/old\x00main": time.Now().UTC()}, Runs: []actionRun{{ID: 1, Repo: "team/old", Status: "success"}}}
	if err := s.saveActions(actions); err != nil {
		t.Fatal(err)
	}
	auditBefore, err := os.ReadFile(filepath.Join(root, auditFile))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.transferRepo("team/old", "team/new"); err != nil {
		t.Fatal(err)
	}

	issues, err := s.loadIssues()
	if err != nil {
		t.Fatal(err)
	}
	moved := issues.Repos["team/new"]
	if len(issues.Repos["team/old"]) != 0 || len(moved) != 1 || moved[0].ID != created.ID {
		t.Fatalf("issues were not moved to the new name: %+v", issues.Repos)
	}
	if moved[0].Title != "team/old" || moved[0].Body != "Rename team/old soon" || moved[0].Repo != "team/new" {
		t.Fatalf("issue text changed or repo field not updated: %+v", moved[0])
	}
	auditAfter, err := os.ReadFile(filepath.Join(root, auditFile))
	if err != nil || !bytes.Equal(auditBefore, auditAfter) {
		t.Fatalf("the append-only audit ledger was rewritten: %v", err)
	}
	users, _ := s.loadUsers()
	if users.Users["alice"].Repos["team/new"] != "write" || users.Users["alice"].Repos["team/old"] != "" {
		t.Fatalf("grant not moved: %+v", users.Users["alice"].Repos)
	}
	secrets, err := s.actionSecrets("team/new")
	if err != nil || secrets["TRACE_SECRET_DEPLOY"] != "value" {
		t.Fatalf("secrets not moved: %v %v", secrets, err)
	}
	loaded, err := s.loadActions()
	if err != nil || loaded.Runs[0].Repo != "team/new" || loaded.LastScheduledAt["team/new\x00main"].IsZero() {
		t.Fatalf("action state not moved: %+v %v", loaded, err)
	}
	newPackage, err := s.packagePath("team/new", "lib", "1.0.0", "lib.tgz")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(newPackage); err != nil || string(b) != "package bytes" {
		t.Fatalf("package files were orphaned by the transfer: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, transferJournalFile)); !os.IsNotExist(err) {
		t.Fatalf("transfer journal left behind: %v", err)
	}
	var starsCheck map[string]any
	raw, _ := os.ReadFile(filepath.Join(root, "stars.json"))
	if err := json.Unmarshal(raw, &starsCheck); err != nil || strings.Contains(string(raw), `"team/old"`) {
		t.Fatalf("stars.json still references the old name: %s", raw)
	}
}

func TestInterruptedTransferCompletesOnOpen(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.createRepo("team/old", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addUser("alice", false); err != nil {
		t.Fatal(err)
	}
	if err := s.grantUser("alice", "team/old", "read"); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the journal was written and the Git directory
	// was renamed, but before metadata was rewritten.
	journal, _ := json.Marshal(transferJournal{Source: "team/old", Target: "team/new"})
	if err := os.WriteFile(filepath.Join(root, transferJournalFile), journal, 0600); err != nil {
		t.Fatal(err)
	}
	oldPath, _ := s.repoPath("team/old")
	newPath, _ := s.repoPath("team/new")
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	users, _ := reopened.loadUsers()
	if users.Users["alice"].Repos["team/new"] != "read" || users.Users["alice"].Repos["team/old"] != "" {
		t.Fatalf("interrupted transfer was not completed: %+v", users.Users["alice"].Repos)
	}
	if _, err := os.Stat(filepath.Join(root, transferJournalFile)); !os.IsNotExist(err) {
		t.Fatalf("journal not removed after recovery: %v", err)
	}
}
