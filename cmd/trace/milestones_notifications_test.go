package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMilestonesAndNotifications(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/demo", false); err != nil {
		t.Fatal(err)
	}
	adminToken := readToken(t, root)
	item, err := a.store.createMilestone("team/demo", "v1", "Ship v1", "2030-01-02")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := a.store.createIssue("team/demo", "Track release", "", "admin", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := a.store.setIssueMilestone("team/demo", issue.ID, "admin", true, "1")
	if err != nil || updated.Milestone != "1" {
		t.Fatalf("set milestone: %+v %v", updated, err)
	}
	if err := a.store.addNotification("admin", "issue.updated", "team/demo", "1", "Issue updated"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/demo/milestones", nil)
	req.SetBasicAuth("admin", adminToken)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("milestones API: %d %s", res.Code, res.Body.String())
	}
	var milestones []milestone
	if err := json.Unmarshal(res.Body.Bytes(), &milestones); err != nil || len(milestones) != 1 || milestones[0].ID != item.ID {
		t.Fatalf("unexpected milestones: %s", res.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil)
	req.SetBasicAuth("admin", adminToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Issue updated") {
		t.Fatalf("notifications API: %d %s", res.Code, res.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/notifications/1/read", nil)
	req.SetBasicAuth("admin", adminToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("mark notification: %d %s", res.Code, res.Body.String())
	}
}
