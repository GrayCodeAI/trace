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
