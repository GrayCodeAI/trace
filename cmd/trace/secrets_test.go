package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRepositorySecretsAPIListsNamesWithoutValues(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/secure", false); err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	put := httptest.NewRequest(http.MethodPut, "/api/v1/repos/team/secure/secrets/API_KEY", strings.NewReader(`{"value":"super-secret"}`))
	put.SetBasicAuth("admin", token)
	put.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, put)
	if w.Code != http.StatusCreated {
		t.Fatalf("secret set status %d: %s", w.Code, w.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/secure/secrets", nil)
	get.SetBasicAuth("admin", token)
	w = httptest.NewRecorder()
	a.ServeHTTP(w, get)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "API_KEY") || strings.Contains(w.Body.String(), "super-secret") {
		t.Fatalf("secret list leaked or omitted name: %d %s", w.Code, w.Body.String())
	}
}
