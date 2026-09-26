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
	"testing"
)

func TestLFSBatchUploadAndDownload(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/lfs", false); err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	server := httptest.NewServer(a)
	defer server.Close()
	content := []byte("large-ish test object\n")
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	batchBody, _ := json.Marshal(lfsBatchRequest{Operation: "upload", Transfers: []string{"basic"}, Objects: []lfsObjectRequest{{OID: oid, Size: int64(len(content))}}})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/lfs/team/lfs.git/info/lfs/objects", bytes.NewReader(batchBody))
	req.SetBasicAuth("admin", token)
	req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var batch lfsBatchResponse
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&batch) != nil || len(batch.Objects) != 1 || batch.Objects[0].Actions["upload"].Href == "" {
		res.Body.Close()
		t.Fatalf("batch upload response: %d %+v", res.StatusCode, batch)
	}
	res.Body.Close()
	put, _ := http.NewRequest(http.MethodPut, batch.Objects[0].Actions["upload"].Href, bytes.NewReader(content))
	put.SetBasicAuth("admin", token)
	putRes, err := http.DefaultClient.Do(put)
	if err != nil {
		t.Fatal(err)
	}
	putRes.Body.Close()
	if putRes.StatusCode != http.StatusCreated {
		t.Fatalf("LFS upload: %d", putRes.StatusCode)
	}
	get, _ := http.NewRequest(http.MethodGet, server.URL+"/lfs/team/lfs.git/info/lfs/objects/"+oid, nil)
	get.SetBasicAuth("admin", token)
	getRes, err := http.DefaultClient.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(getRes.Body)
	getRes.Body.Close()
	if getRes.StatusCode != http.StatusOK || !bytes.Equal(got, content) {
		t.Fatalf("LFS download: %d %q", getRes.StatusCode, got)
	}
	// Objects are stored per repository.
	if _, err := os.Stat(filepath.Join(root, "lfs", "team", "lfs", oid)); err != nil {
		t.Fatal(err)
	}
}
