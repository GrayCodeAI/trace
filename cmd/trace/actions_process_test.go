package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func readPID(t *testing.T, path string) int {
	t.Helper()
	for i := 0; i < 400; i++ {
		if b, err := os.ReadFile(path); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(b))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job never wrote %s", path)
	return 0
}

func waitProcessGone(t *testing.T, pid int, label string) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("%s process %d outlived its workflow step", label, pid)
}

// TestActionStepsDoNotLeaveProcessesBehind checks that background processes
// started by a local step are stopped when the step finishes (whether or not
// they hold its output open) and when a run is cancelled.
func TestActionStepsDoNotLeaveProcessesBehind(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	a.store.actionsMode = actionsModeTrusted
	if err := a.store.createRepo("team/procs", false); err != nil {
		t.Fatal(err)
	}
	pids := t.TempDir()
	workflow := `{"name":"procs","jobs":[{"name":"leftovers","run":[` +
		`"(sleep 120) >/dev/null 2>&1 & echo $! > ` + pids + `/detached",` +
		`"sleep 120 & echo $! > ` + pids + `/attached"` +
		`]}]}`
	pushWorkflow(t, a, root, "team/procs", workflow)
	runs, err := a.store.listActionRuns("team/procs")
	if err != nil || len(runs) != 1 {
		t.Fatalf("push did not queue one run: %+v %v", runs, err)
	}
	started := time.Now()
	run := waitForActionRunWithin(t, a.store, runs[0].ID, 60*time.Second)
	if run.Status != "success" {
		t.Fatalf("step that left background processes should still succeed: %+v", run)
	}
	if elapsed := time.Since(started); elapsed > 60*time.Second {
		t.Fatalf("step waited for its background processes: %s", elapsed)
	}
	waitProcessGone(t, readPID(t, filepath.Join(pids, "detached")), "detached background")
	waitProcessGone(t, readPID(t, filepath.Join(pids, "attached")), "attached background")

	if err := a.store.createRepo("team/cancel", false); err != nil {
		t.Fatal(err)
	}
	cancelPIDs := t.TempDir()
	pushWorkflow(t, a, root, "team/cancel", `{"name":"cancel","jobs":[{"name":"long","run":["sleep 120 & echo $! > `+cancelPIDs+`/child; sleep 120"]}]}`)
	runs, err = a.store.listActionRuns("team/cancel")
	if err != nil || len(runs) != 1 {
		t.Fatalf("push did not queue one run: %+v %v", runs, err)
	}
	child := readPID(t, filepath.Join(cancelPIDs, "child"))
	if !cancelActionRun(runs[0].ID) {
		t.Fatal("cancelActionRun returned false for a running run")
	}
	run = waitForActionRunWithin(t, a.store, runs[0].ID, 30*time.Second)
	if run.Status != "cancelled" {
		t.Fatalf("cancelled run status: %+v", run)
	}
	waitProcessGone(t, child, "cancelled step's background")
}

func waitForActionRunWithin(t *testing.T, s *store, id int, limit time.Duration) actionRun {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		run, err := actionRunByID(s, id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "queued" && run.Status != "running" {
			return run
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("action run %d did not finish within %s", id, limit)
	return actionRun{}
}
