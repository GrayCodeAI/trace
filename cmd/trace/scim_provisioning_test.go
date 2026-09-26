package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newSCIMTestApp(t *testing.T) (*app, func(method, path, body string) *httptest.ResponseRecorder) {
	t.Helper()
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	return a, func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.SetBasicAuth("admin", token)
		r.Header.Set("Content-Type", "application/scim+json")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
}

func TestSCIMRejectsPasswords(t *testing.T) {
	a, request := newSCIMTestApp(t)
	res := request(http.MethodPost, "/scim/v2/Users", `{"userName":"weak","active":true,"password":"hunter2"}`)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "urn:ietf:params:scim:api:messages:2.0:Error") {
		t.Fatalf("SCIM password was not refused: %d %s", res.Code, res.Body.String())
	}
	db, _ := a.store.loadUsers()
	if _, exists := db.Users["weak"]; exists {
		t.Fatal("user was created despite the refused password")
	}
	if _, ok := db.authenticate("weak", "hunter2"); ok {
		t.Fatal("SCIM password became a Basic-auth credential")
	}
}

type scimListBody struct {
	TotalResults int `json:"totalResults"`
	StartIndex   int `json:"startIndex"`
	ItemsPerPage int `json:"itemsPerPage"`
	Resources    []struct {
		ID       string `json:"id"`
		UserName string `json:"userName"`
	} `json:"Resources"`
}

func TestSCIMUserFilterAndPagination(t *testing.T) {
	_, request := newSCIMTestApp(t)
	for _, name := range []string{"carol", "alice", "dave", "bob"} {
		if res := request(http.MethodPost, "/scim/v2/Users", `{"userName":"`+name+`"}`); res.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, res.Code, res.Body.String())
		}
	}
	list := func(query string) scimListBody {
		res := request(http.MethodGet, "/scim/v2/Users?"+query, "")
		if res.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", query, res.Code, res.Body.String())
		}
		var body scimListBody
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	found := list("filter=" + url.QueryEscape(`userName eq "bob"`))
	if found.TotalResults != 1 || len(found.Resources) != 1 || found.Resources[0].UserName != "bob" {
		t.Fatalf("filter by userName: %+v", found)
	}
	if missing := list("filter=" + url.QueryEscape(`USERNAME EQ "nobody"`)); missing.TotalResults != 0 || len(missing.Resources) != 0 {
		t.Fatalf("filter for a missing user: %+v", missing)
	}
	page := list("startIndex=2&count=2")
	if page.TotalResults != 5 || page.StartIndex != 2 || page.ItemsPerPage != 2 || len(page.Resources) != 2 || page.Resources[0].ID != "alice" || page.Resources[1].ID != "bob" {
		t.Fatalf("pagination (admin, alice, bob, carol, dave): %+v", page)
	}
	if last := list("startIndex=5&count=10"); last.ItemsPerPage != 1 || last.Resources[0].ID != "dave" {
		t.Fatalf("last page: %+v", last)
	}
	if res := request(http.MethodGet, "/scim/v2/Users?filter="+url.QueryEscape(`emails co "x"`), ""); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "invalidFilter") {
		t.Fatalf("unsupported filter: %d %s", res.Code, res.Body.String())
	}
}

func TestSCIMPatchPathsAndGroupRemoval(t *testing.T) {
	a, request := newSCIMTestApp(t)
	for _, name := range []string{"alice", "bob", "carol"} {
		if _, err := a.store.addUser(name, false); err != nil {
			t.Fatal(err)
		}
	}
	if res := request(http.MethodPatch, "/scim/v2/Users/alice", `{"Operations":[{"op":"Replace","path":"active","value":"False"}]}`); res.Code != http.StatusOK {
		t.Fatalf("path-style active patch: %d %s", res.Code, res.Body.String())
	}
	db, _ := a.store.loadUsers()
	if !db.Users["alice"].Disabled {
		t.Fatal("path-style active=False did not disable the user")
	}
	if res := request(http.MethodPost, "/scim/v2/Groups", `{"displayName":"eng","members":[{"value":"alice"},{"value":"bob"},{"value":"carol"}]}`); res.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", res.Code, res.Body.String())
	}
	members := func() map[string]bool {
		teams, err := a.store.loadTeams()
		if err != nil {
			t.Fatal(err)
		}
		return teams.Teams["eng"].Members
	}
	if res := request(http.MethodPatch, "/scim/v2/Groups/eng", `{"Operations":[{"op":"remove","path":"members[value eq \"alice\"]"}]}`); res.Code != http.StatusOK {
		t.Fatalf("remove by filter path: %d %s", res.Code, res.Body.String())
	}
	if m := members(); m["alice"] || !m["bob"] || !m["carol"] {
		t.Fatalf("remove by filter path: %v", m)
	}
	if res := request(http.MethodPatch, "/scim/v2/Groups/eng", `{"Operations":[{"op":"Remove","path":"members","value":[{"value":"bob"}]}]}`); res.Code != http.StatusOK {
		t.Fatalf("remove by value: %d %s", res.Code, res.Body.String())
	}
	if m := members(); m["bob"] || !m["carol"] {
		t.Fatalf("remove by value: %v", m)
	}
	if res := request(http.MethodPatch, "/scim/v2/Groups/eng", `{"Operations":[{"op":"add","path":"members","value":[{"value":"alice"}]},{"op":"replace","path":"displayName","value":"eng"}]}`); res.Code != http.StatusOK {
		t.Fatalf("add with path: %d %s", res.Code, res.Body.String())
	}
	if m := members(); !m["alice"] || !m["carol"] {
		t.Fatalf("add with path: %v", m)
	}
	if res := request(http.MethodPatch, "/scim/v2/Groups/eng", `{"Operations":[{"op":"remove","path":"members"}]}`); res.Code != http.StatusOK {
		t.Fatalf("remove all members: %d %s", res.Code, res.Body.String())
	}
	if m := members(); len(m) != 0 {
		t.Fatalf("remove all members: %v", m)
	}
	groups := request(http.MethodGet, "/scim/v2/Groups?filter="+url.QueryEscape(`displayName eq "eng"`), "")
	if groups.Code != http.StatusOK || !strings.Contains(groups.Body.String(), `"totalResults":1`) {
		t.Fatalf("group filter: %d %s", groups.Code, groups.Body.String())
	}
}
