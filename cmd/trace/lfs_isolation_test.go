package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func lfsUpload(t *testing.T, a *app, user, token, repo string, content []byte) string {
	t.Helper()
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	req := httptest.NewRequest(http.MethodPut, "/lfs/"+repo+".git/info/lfs/objects/"+oid, bytes.NewReader(content))
	req.SetBasicAuth(user, token)
	req.ContentLength = int64(len(content))
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("LFS upload to %s: %d %s", repo, res.Code, res.Body.String())
	}
	return oid
}

func lfsDownload(a *app, user, token, repo, oid string) (int, []byte) {
	req := httptest.NewRequest(http.MethodGet, "/lfs/"+repo+".git/info/lfs/objects/"+oid, nil)
	req.SetBasicAuth(user, token)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	body, _ := io.ReadAll(res.Body)
	return res.Code, body
}

func lfsBatchDownloadError(t *testing.T, a *app, user, token, repo, oid string, size int) *lfsObjectError {
	t.Helper()
	body, _ := json.Marshal(lfsBatchRequest{Operation: "download", Objects: []lfsObjectRequest{{OID: oid, Size: int64(size)}}})
	req := httptest.NewRequest(http.MethodPost, "/lfs/"+repo+".git/info/lfs/objects", bytes.NewReader(body))
	req.SetBasicAuth(user, token)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	var out lfsBatchResponse
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil || len(out.Objects) != 1 {
		t.Fatalf("batch response: %d %s", res.Code, res.Body.String())
	}
	return out.Objects[0].Error
}

// TestLFSObjectsAreScopedToTheirRepository reproduces the leak: a user who
// can read one repository could download any LFS object on the node by OID.
func TestLFSObjectsAreScopedToTheirRepository(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"team/public-ish", "team/secret"} {
		if err := a.store.createRepo(name, false); err != nil {
			t.Fatal(err)
		}
	}
	aliceToken, err := a.store.addUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.grantUser("alice", "team/public-ish", "read"); err != nil {
		t.Fatal(err)
	}
	admin := adminToken(t, root)
	secret := []byte("confidential design document\n")
	oid := lfsUpload(t, a, "admin", admin, "team/secret", secret)

	if code, body := lfsDownload(a, "alice", aliceToken, "team/public-ish", oid); code == http.StatusOK || bytes.Contains(body, secret) {
		t.Fatalf("object from team/secret was downloadable through team/public-ish: %d", code)
	}
	if objErr := lfsBatchDownloadError(t, a, "alice", aliceToken, "team/public-ish", oid, len(secret)); objErr == nil || objErr.Code != http.StatusNotFound {
		t.Fatalf("batch download advertised another repository's object: %+v", objErr)
	}
	if code, body := lfsDownload(a, "admin", admin, "team/secret", oid); code != http.StatusOK || !bytes.Equal(body, secret) {
		t.Fatalf("owner repository download: %d", code)
	}

	// Forks get their own copy; transfers carry objects; deletes remove them.
	if err := a.store.forkRepo("team/secret", "team/fork"); err != nil {
		t.Fatal(err)
	}
	if code, _ := lfsDownload(a, "admin", admin, "team/fork", oid); code != http.StatusOK {
		t.Fatalf("fork lost LFS objects: %d", code)
	}
	if err := a.store.transferRepo("team/fork", "other/moved"); err != nil {
		t.Fatal(err)
	}
	if code, _ := lfsDownload(a, "admin", admin, "other/moved", oid); code != http.StatusOK {
		t.Fatalf("transfer lost LFS objects: %d", code)
	}
	if err := a.store.deleteRepo("other/moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "lfs", "other", "moved")); !os.IsNotExist(err) {
		t.Fatalf("deleted repository kept its LFS objects: %v", err)
	}
	if code, _ := lfsDownload(a, "admin", admin, "team/secret", oid); code != http.StatusOK {
		t.Fatalf("deleting a fork removed the source repository's object: %d", code)
	}
}

func TestLegacyLFSObjectsMigrateToReferencingRepositories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"team/uses-it", "team/unrelated"} {
		if err := a.store.createRepo(name, false); err != nil {
			t.Fatal(err)
		}
	}
	content := []byte("legacy object\n")
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	orphan := bytes.Repeat([]byte("o"), 10)
	orphanSum := sha256.Sum256(orphan)
	orphanOID := hex.EncodeToString(orphanSum[:])
	if err := os.MkdirAll(filepath.Join(root, "lfs"), 0700); err != nil {
		t.Fatal(err)
	}
	for id, data := range map[string][]byte{oid: content, orphanOID: orphan} {
		if err := os.WriteFile(filepath.Join(root, "lfs", id), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize " + strconv.Itoa(len(content)) + "\n"
	commitFiles(t, a.store, "team/uses-it", map[string][]byte{"asset.bin": []byte(pointer), ".gitattributes": []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n")})
	commitFiles(t, a.store, "team/unrelated", map[string][]byte{"README.md": []byte("nothing here\n")})

	if err := a.store.migrateLegacyLFS(); err != nil {
		t.Fatal(err)
	}
	if err := a.store.migrateLegacyLFS(); err != nil {
		t.Fatalf("migration is not idempotent: %v", err)
	}
	admin := adminToken(t, root)
	if code, body := lfsDownload(a, "admin", admin, "team/uses-it", oid); code != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("migrated object not served for its repository: %d", code)
	}
	if code, _ := lfsDownload(a, "admin", admin, "team/unrelated", oid); code == http.StatusOK {
		t.Fatal("migrated object leaked into a repository that never referenced it")
	}
	for _, id := range []string{oid, orphanOID} {
		if _, err := os.Stat(filepath.Join(root, "lfs", id)); !os.IsNotExist(err) {
			t.Fatalf("legacy global object %s is still in place: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "lfs", ".legacy-unreferenced", orphanOID)); err != nil {
		t.Fatalf("unreferenced legacy object was not preserved: %v", err)
	}
}
