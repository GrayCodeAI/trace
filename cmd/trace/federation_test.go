package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentNodeSignerCreationKeepsOneIdentity(t *testing.T) {
	root := t.TempDir()
	const workers = 16
	keys := make([]ed25519.PublicKey, workers)
	errors := make([]error, workers)
	var group sync.WaitGroup
	for i := range keys {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			private, err := loadOrCreateNodeSigner(root)
			if err == nil {
				keys[i] = private.Public().(ed25519.PublicKey)
			}
			errors[i] = err
		}(i)
	}
	group.Wait()
	for i := range keys {
		if errors[i] != nil || !bytes.Equal(keys[i], keys[0]) {
			t.Fatalf("node identity changed under concurrent creation: worker=%d err=%v", i, errors[i])
		}
	}
}

func TestRepositoryManifestIsSignedAndVerifiable(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s := &store{root: root, repos: filepath.Join(root, "repos")}
	if err := s.createRepo("alice/demo", false); err != nil {
		t.Fatal(err)
	}
	repoPath, err := s.repoPath("alice/demo")
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git setup: %v: %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("demo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	gitAdminPush(t, work, repoPath, "main")
	manifest, err := s.repositoryManifest("alice/demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyRepositoryManifest(manifest); err != nil {
		t.Fatalf("verify signed manifest: %v", err)
	}
	manifest.Payload.Refs["refs/heads/main"] = "tampered"
	if err := verifyRepositoryManifest(manifest); err == nil {
		t.Fatal("tampered manifest unexpectedly verified")
	}
}

func TestFederationManifestAPI(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s := &store{root: root, repos: filepath.Join(root, "repos")}
	if err := s.createRepo("alice/demo", false); err != nil {
		t.Fatal(err)
	}
	db, err := s.loadUsers()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{store: s}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/federation/repos/alice/demo/manifest", nil)
	token, err := os.ReadFile(filepath.Join(root, tokenFilename))
	if err != nil {
		t.Fatal(err)
	}
	r.SetBasicAuth("admin", strings.TrimSpace(string(token)))
	w := httptest.NewRecorder()
	a.api(w, r, db)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var manifest signedRepositoryManifest
	if err := json.Unmarshal(w.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if err := verifyRepositoryManifest(manifest); err != nil {
		t.Fatal(err)
	}
}

func TestFederationPeersAPI(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s := &store{root: root, repos: filepath.Join(root, "repos")}
	db, err := s.loadUsers()
	if err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(filepath.Join(root, tokenFilename))
	if err != nil {
		t.Fatal(err)
	}
	sourceToken := filepath.Join(root, "source-token")
	if err := os.WriteFile(sourceToken, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &app{store: s}
	request := func(method, path string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.SetBasicAuth("admin", strings.TrimSpace(string(token)))
		w := httptest.NewRecorder()
		a.api(w, r, db)
		return w
	}
	peerBody, _ := json.Marshal(federationPeer{Repo: "alice/demo", Source: "http://127.0.0.1:8787/git/alice/demo.git", Username: "mirror", TokenFile: sourceToken})
	if w := request(http.MethodPost, "/api/v1/federation/peers", peerBody); w.Code != http.StatusCreated {
		t.Fatalf("create peer status %d: %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, "/api/v1/federation/peers", nil); w.Code != http.StatusOK {
		t.Fatalf("list peer status %d: %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodDelete, "/api/v1/federation/peers/alice/demo", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete peer status %d: %s", w.Code, w.Body.String())
	}
}

func TestFederationSettingsWebPage(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	r := httptest.NewRequest(http.MethodGet, "/settings/federation", nil)
	r.SetBasicAuth("admin", token)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Configured peers") {
		t.Fatalf("federation page: status=%d body=%s", w.Code, w.Body.String())
	}
}
