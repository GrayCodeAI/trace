package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRepositoryStarsAPIAndWeb(t *testing.T) {
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
	request := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.SetBasicAuth("admin", token)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	res := request(http.MethodGet, "/api/v1/repos/team/demo/stars")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"count":0`) {
		t.Fatalf("initial stars: %d %s", res.Code, res.Body.String())
	}
	res = request(http.MethodPut, "/api/v1/repos/team/demo/stars")
	if res.Code != http.StatusCreated {
		t.Fatalf("star: %d %s", res.Code, res.Body.String())
	}
	var starred map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &starred); err != nil || starred["starred"] != true {
		t.Fatalf("star response: %s", res.Body.String())
	}
	res = request(http.MethodGet, "/repos/team/demo")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Starred (1)") {
		t.Fatalf("web star state: %d", res.Code)
	}
	res = request(http.MethodDelete, "/api/v1/repos/team/demo/stars")
	if res.Code != http.StatusNoContent {
		t.Fatalf("unstar: %d %s", res.Code, res.Body.String())
	}
	res = request(http.MethodPut, "/api/v1/repos/team/demo/watchers")
	if res.Code != http.StatusCreated {
		t.Fatalf("watch: %d %s", res.Code, res.Body.String())
	}
	res = request(http.MethodGet, "/api/v1/repos/team/demo/watchers")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"watching":true`) {
		t.Fatalf("watch list: %d %s", res.Code, res.Body.String())
	}
	notifyWatchers(a.store, "team/demo", "someone-else", "repository.push", "main", "A push arrived")
	notifications, err := a.store.listNotifications("admin", true)
	if err != nil || len(notifications) != 1 || notifications[0].Kind != "repository.push" {
		t.Fatalf("watcher notification: %#v err=%v", notifications, err)
	}
	res = request(http.MethodDelete, "/api/v1/repos/team/demo/watchers")
	if res.Code != http.StatusNoContent {
		t.Fatalf("unwatch: %d %s", res.Code, res.Body.String())
	}
}
