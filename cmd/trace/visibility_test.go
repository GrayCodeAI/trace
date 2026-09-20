package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicRepositoryAllowsAnonymousGitReadOnly(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/public", false); err != nil {
		t.Fatal(err)
	}
	if err := a.store.setPublic("team/public", true); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/git/team/public.git/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("anonymous public clone advertisement: %d %s", w.Code, w.Body.String())
	}
	push := httptest.NewRequest(http.MethodGet, "/git/team/public.git/info/refs?service=git-receive-pack", nil)
	pushW := httptest.NewRecorder()
	a.ServeHTTP(pushW, push)
	if pushW.Code == http.StatusOK {
		t.Fatal("anonymous public push advertisement unexpectedly allowed")
	}
	page := httptest.NewRequest(http.MethodGet, "/repos/team/public", nil)
	pageW := httptest.NewRecorder()
	a.ServeHTTP(pageW, page)
	if pageW.Code != http.StatusOK {
		t.Fatalf("anonymous public web page: %d %s", pageW.Code, pageW.Body.String())
	}
}
