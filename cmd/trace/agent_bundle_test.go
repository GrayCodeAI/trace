package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignedAgentBundleImportAndIncrementalCheckpoints(t *testing.T) {
	sourceRoot := filepath.Join(t.TempDir(), "source")
	targetRoot := filepath.Join(t.TempDir(), "target")
	for _, root := range []string{sourceRoot, targetRoot} {
		if err := initData(root); err != nil {
			t.Fatal(err)
		}
	}
	source, _ := openStore(sourceRoot)
	target, _ := openStore(targetRoot)
	for _, store := range []*store{source, target} {
		if err := store.createRepo("team/demo", false); err != nil {
			t.Fatal(err)
		}
	}
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Test")
	gitTest(t, work, "config", "user.email", "test@example.invalid")
	gitTest(t, work, "commit", "--allow-empty", "-m", "first")
	for _, store := range []*store{source, target} {
		path, _ := store.repoPath("team/demo")
		gitAdminPush(t, work, path, "main")
	}
	session, err := source.createAgentSession("team/demo", "codex", "main", "token=hidden Implement login", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.addAgentCheckpoint("team/demo", session.ID, "main", "password=hush Tests pass", "review"); err != nil {
		t.Fatal(err)
	}
	bundle, err := source.exportAgentBundle("team/demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Payload.Sessions) != 1 || strings.Contains(bundle.Payload.Sessions[0].Summary, "hidden") || strings.Contains(bundle.Payload.Sessions[0].Checkpoints[0].Summary, "hush") {
		t.Fatalf("exported sensitive summary: %+v", bundle.Payload.Sessions)
	}
	wrongID := strings.Repeat("A", len(bundle.Payload.NodeID))
	if _, err := target.importAgentBundle("team/demo", wrongID, bundle); err == nil {
		t.Fatal("wrong source node identity accepted")
	}
	tampered := bundle
	tampered.Payload.Sessions = append([]agentSession(nil), bundle.Payload.Sessions...)
	tampered.Payload.Sessions[0].Summary = "changed"
	if _, err := target.importAgentBundle("team/demo", bundle.Payload.NodeID, tampered); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered bundle accepted: %v", err)
	}
	result, err := target.importAgentBundle("team/demo", bundle.Payload.NodeID, bundle)
	if err != nil || result.SessionsCreated != 1 || result.CheckpointsAdded != 1 {
		t.Fatalf("first import: %+v %v", result, err)
	}
	result, err = target.importAgentBundle("team/demo", bundle.Payload.NodeID, bundle)
	if err != nil || result.SessionsCreated != 0 || result.CheckpointsAdded != 0 {
		t.Fatalf("repeat import: %+v %v", result, err)
	}
	imported, err := target.listAgentSessions("team/demo")
	if err != nil || len(imported) != 1 || imported[0].OriginNodeID != bundle.Payload.NodeID || imported[0].OriginSessionID != session.ID {
		t.Fatalf("import provenance: %+v %v", imported, err)
	}
	if _, err := target.addAgentCheckpoint("team/demo", imported[0].ID, "main", "local rewrite", ""); err == nil {
		t.Fatal("imported signed session was changed locally")
	}
	gitTest(t, work, "commit", "--allow-empty", "-m", "second")
	sourcePath, _ := source.repoPath("team/demo")
	gitAdminPush(t, work, sourcePath, "main")
	if _, err := source.addAgentCheckpoint("team/demo", session.ID, "main", "Second commit", "done"); err != nil {
		t.Fatal(err)
	}
	newBundle, err := source.exportAgentBundle("team/demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.importAgentBundle("team/demo", newBundle.Payload.NodeID, newBundle); err == nil || !strings.Contains(err.Error(), "commit missing") {
		t.Fatalf("missing target commit accepted: %v", err)
	}
	imported, _ = target.listAgentSessions("team/demo")
	if len(imported[0].Checkpoints) != 1 {
		t.Fatal("failed import partially changed target history")
	}
	targetPath, _ := target.repoPath("team/demo")
	gitAdminPush(t, work, targetPath, "main")
	result, err = target.importAgentBundle("team/demo", newBundle.Payload.NodeID, newBundle)
	if err != nil || result.SessionsCreated != 0 || result.CheckpointsAdded != 1 {
		t.Fatalf("incremental import: %+v %v", result, err)
	}
	imported, _ = target.listAgentSessions("team/demo")
	if len(imported[0].Checkpoints) != 2 {
		t.Fatalf("incremental checkpoint missing: %+v", imported[0])
	}
	rewritten := newBundle
	rewritten.Payload.Sessions = append([]agentSession(nil), newBundle.Payload.Sessions...)
	rewritten.Payload.Sessions[0].Checkpoints = append([]agentCheckpoint(nil), newBundle.Payload.Sessions[0].Checkpoints...)
	rewritten.Payload.Sessions[0].Checkpoints[0].Summary = "rewritten history"
	signer, err := loadOrCreateNodeSigner(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(rewritten.Payload)
	rewritten.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(signer, encoded))
	if _, err := target.importAgentBundle("team/demo", rewritten.Payload.NodeID, rewritten); err == nil || !strings.Contains(err.Error(), "changed its signed origin fields") {
		t.Fatalf("source rewrite of imported history accepted: %v", err)
	}
}

