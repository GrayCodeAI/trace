package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSCIMUserProvisioning(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.SetBasicAuth("admin", token)
		r.Header.Set("Content-Type", "application/scim+json")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	created := request(http.MethodPost, "/scim/v2/Users", `{"userName":"scim-user","active":true}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("SCIM create status %d: %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "token") {
		t.Fatal("SCIM response exposed a personal token")
	}
	listed := request(http.MethodGet, "/scim/v2/Users", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "scim-user") {
		t.Fatalf("SCIM list status %d: %s", listed.Code, listed.Body.String())
	}
	patched := request(http.MethodPatch, "/scim/v2/Users/scim-user", `{"Operations":[{"op":"replace","value":{"active":false}}]}`)
	if patched.Code != http.StatusOK {
		t.Fatalf("SCIM patch status %d: %s", patched.Code, patched.Body.String())
	}
	db, err := a.store.loadUsers()
	if err != nil || !db.Users["scim-user"].Disabled {
		t.Fatalf("SCIM user not disabled: %#v err=%v", db.Users["scim-user"], err)
	}
}

func TestSCIMGroupsMapToTeams(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.addUser("alice", false); err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.SetBasicAuth("admin", token)
		r.Header.Set("Content-Type", "application/scim+json")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	created := request(http.MethodPost, "/scim/v2/Groups", `{"displayName":"engineering","members":[{"value":"alice"}]}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), "alice") {
		t.Fatalf("SCIM group create: %d %s", created.Code, created.Body.String())
	}
	teamDB, err := a.store.loadTeams()
	if err != nil || !teamDB.Teams["engineering"].Members["alice"] {
		t.Fatalf("SCIM group did not map to team: %#v err=%v", teamDB, err)
	}
	patched := request(http.MethodPatch, "/scim/v2/Groups/engineering", `{"Operations":[{"op":"replace","value":[{"value":"alice"}]}]}`)
	if patched.Code != http.StatusOK || !strings.Contains(patched.Body.String(), "alice") {
		t.Fatalf("SCIM group patch: %d %s", patched.Code, patched.Body.String())
	}
	listed := request(http.MethodGet, "/scim/v2/Groups", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "engineering") {
		t.Fatalf("SCIM group list: %d %s", listed.Code, listed.Body.String())
	}
}
