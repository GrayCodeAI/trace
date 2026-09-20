package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCancelActionRunSignalsRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	actionCancelMu.Lock()
	actionCancels[77] = cancel
	actionCancelMu.Unlock()
	defer func() {
		actionCancelMu.Lock()
		delete(actionCancels, 77)
		actionCancelMu.Unlock()
	}()
	if !cancelActionRun(77) {
		t.Fatal("cancelActionRun returned false")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("runner context was not cancelled")
	}
}

func TestSandboxedActionCommandDoesNotFallback(t *testing.T) {
	cmd, err := actionCommand(context.Background(), "printf ok", filepath.Join(t.TempDir(), "workspace"), true)
	if runtime.GOOS != "darwin" {
		if err == nil {
			t.Fatal("sandboxed action unexpectedly has an unsafe fallback")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Args) < 1 || cmd.Args[0] != "sandbox-exec" {
		t.Fatalf("sandbox command unexpectedly used an unsafe runner: %v", cmd.Args)
	}
}

func TestDockerSandboxCommandShape(t *testing.T) {
	if !commandAvailable("docker") {
		t.Skip("docker client is not installed")
	}
	cmd, err := actionCommandWithSandbox(context.Background(), "printf ok", t.TempDir(), true, "docker", "alpine:3.20")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, expected := range []string{"--network none", "--read-only", "--cap-drop ALL", "--security-opt no-new-privileges", "alpine:3.20"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("docker sandbox missing %q: %s", expected, joined)
		}
	}
}

func TestLocalActionRunAndArtifact(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/ci", false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	token := adminToken(t, root)
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.MkdirAll(filepath.Join(work, ".trace"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.store.setSecret("team/ci", "TOKEN", "ok"); err != nil {
		t.Fatal(err)
	}
	workflow := `{"name":"local","jobs":[{"name":"test","run":["printf %s \"$TRACE_SECRET_TOKEN\" > result.txt"],"artifacts":["result.txt"]}]}`
	if err := os.WriteFile(filepath.Join(work, ".trace", "workflow.json"), []byte(workflow+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "add workflow")
	remote := strings.Replace(server.URL, "http://", "http://admin:"+token+"@", 1) + "/git/team/ci.git"
	gitTest(t, work, "remote", "add", "origin", remote)
	gitTest(t, work, "push", "origin", "main")
	runs, err := a.store.listActionRuns("team/ci")
	if err != nil || len(runs) == 0 {
		t.Fatalf("push did not trigger workflow: runs=%#v err=%v", runs, err)
	}
	pageReq := httptest.NewRequest(http.MethodGet, "/repos/team/ci/actions", nil)
	pageReq.SetBasicAuth("admin", token)
	pageRes := httptest.NewRecorder()
	a.ServeHTTP(pageRes, pageReq)
	if pageRes.Code != http.StatusOK || !strings.Contains(pageRes.Body.String(), "Run workflow") {
		t.Fatalf("actions web page: got %d", pageRes.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repos/team/ci/actions/runs", strings.NewReader(`{"ref":"main"}`))
	req.SetBasicAuth("admin", token)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("trigger action: got %d: %s", res.Code, res.Body.String())
	}
	var run actionRun
	if err := json.Unmarshal(res.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, err = actionRunByID(a.store, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "queued" && run.Status != "running" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if run.Status != "success" || len(run.Jobs) != 1 || len(run.Jobs[0].Artifacts) != 1 {
		t.Fatalf("unexpected action result: %+v", run)
	}
	artifact := run.Jobs[0].Artifacts[0]
	b, err := os.ReadFile(artifact.Path)
	if err != nil || string(b) != "ok" {
		t.Fatalf("artifact: %v %q", err, b)
	}
}

func TestScheduledWorkflowQueuesOncePerInterval(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/scheduled", false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.MkdirAll(filepath.Join(work, ".trace"), 0700); err != nil {
		t.Fatal(err)
	}
	workflow := `{"name":"scheduled","schedule":"1m","jobs":[{"name":"test","run":["true"]}]}`
	if err := os.WriteFile(filepath.Join(work, ".trace", "workflow.json"), []byte(workflow+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "scheduled workflow")
	remote := strings.Replace(server.URL, "http://", "http://admin:"+adminToken(t, root)+"@", 1) + "/git/team/scheduled.git"
	gitTest(t, work, "remote", "add", "origin", remote)
	gitTest(t, work, "push", "origin", "main")
	if err := a.store.scheduleActionRuns(); err != nil {
		t.Fatal(err)
	}
	if err := a.store.scheduleActionRuns(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := a.store.listActionRuns("team/scheduled")
		if err != nil {
			t.Fatal(err)
		}
		finished := false
		for _, run := range runs {
			if run.TriggeredBy == "scheduler" && (run.Status == "success" || run.Status == "failure" || run.Status == "cancelled") {
				finished = true
			}
		}
		if finished {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	runs, err := a.store.listActionRuns("team/scheduled")
	if err != nil {
		t.Fatal(err)
	}
	scheduled := 0
	for _, run := range runs {
		if run.TriggeredBy == "scheduler" {
			scheduled++
		}
	}
	if scheduled != 1 {
		t.Fatalf("unexpected scheduled runs: %+v", runs)
	}
}