func TestAgentBundleAPIAndWebPermissions(t *testing.T) {
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
	path, _ := a.store.repoPath("team/demo")
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Test")
	gitTest(t, work, "config", "user.email", "test@example.invalid")
	gitTest(t, work, "commit", "--allow-empty", "-m", "first")
	gitAdminPush(t, work, path, "main")
	memberToken, err := a.store.addUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.grantUser("alice", "team/demo", "read"); err != nil {
		t.Fatal(err)
	}
	admin := adminToken(t, root)
	request := func(method, endpoint, user, token string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, endpoint, bytes.NewReader(body))
		r.SetBasicAuth(user, token)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	export := request(http.MethodGet, "/api/v1/repos/team/demo/agent-sessions/bundle", "alice", memberToken, nil)
	if export.Code != http.StatusOK || !strings.Contains(export.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("bundle export: %d %s", export.Code, export.Body.String())
	}
	var bundle signedAgentBundle
	if err := json.Unmarshal(export.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"expected_node_id": bundle.Payload.NodeID, "bundle": bundle})
	if response := request(http.MethodPost, "/api/v1/repos/team/demo/agent-sessions/bundle", "alice", memberToken, body); response.Code != http.StatusForbidden {
		t.Fatalf("member import status %d", response.Code)
	}
	if response := request(http.MethodPost, "/api/v1/repos/team/demo/agent-sessions/bundle", "admin", admin, body); response.Code != http.StatusOK {
		t.Fatalf("admin import status %d: %s", response.Code, response.Body.String())
	}
	page := request(http.MethodGet, "/repos/team/demo/agents", "admin", admin, nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Portable signed history") || !strings.Contains(page.Body.String(), "agents/import") {
		t.Fatalf("bundle form missing: %d", page.Code)
	}
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	_ = writer.WriteField("csrf", a.csrfFor("admin"))
	_ = writer.WriteField("node_id", bundle.Payload.NodeID)
	part, err := writer.CreateFormFile("bundle", "agent-bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(export.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	post := httptest.NewRequest(http.MethodPost, "/repos/team/demo/agents/import", &upload)
	post.SetBasicAuth("admin", admin)
	post.Header.Set("Content-Type", writer.FormDataContentType())
	result := httptest.NewRecorder()
	a.ServeHTTP(result, post)
	if result.Code != http.StatusSeeOther {
		t.Fatalf("web bundle import: %d %s", result.Code, result.Body.String())
	}
	file := filepath.Join(t.TempDir(), "bundle.json")
	if err := run([]string{"agent", "bundle", "export", "-data", root, "-out", file, "team/demo"}); err != nil {
		t.Fatalf("local CLI bundle export: %v", err)
	}
	if err := run([]string{"agent", "bundle", "import", "-data", root, "-file", file, "-expected-node-id", bundle.Payload.NodeID, "team/demo"}); err != nil {
		t.Fatalf("local CLI bundle import: %v", err)
	}
	if mode := mustStatMode(t, file); mode.Perm() != 0600 {
		t.Fatalf("export file permissions: %v", mode)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	remoteFile := filepath.Join(t.TempDir(), "remote-bundle.json")
	tokenFile := filepath.Join(root, tokenFilename)
	if err := run([]string{"api", "agent-bundle-export", "-url", server.URL, "-user", "admin", "-token-file", tokenFile, "-output", remoteFile, "team/demo"}); err != nil {
		t.Fatalf("remote CLI bundle export: %v", err)
	}
	if err := run([]string{"api", "agent-bundle-import", "-url", server.URL, "-user", "admin", "-token-file", tokenFile, "-bundle-file", remoteFile, "-expected-node-id", bundle.Payload.NodeID, "team/demo"}); err != nil {
		t.Fatalf("remote CLI bundle import: %v", err)
	}
	if mode := mustStatMode(t, remoteFile); mode.Perm() != 0600 {
		t.Fatalf("remote export file permissions: %v", mode)
	}
}

func mustStatMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}
