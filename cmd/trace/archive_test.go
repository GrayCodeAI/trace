package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArchiveAPIAndRestore(t *testing.T) {
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
	token := adminToken(t, root)
	call := func(action string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/repos/team/demo/"+action, strings.NewReader("{}"))
		r.SetBasicAuth("admin", token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	if w := call("archive"); w.Code != http.StatusOK {
		t.Fatalf("archive status %d: %s", w.Code, w.Body.String())
	}
	path, _ := a.store.repoPath("team/demo")
	if !isArchived(path) {
		t.Fatal("repository was not archived")
	}
	if w := call("restore"); w.Code != http.StatusOK {
		t.Fatalf("restore status %d: %s", w.Code, w.Body.String())
	}
	var repo apiRepo
	if err := json.Unmarshal(callGetRepo(t, a, token).Body.Bytes(), &repo); err != nil {
		t.Fatal(err)
	}
	if repo.Archived {
		t.Fatal("repository remained archived")
	}
}

func callGetRepo(t *testing.T, a *app, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/demo", nil)
	r.SetBasicAuth("admin", token)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("repo status %d: %s", w.Code, w.Body.String())
	}
	return w
}
