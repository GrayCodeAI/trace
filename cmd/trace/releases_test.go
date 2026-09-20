package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseLifecycleAndArchive(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
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
	repoPath, err := a.store.repoPath("team/demo")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("release\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "release base")
	gitAdminPush(t, work, repoPath, "main")
	gitTest(t, work, "tag", "-m", "First release", "v1.0.0")
	cmd := exec.Command("git", "push", repoPath, "refs/tags/v1.0.0")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "TRACE_ADMIN=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("admin tag push: %v\n%s", err, out)
	}

	item, err := a.store.createRelease("team/demo", "v1.0.0", "First release", "Notes", "admin", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != 1 || item.Commit == "" {
		t.Fatalf("unexpected release: %+v", item)
	}
	token := readToken(t, root)
	webReq := httptest.NewRequest(http.MethodGet, "/repos/team/demo/releases", nil)
	webReq.SetBasicAuth("admin", token)
	webRes := httptest.NewRecorder()
	a.ServeHTTP(webRes, webReq)
	if webRes.Code != http.StatusOK || !strings.Contains(webRes.Body.String(), "Create release") {
		t.Fatalf("release web page: %d %s", webRes.Code, webRes.Body.String())
	}
	if _, err := a.store.createRelease("team/demo", "v1.0.0", "Duplicate", "", "admin", false, false); err == nil {
		t.Fatal("duplicate tag release was accepted")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/demo/releases", nil)
	req.SetBasicAuth("admin", token)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("release list: %d %s", res.Code, res.Body.String())
	}
	var listed []release
	if err := json.Unmarshal(res.Body.Bytes(), &listed); err != nil || len(listed) != 1 || listed[0].Tag != "v1.0.0" {
		t.Fatalf("unexpected release list: %s", res.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/repos/team/demo/releases/1/assets", strings.NewReader("binary release asset"))
	req.Header.Set("X-Asset-Name", "demo-macos.tar.gz")
	req.Header.Set("Content-Type", "application/gzip")
	req.SetBasicAuth("admin", token)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("asset upload: %d %s", res.Code, res.Body.String())
	}
	var asset releaseAsset
	if err := json.Unmarshal(res.Body.Bytes(), &asset); err != nil || asset.Name != "demo-macos.tar.gz" || asset.Size != 20 || asset.SHA256 == "" {
		t.Fatalf("unexpected asset response: %s", res.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/demo/releases/1/assets", nil)
	req.SetBasicAuth("admin", token)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "demo-macos.tar.gz") {
		t.Fatalf("asset list: %d %s", res.Code, res.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/demo/releases/1/assets/demo-macos.tar.gz", nil)
	req.SetBasicAuth("admin", token)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK || res.Body.String() != "binary release asset" {
		t.Fatalf("asset download: %d %q", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/demo/releases/archive/v1.0.0", nil)
	req.SetBasicAuth("admin", token)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("archive: %d %s", res.Code, res.Body.String())
	}
	gz, err := gzip.NewReader(strings.NewReader(res.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(header.Name, "README.md") {
			content, _ := io.ReadAll(tr)
			found = string(content) == "release\n"
		}
	}
	if err := gz.Close(); err != nil || !found {
		t.Fatalf("archive did not contain README: %v", err)
	}
}
