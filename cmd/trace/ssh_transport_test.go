package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestSSHHostKeyEndpoint(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/.well-known/trace/ssh-host-key", nil)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("host key endpoint: got %d: %s", res.Code, res.Body.String())
	}
	var info struct {
		PublicKey   string `json:"public_key"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil || info.PublicKey == "" || info.Fingerprint == "" {
		t.Fatalf("invalid host key response: %s", res.Body.String())
	}
	if !bytes.HasPrefix([]byte(info.PublicKey), []byte("ssh-ed25519 ")) {
		t.Fatalf("unexpected host key: %q", info.PublicKey)
	}
}

func TestSSHHostKeyRotationAPI(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	request := func(method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/ssh/host-key", nil)
		req.SetBasicAuth("admin", token)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		return res
	}
	first := request(http.MethodGet)
	if first.Code != http.StatusOK {
		t.Fatalf("first key: %d", first.Code)
	}
	var before struct {
		Fingerprint string `json:"fingerprint"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &before)
	rotated := request(http.MethodPost)
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rotated.Code, rotated.Body.String())
	}
	var after struct {
		Fingerprint string `json:"fingerprint"`
		Restart     string `json:"restart_required"`
	}
	_ = json.Unmarshal(rotated.Body.Bytes(), &after)
	if before.Fingerprint == "" || after.Fingerprint == before.Fingerprint || after.Restart != "true" {
		t.Fatalf("rotation response: before=%q after=%+v", before.Fingerprint, after)
	}
}

func TestSSHKeyAuthAndGitCommand(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.createRepo("team/demo", false); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.updateUsers(func(db *userDB) error {
		u := db.Users["admin"]
		u.SSHKeys = []string{string(ssh.MarshalAuthorizedKey(publicKey))}
		db.Users["admin"] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	config, err := sshServerConfig(s)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() { _ = serveSSHListener(s, config, listener) }()
	clientConfig := &ssh.ClientConfig{User: "admin", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.InsecureIgnoreHostKey()}
	client, err := ssh.Dial("tcp", listener.Addr().String(), clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr
	if err := session.Run("git-upload-pack 'team/demo.git'"); err != nil && !bytes.Contains(stderr.Bytes(), []byte("remote end hung up")) {
		t.Fatalf("SSH Git command failed: %v stderr=%s", err, stderr.String())
	}
}
