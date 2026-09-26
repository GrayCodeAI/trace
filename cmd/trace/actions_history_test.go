package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestActionLogsLiveOutsideTheRunDatabase checks that job logs are stored per
// run instead of inside actions.json, that API list responses stay small, and
// that the single-run API and the actions page still show the log and a
// working artifact link.
func TestActionLogsLiveOutsideTheRunDatabase(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	a.store.actionsMode = actionsModeTrusted
	if err := a.store.createRepo("team/logs", false); err != nil {
		t.Fatal(err)
	}
	pushWorkflow(t, a, root, "team/logs", `{"name":"logs","jobs":[{"name":"build","run":["echo distinctive-log-line","printf ok > out.txt"],"artifacts":["out.txt"]}]}`)
	runs, err := a.store.listActionRuns("team/logs")
	if err != nil || len(runs) != 1 {
		t.Fatalf("push did not queue one run: %+v %v", runs, err)
	}
	run := waitForActionRun(t, a.store, runs[0].ID)
	if run.Status != "success" || !strings.Contains(run.Jobs[0].Log, "distinctive-log-line") {
		t.Fatalf("single-run lookup lost the job log: %+v", run)
	}
	raw, err := os.ReadFile(filepath.Join(root, "actions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "distinctive-log-line") {
		t.Fatal("job log is still embedded in actions.json")
	}
	token := adminToken(t, root)
	list := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/logs/actions/runs", nil)
	list.SetBasicAuth("admin", token)
	listRes := httptest.NewRecorder()
	a.ServeHTTP(listRes, list)
	if listRes.Code != http.StatusOK || strings.Contains(listRes.Body.String(), "distinctive-log-line") {
		t.Fatalf("run list should be a summary: %d %s", listRes.Code, listRes.Body.String())
	}
	one := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/logs/actions/runs/"+strconv.Itoa(run.ID), nil)
	one.SetBasicAuth("admin", token)
	oneRes := httptest.NewRecorder()
	a.ServeHTTP(oneRes, one)
	if oneRes.Code != http.StatusOK || !strings.Contains(oneRes.Body.String(), "distinctive-log-line") {
		t.Fatalf("single-run API lost the log: %d %s", oneRes.Code, oneRes.Body.String())
	}
	page := httptest.NewRequest(http.MethodGet, "/repos/team/logs/actions", nil)
	page.SetBasicAuth("admin", token)
	pageRes := httptest.NewRecorder()
	a.ServeHTTP(pageRes, page)
	artifactLink := "/api/v1/repos/team/logs/actions/runs/" + strconv.Itoa(run.ID) + "/artifacts/out.txt"
	if pageRes.Code != http.StatusOK || !strings.Contains(pageRes.Body.String(), "distinctive-log-line") || !strings.Contains(pageRes.Body.String(), artifactLink) || !strings.HasSuffix(strings.TrimSpace(pageRes.Body.String()), "</html>") {
		t.Fatalf("actions page is missing the log or artifact link:\n%s", pageRes.Body.String())
	}
}

func TestActionHistoryMigratesInlineLogsAndPrunes(t *testing.T) {
	root := t.TempDir()
	s := &store{root: root, repos: filepath.Join(root, "repos")}
	now := time.Now().UTC()
	db := actionDB{NextRunID: 1, LastScheduledAt: map[string]time.Time{}}
	total := maxActionRunsPerRepo + 5
	for i := 1; i <= total; i++ {
		run := actionRun{ID: i, Repo: "team/busy", Ref: "main", Commit: "c", Status: "success", CreatedAt: now, Jobs: []actionJob{{ID: 1, Name: "test", Status: "success", Log: "legacy inline log " + strconv.Itoa(i)}}}
		if i == 2 {
			run.Status = "running"
		}
		db.Runs = append(db.Runs, run)
		if err := os.MkdirAll(filepath.Join(root, "artifacts", strconv.Itoa(i), "1"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	db.Runs = append(db.Runs, actionRun{ID: total + 1, Repo: "team/quiet", Status: "queued", CreatedAt: now})
	db.NextRunID = total + 2
	if err := s.saveActions(db); err != nil {
		t.Fatal(err)
	}
	s.finishActionRun(total+1, "success", []actionJob{{ID: 1, Name: "test", Status: "success", Log: "fresh log"}})

	loaded, err := s.loadActions()
	if err != nil {
		t.Fatal(err)
	}
	busy := 0
	ids := map[int]bool{}
	for _, run := range loaded.Runs {
		ids[run.ID] = true
		if run.Repo == "team/busy" {
			busy++
		}
		for _, job := range run.Jobs {
			if job.Log != "" {
				t.Fatalf("run %d still stores its log inline", run.ID)
			}
		}
	}
	// 200 finished runs are kept plus the still-running run #2.
	if busy != maxActionRunsPerRepo+1 || !ids[2] || ids[1] || ids[3] || !ids[6] || !ids[total] || !ids[total+1] {
		t.Fatalf("unexpected pruning result: busy=%d ids=%v", busy, ids)
	}
	// 204 finished runs exceed the cap by four: the oldest finished ones go.
	for _, gone := range []int{1, 3, 4, 5} {
		if _, err := os.Stat(filepath.Join(root, "artifacts", strconv.Itoa(gone))); !os.IsNotExist(err) {
			t.Fatalf("artifacts of pruned run %d remain: %v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "artifacts", "6")); err != nil {
		t.Fatalf("artifacts of a kept run were removed: %v", err)
	}
	run, err := actionRunByID(s, total)
	if err != nil || run.Jobs[0].Log != "legacy inline log "+strconv.Itoa(total) {
		t.Fatalf("migrated log not readable: %+v %v", run, err)
	}
	fresh, err := actionRunByID(s, total+1)
	if err != nil || fresh.Jobs[0].Log != "fresh log" {
		t.Fatalf("fresh log not readable: %+v %v", fresh, err)
	}
	var check map[string]any
	raw, _ := os.ReadFile(filepath.Join(root, "actions.json"))
	if err := json.Unmarshal(raw, &check); err != nil || strings.Contains(string(raw), "inline log") {
		t.Fatalf("actions.json still carries logs or is invalid: %v", err)
	}
}
