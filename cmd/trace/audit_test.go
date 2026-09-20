package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuditAPI(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	token := readToken(t, root)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/repos", strings.NewReader(`{"name":"team/audit"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", token)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("create repo: %d %s", res.Code, res.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/audit?limit=10", nil)
	req.SetBasicAuth("admin", token)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("audit: %d %s", res.Code, res.Body.String())
	}
	var events []auditEvent
	if err := json.Unmarshal(res.Body.Bytes(), &events); err != nil || len(events) != 1 {
		t.Fatalf("unexpected audit response: %s", res.Body.String())
	}
	if events[0].Action != "repo.create" || events[0].Repo != "team/audit" || events[0].Actor != "admin" {
		t.Fatalf("unexpected event: %+v", events[0])
	}
}
