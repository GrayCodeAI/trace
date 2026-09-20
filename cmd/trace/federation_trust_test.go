package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFederationTrustPinsAndAdminControls(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	memberToken, err := a.store.addUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	first := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	second := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	source := "https://source.example/git/team/project.git"
	if err := a.store.checkOrPinFederationIdentity("team/project", source, first); err != nil {
		t.Fatal(err)
	}
	if err := a.store.checkOrPinFederationIdentity("team/project", source, first); err != nil {
		t.Fatal(err)
	}
	if err := a.store.checkOrPinFederationIdentity("team/project", source, second); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("changed identity accepted: %v", err)
	}
	pins, err := a.store.loadFederationTrust()
	if err != nil || len(pins) != 1 || pins[0].NodeID != first {
		t.Fatalf("unexpected pins: %+v %v", pins, err)
	}
	request := func(method, path, token string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.SetBasicAuth("admin", token)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	admin := adminToken(t, root)
	got := request(http.MethodGet, "/api/v1/federation/trust", admin, nil)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), first) {
		t.Fatalf("admin trust list: %d %s", got.Code, got.Body.String())
	}
	memberRequest := httptest.NewRequest(http.MethodGet, "/api/v1/federation/trust", nil)
	memberRequest.SetBasicAuth("alice", memberToken)
	memberResponse := httptest.NewRecorder()
	a.ServeHTTP(memberResponse, memberRequest)
	if memberResponse.Code != http.StatusForbidden {
		t.Fatalf("member trust access: %d", memberResponse.Code)
	}
	web := request(http.MethodGet, "/settings/federation", admin, nil)
	if web.Code != http.StatusOK || !strings.Contains(web.Body.String(), "Pinned source identities") || !strings.Contains(web.Body.String(), first) {
		t.Fatalf("trust settings page: %d %s", web.Code, web.Body.String())
	}
	wrong, _ := json.Marshal(map[string]string{"repo": "team/project", "source": source, "expected_node_id": second})
	if result := request(http.MethodDelete, "/api/v1/federation/trust", admin, wrong); result.Code != http.StatusBadRequest {
		t.Fatalf("wrong identity removed pin: %d", result.Code)
	}
	form := url.Values{"csrf": {a.csrfFor("admin")}, "action": {"forget-trust"}, "repo": {"team/project"}, "source": {source}, "node_id": {first}}
	post := httptest.NewRequest(http.MethodPost, "/settings/federation", strings.NewReader(form.Encode()))
	post.SetBasicAuth("admin", admin)
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postResponse := httptest.NewRecorder()
	a.ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusOK {
		t.Fatalf("forget trust via web: %d %s", postResponse.Code, postResponse.Body.String())
	}
	pins, err = a.store.loadFederationTrust()
	if err != nil || len(pins) != 0 {
		t.Fatalf("pin remains after explicit forget: %+v %v", pins, err)
	}
}

func TestFederationTrustRejectsDuplicateSource(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	record := federationTrust{Repo: "team/project", Source: "https://source.example/git/team/project.git", NodeID: nodeID}
	encoded, err := json.Marshal([]federationTrust{record, record})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, federationTrustFile), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadFederationTrust(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate source identity was accepted: %v", err)
	}
}
