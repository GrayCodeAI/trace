package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestActionArtifactsRefuseSymlinksAndSpecialFiles guards against a job
// planting a symlink (or a symlinked directory) in its workspace so that the
// unsandboxed Trace process copies a server file such as data/admin-token into
// the downloadable artifact store.
func TestActionArtifactsRefuseSymlinksAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "admin-token")
	if err := os.WriteFile(secret, []byte("server secret"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "reports", "result.txt"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"coverage.out": secret,
		"linkdir":      outside,
		"inner.txt":    filepath.Join(workspace, "reports", "result.txt"),
	} {
		if err := os.Symlink(target, filepath.Join(workspace, link)); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(workspace, "fifo.out"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan []actionArtifact, 1)
	go func() {
		done <- collectActionArtifacts(root, 1, 1, workspace, []string{"coverage.out", "linkdir/admin-token", "linkdir/*", "inner.txt", "fifo.out", "reports/*"})
	}()
	var artifacts []actionArtifact
	select {
	case artifacts = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("artifact collection blocked on a special file")
	}
	if len(artifacts) != 1 || artifacts[0].Name != "reports/result.txt" {
		t.Fatalf("unexpected artifacts: %+v", artifacts)
	}
	b, err := os.ReadFile(artifacts[0].Path)
	if err != nil || string(b) != "ok" {
		t.Fatalf("collected artifact: %q %v", b, err)
	}
	_ = filepath.Walk(filepath.Join(root, "artifacts"), func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			if content, readErr := os.ReadFile(path); readErr == nil && string(content) == "server secret" {
				t.Fatalf("server file was copied into the artifact store at %s", path)
			}
		}
		return nil
	})
}
