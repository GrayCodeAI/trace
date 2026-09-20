package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignedAgentHistoryRoundTripsThroughGitRef(t *testing.T) {
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
	if _, err := source.createAgentSession("team/demo", "Cursor", "main", "token=private Reviewed change", "admin"); err != nil {
		t.Fatal(err)
	}
	first, err := source.publishAgentGitBundle("team/demo")
	if err != nil || !first.Changed || !strings.HasPrefix(first.Ref, agentGitRefPrefix) || !isFullHexSHA(first.Commit) {
		t.Fatalf("first Git publish: %+v %v", first, err)
	}
	sourcePath, _ := source.repoPath("team/demo")
	bundle, _, err := readAgentGitBundle(sourcePath, first.Ref, first.NodeID, "team/demo")
	if err != nil || len(bundle.Payload.Sessions) != 1 || strings.Contains(bundle.Payload.Sessions[0].Summary, "private") {
		t.Fatalf("published bundle invalid or unredacted: %v", err)
	}
	again, err := source.publishAgentGitBundle("team/demo")
	if err != nil || again.Changed || again.Commit != first.Commit {
		t.Fatalf("unchanged history created a new commit: %+v %v", again, err)
	}
	if err := source.setPublic("team/demo", true); err == nil {
		t.Fatal("repository became public with agent history ref present")
	}
	targetPath, _ := target.repoPath("team/demo")
	sourceApp, err := newApp(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(sourceApp)
	defer server.Close()
	credential := base64.StdEncoding.EncodeToString([]byte("admin:" + adminToken(t, sourceRoot)))
	fetch := exec.Command("git", "-C", targetPath, "fetch", server.URL+"/git/team/demo.git", "+"+first.Ref+":"+first.Ref)
	fetch.Env = append(os.Environ(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+credential, "GIT_TERMINAL_PROMPT=0")
	if err := fetch.Run(); err != nil {
		t.Fatalf("authenticated Git HTTP fetch of agent history failed: %v", err)
	}
	wrongID := strings.Repeat("A", len(first.NodeID))
	if _, err := target.importAgentGitBundle("team/demo", wrongID); err == nil {
		t.Fatal("wrong source node identity accepted")
	}
	imported, err := target.importAgentGitBundle("team/demo", first.NodeID)
	if err != nil || imported.SessionsCreated != 1 {
		t.Fatalf("Git import: %+v %v", imported, err)
	}
	imported, err = target.importAgentGitBundle("team/demo", first.NodeID)
	if err != nil || imported.SessionsCreated != 0 || imported.CheckpointsAdded != 0 {
		t.Fatalf("repeat Git import: %+v %v", imported, err)
	}
	gitTest(t, work, "commit", "--allow-empty", "-m", "second")
	gitAdminPush(t, work, sourcePath, "main")
	gitAdminPush(t, work, targetPath, "main")
	if _, err := source.addAgentCheckpoint("team/demo", bundle.Payload.Sessions[0].ID, "main", "Second review", "done"); err != nil {
		t.Fatal(err)
	}
	second, err := source.publishAgentGitBundle("team/demo")
	if err != nil || !second.Changed || second.Commit == first.Commit {
		t.Fatalf("incremental Git publish: %+v %v", second, err)
	}
	gitTest(t, targetPath, "fetch", sourcePath, "+"+first.Ref+":"+first.Ref)
	imported, err = target.importAgentGitBundle("team/demo", first.NodeID)
	if err != nil || imported.SessionsCreated != 0 || imported.CheckpointsAdded != 1 {
		t.Fatalf("incremental Git import: %+v %v", imported, err)
	}
	if _, err := source.unpublishAgentGitBundle("team/demo", wrongID); err == nil {
		t.Fatal("unpublish accepted wrong node identity")
	}
	if _, err := source.unpublishAgentGitBundle("team/demo", first.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := source.setPublic("team/demo", true); err != nil {
		t.Fatalf("could not make repository public after ref removal: %v", err)
	}
	if _, err := source.publishAgentGitBundle("team/demo"); err == nil {
		t.Fatal("agent history published into public repository")
	}
	// A fetched ref is independently verifiable and cannot be silently rewritten.
	tampered := bundle
	tampered.Payload.Sessions = append([]agentSession(nil), bundle.Payload.Sessions...)
	tampered.Payload.Sessions[0].Summary = "rewritten"
	encoded, _ := json.Marshal(tampered)
	blob, err := gitInput(targetPath, encoded, nil, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := gitInput(targetPath, []byte("100644 blob "+blob+"\tbundle.json\n"), nil, "mktree")
	if err != nil {
		t.Fatal(err)
	}
	identity := []string{"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid"}
	badCommit, err := gitInput(targetPath, nil, identity, "commit-tree", tree, "-m", "tampered")
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, targetPath, "update-ref", first.Ref, badCommit, second.Commit)
	if _, err := target.importAgentGitBundle("team/demo", first.NodeID); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered Git bundle accepted: %v", err)
	}
}

func TestAgentGitAPIWebAndCLI(t *testing.T) {
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
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Test")
	gitTest(t, work, "config", "user.email", "test@example.invalid")
	gitTest(t, work, "commit", "--allow-empty", "-m", "first")
	path, _ := a.store.repoPath("team/demo")
	gitAdminPush(t, work, path, "main")
	if _, err := a.store.createAgentSession("team/demo", "Codex", "main", "Reviewed feature", "admin"); err != nil {
		t.Fatal(err)
	}
	memberToken, err := a.store.addUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.grantUser("alice", "team/demo", "read"); err != nil {
		t.Fatal(err)
	}
	admin := adminToken(t, root)
	request := func(method, endpoint, user, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, endpoint, nil)
		r.SetBasicAuth(user, token)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	status := request(http.MethodGet, "/api/v1/repos/team/demo/agent-sessions/git-bundle", "alice", memberToken)
	var result agentGitResult
	if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &result) != nil || result.Commit != "" || result.Ref == "" {
		t.Fatalf("initial Git bundle status: %d", status.Code)
	}
	if member := request(http.MethodPost, "/api/v1/repos/team/demo/agent-sessions/git-bundle", "alice", memberToken); member.Code != http.StatusForbidden {
		t.Fatalf("member published Git history: %d", member.Code)
	}
	if err := run([]string{"agent", "git", "publish", "-data", root, "team/demo"}); err != nil {
		t.Fatalf("CLI Git publish: %v", err)
	}
	status = request(http.MethodGet, "/api/v1/repos/team/demo/agent-sessions/git-bundle", "alice", memberToken)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"sessions":1`) {
		t.Fatalf("published Git bundle status: %d", status.Code)
	}
	web := request(http.MethodGet, "/repos/team/demo/agents", "admin", admin)
	if web.Code != http.StatusOK || !strings.Contains(web.Body.String(), "Git portable history") || !strings.Contains(web.Body.String(), "Update Git snapshot") {
		t.Fatalf("agent Git web card: %d", web.Code)
	}
	gitTest(t, work, "commit", "--allow-empty", "-m", "second")
	gitAdminPush(t, work, path, "main")
	if _, err := a.store.addAgentCheckpoint("team/demo", 1, "main", "Second review", "done"); err != nil {
		t.Fatal(err)
	}
	badForm := httptest.NewRequest(http.MethodPost, "/repos/team/demo/agents/publish-git", strings.NewReader("csrf=wrong"))
	badForm.SetBasicAuth("admin", admin)
	badForm.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badW := httptest.NewRecorder()
	a.ServeHTTP(badW, badForm)
	if badW.Code != http.StatusForbidden {
		t.Fatalf("bad CSRF accepted: %d", badW.Code)
	}
	goodForm := httptest.NewRequest(http.MethodPost, "/repos/team/demo/agents/publish-git", strings.NewReader("csrf="+a.csrfFor("admin")))
	goodForm.SetBasicAuth("admin", admin)
	goodForm.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	goodW := httptest.NewRecorder()
	a.ServeHTTP(goodW, goodForm)
	if goodW.Code != http.StatusSeeOther {
		t.Fatalf("web Git publish: %d", goodW.Code)
	}
	status = request(http.MethodGet, "/api/v1/repos/team/demo/agent-sessions/git-bundle", "admin", admin)
	if err := json.Unmarshal(status.Body.Bytes(), &result); err != nil || result.Commit == "" || result.Sessions != 1 {
		t.Fatalf("updated Git bundle status: %+v %v", result, err)
	}
}
