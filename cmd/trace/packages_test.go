package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagePublishListDownload(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/pkg", false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	token := adminToken(t, root)
	data := []byte("package artifact")
	put, _ := http.NewRequest(http.MethodPut, server.URL+"/packages/team/pkg.git/lib/1.0.0/file.tgz", bytes.NewReader(data))
	put.SetBasicAuth("admin", token)
	putRes, err := http.DefaultClient.Do(put)
	if err != nil {
		t.Fatal(err)
	}
	var artifact packageArtifact
	if putRes.StatusCode != http.StatusCreated || json.NewDecoder(putRes.Body).Decode(&artifact) != nil {
		putRes.Body.Close()
		t.Fatalf("package publish: %d", putRes.StatusCode)
	}
	putRes.Body.Close()
	if artifact.Name != "lib" || artifact.Version != "1.0.0" || artifact.Size != int64(len(data)) || artifact.SHA256 == "" {
		t.Fatalf("unexpected artifact: %+v", artifact)
	}
	listReq, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/repos/team/pkg/packages", nil)
	listReq.SetBasicAuth("admin", token)
	listRes, _ := http.DefaultClient.Do(listReq)
	var listed []packageArtifact
	_ = json.NewDecoder(listRes.Body).Decode(&listed)
	listRes.Body.Close()
	if listRes.StatusCode != http.StatusOK || len(listed) != 1 {
		t.Fatalf("package list: %d %+v", listRes.StatusCode, listed)
	}
	getReq, _ := http.NewRequest(http.MethodGet, server.URL+"/packages/team/pkg.git/lib/1.0.0/file.tgz", nil)
	getReq.SetBasicAuth("admin", token)
	getRes, _ := http.DefaultClient.Do(getReq)
	got, _ := io.ReadAll(getRes.Body)
	getRes.Body.Close()
	if getRes.StatusCode != http.StatusOK || !bytes.Equal(got, data) || !strings.Contains(getRes.Header.Get("X-Trace-Package-SHA256"), artifact.SHA256) {
		t.Fatalf("package download: %d %q", getRes.StatusCode, got)
	}
	npmPayload := `{"name":"lib","versions":{"1.1.0":{"version":"1.1.0"}},"_attachments":{"lib-1.1.0.tgz":{"data":"` + base64.StdEncoding.EncodeToString([]byte("npm package")) + `"}}}`
	npmPut, _ := http.NewRequest(http.MethodPut, server.URL+"/npm/team/pkg/lib", strings.NewReader(npmPayload))
	npmPut.SetBasicAuth("admin", token)
	npmPut.Header.Set("Content-Type", "application/json")
	npmPutRes, _ := http.DefaultClient.Do(npmPut)
	if npmPutRes.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(npmPutRes.Body)
		npmPutRes.Body.Close()
		t.Fatalf("npm publish: %d %s", npmPutRes.StatusCode, body)
	}
	npmPutRes.Body.Close()
	npmReq, _ := http.NewRequest(http.MethodGet, server.URL+"/npm/team/pkg/lib", nil)
	npmReq.SetBasicAuth("admin", token)
	npmRes, _ := http.DefaultClient.Do(npmReq)
	npmBody, _ := io.ReadAll(npmRes.Body)
	npmRes.Body.Close()
	if npmRes.StatusCode != http.StatusOK || !strings.Contains(string(npmBody), `"latest":"1.1.0"`) {
		t.Fatalf("npm metadata: %d %s", npmRes.StatusCode, npmBody)
	}
	npmTar, _ := http.NewRequest(http.MethodGet, server.URL+"/npm/team/pkg/lib/-/file.tgz", nil)
	npmTar.SetBasicAuth("admin", token)
	npmTarRes, _ := http.DefaultClient.Do(npmTar)
	npmTarBody, _ := io.ReadAll(npmTarRes.Body)
	npmTarRes.Body.Close()
	if npmTarRes.StatusCode != http.StatusOK || !bytes.Equal(npmTarBody, data) {
		t.Fatalf("npm tarball: %d %q", npmTarRes.StatusCode, npmTarBody)
	}
	pypiReq, _ := http.NewRequest(http.MethodGet, server.URL+"/pypi/team/pkg/simple/", nil)
	pypiReq.SetBasicAuth("admin", token)
	pypiRes, _ := http.DefaultClient.Do(pypiReq)
	pypiBody, _ := io.ReadAll(pypiRes.Body)
	pypiRes.Body.Close()
	if pypiRes.StatusCode != http.StatusOK || !strings.Contains(string(pypiBody), "lib") {
		t.Fatalf("pypi index: %d %s", pypiRes.StatusCode, pypiBody)
	}
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	_ = writer.WriteField("name", "lib")
	_ = writer.WriteField("version", "2.0.0")
	part, _ := writer.CreateFormFile("content", "lib-2.0.0-py3-none-any.whl")
	_, _ = part.Write([]byte("wheel artifact"))
	_ = writer.Close()
	pypiUpload, _ := http.NewRequest(http.MethodPost, server.URL+"/pypi/team/pkg/", &upload)
	pypiUpload.SetBasicAuth("admin", token)
	pypiUpload.Header.Set("Content-Type", writer.FormDataContentType())
	pypiUploadRes, _ := http.DefaultClient.Do(pypiUpload)
	if pypiUploadRes.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(pypiUploadRes.Body)
		pypiUploadRes.Body.Close()
		t.Fatalf("pypi upload: %d %s", pypiUploadRes.StatusCode, body)
	}
	pypiUploadRes.Body.Close()
	pageReq, _ := http.NewRequest(http.MethodGet, server.URL+"/repos/team/pkg/packages", nil)
	pageReq.SetBasicAuth("admin", token)
	pageRes, _ := http.DefaultClient.Do(pageReq)
	pageBody, _ := io.ReadAll(pageRes.Body)
	pageRes.Body.Close()
	if pageRes.StatusCode != http.StatusOK || !strings.Contains(string(pageBody), "lib@1.0.0") {
		t.Fatalf("package page: %d %s", pageRes.StatusCode, pageBody)
	}
}
