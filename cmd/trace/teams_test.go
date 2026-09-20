package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTeamInheritedRepositoryAccess(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/shared", false); err != nil {
		t.Fatal(err)
	}
	memberToken, err := a.store.addUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createTeam("builders"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.updateTeamMember("builders", "alice", true); err != nil {
		t.Fatal(err)
	}
	if err := a.store.grantTeam("builders", "team/shared", "write"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	req := httptest.NewRequest(http.MethodGet, "/repos/team/shared", nil)
	req.SetBasicAuth("alice", memberToken)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("team member repository page: %d %s", w.Code, w.Body.String())
	}
	api := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
	api.SetBasicAuth("alice", memberToken)
	apiW := httptest.NewRecorder()
	a.ServeHTTP(apiW, api)
	if apiW.Code != http.StatusOK || !strings.Contains(apiW.Body.String(), "team/shared") {
		t.Fatalf("team member repository list: %d %s", apiW.Code, apiW.Body.String())
	}
	if got := a.store.expandUser("alice", userRecord{}).Repos["team/shared"]; got != "write" {
		t.Fatalf("inherited role %q", got)
	}
	if err := a.store.grantTeam("builders", "team/shared", "maintain"); err != nil {
		t.Fatal(err)
	}
	if got := a.store.expandUser("alice", userRecord{}).Repos["team/shared"]; got != "maintain" || !a.store.expandUser("alice", userRecord{}).canMaintain("team/shared") {
		t.Fatalf("maintain role was not inherited: %q", got)
	}
	page := httptest.NewRequest(http.MethodGet, "/settings/teams", nil)
	page.SetBasicAuth("admin", adminToken(t, root))
	pageW := httptest.NewRecorder()
	a.ServeHTTP(pageW, page)
	if pageW.Code != http.StatusOK || !strings.Contains(pageW.Body.String(), "builders") {
		t.Fatalf("team settings page: %d %s", pageW.Code, pageW.Body.String())
	}
}
