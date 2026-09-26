package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseActionsMode(t *testing.T) {
	for input, want := range map[string]string{"": actionsModeSandboxed, "sandboxed": actionsModeSandboxed, "OFF": actionsModeOff, " trusted ": actionsModeTrusted} {
		got, err := parseActionsMode(input)
		if err != nil || got != want {
			t.Fatalf("parseActionsMode(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := parseActionsMode("yes"); err == nil {
		t.Fatal("unknown actions mode accepted")
	}
	if (&store{}).actionsPolicy() != actionsModeSandboxed {
		t.Fatal("an unset actions mode must default to sandboxed")
	}
	if err := run([]string{"serve", "-data", t.TempDir(), "-actions", "always"}); err == nil || !strings.Contains(err.Error(), "-actions") {
		t.Fatalf("serve accepted an invalid -actions value: %v", err)
	}
}

// pushWorkflow commits a workflow file and pushes it through Trace's HTTP Git
// endpoint, which is what auto-triggers runs.
func pushWorkflow(t *testing.T, a *app, root, repo, workflow string) {
	t.Helper()
	server := httptest.NewServer(a)
	defer server.Close()
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.MkdirAll(filepath.Join(work, ".trace"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".trace", "workflow.json"), []byte(workflow+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "workflow")
	gitTest(t, work, "push", strings.Replace(server.URL, "http://", "http://admin:"+adminToken(t, root)+"@", 1)+"/git/"+repo+".git", "main")
}

// TestActionsPolicyIsOperatorControlled checks that repository content cannot
// opt out of the operator's runner policy: by default only sandboxed
// workflows run, and `-actions off` disables push, manual, and scheduled runs.
func TestActionsPolicyIsOperatorControlled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/untrusted", false); err != nil {
		t.Fatal(err)
	}
	pushWorkflow(t, a, root, "team/untrusted", `{"name":"escape","schedule":"1m","jobs":[{"name":"pwn","run":["id > pwned.txt"]}]}`)
	if runs, err := a.store.listActionRuns("team/untrusted"); err != nil || len(runs) != 0 {
		t.Fatalf("default policy queued an unsandboxed workflow on push: %+v %v", runs, err)
	}
	trigger := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/repos/team/untrusted/actions/runs", strings.NewReader(`{"ref":"main"}`))
		req.SetBasicAuth("admin", adminToken(t, root))
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		return res
	}
	if res := trigger(); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "sandboxed") {
		t.Fatalf("manual unsandboxed run under the default policy: %d %s", res.Code, res.Body.String())
	}
	if err := a.store.scheduleActionRuns(); err != nil {
		t.Fatal(err)
	}
	if runs, _ := a.store.listActionRuns("team/untrusted"); len(runs) != 0 {
		t.Fatalf("scheduler queued an unsandboxed workflow under the default policy: %+v", runs)
	}

	a.store.actionsMode = actionsModeOff
	if err := a.store.createRepo("team/sandboxed", false); err != nil {
		t.Fatal(err)
	}
	pushWorkflow(t, a, root, "team/sandboxed", `{"name":"boxed","sandbox":true,"sandbox_runtime":"macos","jobs":[{"name":"test","run":["true"]}]}`)
	if runs, err := a.store.listActionRuns("team/sandboxed"); err != nil || len(runs) != 0 {
		t.Fatalf("actions off still queued a run on push: %+v %v", runs, err)
	}
	if _, err := a.store.actionRun("team/sandboxed", "main", "admin"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("actions off accepted a manual run: %v", err)
	}

	a.store.actionsMode = actionsModeSandboxed
	run, err := a.store.actionRun("team/sandboxed", "main", "admin")
	if err != nil {
		t.Fatalf("sandboxed policy refused a sandboxed workflow: %v", err)
	}
	waitForActionRun(t, a.store, run.ID)
}

func waitForActionRun(t *testing.T, s *store, id int) actionRun {
	t.Helper()
	var run actionRun
	for i := 0; i < 400; i++ {
		var err error
		run, err = actionRunByID(s, id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "queued" && run.Status != "running" {
			return run
		}
		sleepBriefly()
	}
	t.Fatalf("action run %d did not finish: %+v", id, run)
	return run
}

func sleepBriefly() { time.Sleep(25 * time.Millisecond) }
