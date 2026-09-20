package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeQueueProcessesPolicyCheckedPullRequest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/queue", false); err != nil {
		t.Fatal(err)
	}
	repoPath, _ := a.store.repoPath("team/queue")
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "base")
	gitAdminPush(t, work, repoPath, "main")
	gitTest(t, work, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("queued\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "commit", "-am", "feature")
	gitAdminPush(t, work, repoPath, "feature")
	pr, err := a.store.createPullRequest("team/queue", "Queue me", "", "alice", "feature", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.setRepoPolicy("team/queue", repoPolicy{RequiredApprovals: 0}); err != nil {
		t.Fatal(err)
	}
	entry, err := a.store.enqueueMerge("team/queue", pr.ID, "ff", "admin")
	if err != nil || entry.State != "queued" {
		t.Fatalf("enqueue: %+v %v", entry, err)
	}
	if _, err := a.store.enqueueMerge("team/queue", pr.ID, "ff", "admin"); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("duplicate queue entry accepted: %v", err)
	}
	processed, err := a.store.processMergeQueue("team/queue", "admin")
	if err != nil || processed.State != "merged" {
		t.Fatalf("process: %+v %v", processed, err)
	}
	entries, err := a.store.listMergeQueue("team/queue")
	if err != nil || len(entries) != 1 || entries[0].State != "merged" {
		t.Fatalf("queue persistence: %+v %v", entries, err)
	}
	updated, err := a.store.loadPullRequests()
	if err != nil || updated.Repos["team/queue"][0].State != "merged" {
		t.Fatalf("pull request was not merged: %+v %v", updated.Repos, err)
	}
}
