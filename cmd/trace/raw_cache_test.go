package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrivateRawAndPagesAreNotSharedCacheable checks Cache-Control on raw and
// Pages responses: only public repositories may be stored by shared caches.
func TestPrivateRawAndPagesAreNotSharedCacheable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"team/private", "team/public"} {
		if err := a.store.createRepo(name, false); err != nil {
			t.Fatal(err)
		}
		commitFiles(t, a.store, name, map[string][]byte{"index.html": []byte("<p>site</p>"), "notes.txt": []byte("notes")})
		if err := a.store.setPages(name, "main", "", true); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.setPublic("team/public", true); err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetBasicAuth("admin", token)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, res.Code, res.Body.String())
		}
		return res
	}
	for _, path := range []string{"/raw/team/private/notes.txt", "/api/v1/repos/team/private/raw?path=notes.txt", "/pages/team/private/"} {
		cc := get(path).Header().Get("Cache-Control")
		if strings.Contains(cc, "public") || !strings.Contains(cc, "private") || !strings.Contains(cc, "no-store") {
			t.Fatalf("%s: private content has Cache-Control %q", path, cc)
		}
	}
	for _, path := range []string{"/raw/team/public/notes.txt", "/pages/team/public/"} {
		if cc := get(path).Header().Get("Cache-Control"); !strings.Contains(cc, "public") {
			t.Fatalf("%s: public content has Cache-Control %q", path, cc)
		}
	}
}
