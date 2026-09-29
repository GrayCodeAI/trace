package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestMaintainersCanUseWriterAPIs checks that the maintain role, a superset of
// write, is accepted by the action-run, secrets, and Pages APIs, while a
// read-only user is still refused.
func TestMaintainersCanUseWriterAPIs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	a.store.actionsMode = actionsModeTrusted
	if err := a.store.createRepo("team/app", false); err != nil {
		t.Fatal(err)
	}
	commitFiles(t, a.store, "team/app", map[string][]byte{"index.html": []byte("<p>app</p>"), ".trace/workflow.json": []byte(`{"name":"ci","jobs":[{"name":"test","run":["true"]}]}`)})
	tokens := map[string]string{}
	for user, role := range map[string]string{"mia": "maintain", "rita": "read"} {
		token, err := a.store.addUser(user, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.store.grantUser(user, "team/app", role); err != nil {
			t.Fatal(err)
		}
		tokens[user] = token
	}
	call := func(user, method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.SetBasicAuth(user, tokens[user])
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		return res.Code
	}
	requests := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/repos/team/app/actions/runs", `{"ref":"main"}`},
		{http.MethodGet, "/api/v1/repos/team/app/secrets", ""},
		{http.MethodPut, "/api/v1/repos/team/app/secrets/DEPLOY", `{"value":"s3cret-value"}`},
		{http.MethodPost, "/api/v1/repos/team/app/pages", `{"branch":"main"}`},
	}
	for _, rq := range requests {
		if code := call("mia", rq.method, rq.path, rq.body); code == http.StatusForbidden || code >= 400 {
			t.Fatalf("maintainer %s %s: %d", rq.method, rq.path, code)
		}
		if code := call("rita", rq.method, rq.path, rq.body); code != http.StatusForbidden {
			t.Fatalf("read-only user %s %s: %d", rq.method, rq.path, code)
		}
	}
	runs, err := a.store.listActionRuns("team/app")
	if err != nil || len(runs) != 1 {
		t.Fatalf("maintainer run was not queued: %+v %v", runs, err)
	}
	waitForActionRun(t, a.store, runs[0].ID)
}
