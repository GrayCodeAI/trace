package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFederationConflictResolutionAdminAPIAndWeb(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/demo", true); err != nil {
		t.Fatal(err)
	}
	path, err := a.store.repoPath("team/demo")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Test")
	gitTest(t, work, "config", "user.email", "test@example.invalid")
	gitTest(t, work, "commit", "--allow-empty", "-m", "first")
	gitAdminPush(t, work, path, "main")
	refs, err := a.store.repositoryRefs("team/demo")
	if err != nil {
		t.Fatal(err)
	}
	localSHA := refs["refs/heads/main"]
	gitTest(t, work, "commit", "--allow-empty", "-m", "second")
	remoteSHA := strings.TrimSpace(gitTest(t, work, "rev-parse", "HEAD"))
	source := "https://source.example/git/team/demo.git"
	branch := federationConflict{Repo: "team/demo", Source: source, Ref: "refs/heads/main", LocalSHA: localSHA, RemoteSHA: remoteSHA, DetectedAt: time.Now().UTC()}
	if err := a.store.recordFederationConflicts([]federationConflict{branch}); err != nil {
		t.Fatal(err)
	}
	admin := adminToken(t, root)
	memberToken, err := a.store.addUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	request := func(user, token string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/federation/conflicts/resolve", bytes.NewReader(body))
		r.SetBasicAuth(user, token)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	payload, _ := json.Marshal(map[string]string{"repo": branch.Repo, "source": branch.Source, "ref": branch.Ref, "local_sha": branch.LocalSHA, "remote_sha": branch.RemoteSHA})
	if response := request("alice", memberToken, payload); response.Code != http.StatusForbidden {
		t.Fatalf("member approval status %d", response.Code)
	}
	wrong, _ := json.Marshal(map[string]string{"repo": branch.Repo, "source": branch.Source, "ref": branch.Ref, "local_sha": branch.LocalSHA, "remote_sha": localSHA})
	if response := request("admin", admin, wrong); response.Code != http.StatusConflict {
		t.Fatalf("mismatched remote approval status %d", response.Code)
	}
	if response := request("admin", admin, payload); response.Code != http.StatusOK {
		t.Fatalf("admin approval status %d: %s", response.Code, response.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/settings/federation", nil)
	get.SetBasicAuth("admin", admin)
	page := httptest.NewRecorder()
	a.ServeHTTP(page, get)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Approved by admin") {
		t.Fatalf("approved conflict missing from page: %d %s", page.Code, page.Body.String())
	}
	deletion := federationConflict{Repo: "team/demo", Source: source, Ref: "refs/heads/main", LocalSHA: localSHA, DetectedAt: time.Now().UTC()}
	if err := a.store.recordFederationConflicts([]federationConflict{deletion}); err != nil {
		t.Fatal(err)
	}
	form := "csrf=" + a.csrfFor("admin") + "&action=resolve&repo=team%2Fdemo&source=https%3A%2F%2Fsource.example%2Fgit%2Fteam%2Fdemo.git&ref=refs%2Fheads%2Fmain&local_sha=" + localSHA
	post := httptest.NewRequest(http.MethodPost, "/settings/federation", strings.NewReader(form))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.SetBasicAuth("admin", admin)
	result := httptest.NewRecorder()
	a.ServeHTTP(result, post)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "Retry peer sync") {
		t.Fatalf("web approval failed: %d %s", result.Code, result.Body.String())
	}
	conflicts, err := a.store.loadFederationConflicts()
	if err != nil || len(conflicts) != 2 || conflicts[0].SupersededAt.IsZero() || conflicts[1].ApprovedAt.IsZero() {
		t.Fatalf("web approval did not persist: %+v %v", conflicts, err)
	}
}
