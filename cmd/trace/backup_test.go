package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupCreateVerifyAndRestore(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker.json"), []byte(`{"marker":"trace"}`), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "trace.tar.gz")
	if err := createBackup(root, archive); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackup(archive); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := restoreBackup(archive, restored, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(restored, "marker.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"marker":"trace"}` {
		t.Fatalf("restored marker %q", b)
	}
}

func TestBackupRejectsUnsafeEntry(t *testing.T) {
	if err := validateArchiveName("../escape"); err == nil {
		t.Fatal("unsafe archive path accepted")
	}
	if err := validateArchiveName("/absolute"); err == nil {
		t.Fatal("absolute archive path accepted")
	}
}

func TestBackupRefusesOutputInsideDataDirectory(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "data-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{
		filepath.Join(root, "backup.tar.gz"),
		filepath.Join(root, "repos", "nested.tar.gz"),
		filepath.Join(link, "via-symlink.tar.gz"),
	} {
		if err := createBackup(root, output); err == nil {
			t.Fatalf("backup written inside the data directory: %s", output)
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("refused backup still created %s: %v", output, err)
		}
	}
	// A sibling directory whose name merely starts with the data path is fine.
	sibling := root + "-backups"
	if err := os.MkdirAll(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sibling)
	if err := createBackup(root, filepath.Join(sibling, "ok.tar.gz")); err != nil {
		t.Fatalf("backup next to the data directory refused: %v", err)
	}
}
