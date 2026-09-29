package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestScheduleOnFreshNodeDoesNotPanic covers a node whose first action run is
// a scheduled one: actions.json does not exist yet, and the scheduler must not
// write into a nil map (a panic there terminates the whole server process).
func TestScheduleOnFreshNodeDoesNotPanic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	s.actionsMode = actionsModeTrusted
	if err := s.createRepo("team/nightly", false); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.MkdirAll(filepath.Join(work, ".trace"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".trace", "workflow.json"), []byte(`{"name":"nightly","schedule":"1m","jobs":[{"name":"test","run":["true"]}]}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "nightly")
	repoPath, _ := s.repoPath("team/nightly")
	push := exec.Command("git", "-C", work, "push", repoPath, "main")
	push.Env = append(os.Environ(), "TRACE_ADMIN=1")
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("seed repository: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "actions.json")); !os.IsNotExist(err) {
		t.Fatalf("precondition: actions.json should not exist yet: %v", err)
	}
	if err := s.scheduleActionRuns(); err != nil {
		t.Fatal(err)
	}
	runs, err := s.listActionRuns("team/nightly")
	if err != nil || len(runs) != 1 || runs[0].TriggeredBy != "scheduler" {
		t.Fatalf("scheduler did not queue exactly one run: %+v %v", runs, err)
	}
	for i := 0; i < 400; i++ {
		run, err := actionRunByID(s, runs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "queued" && run.Status != "running" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("scheduled run did not finish")
}
