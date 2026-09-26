package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// commitFiles writes files into a new commit on main of repo's bare
// repository (bypassing the protection hook) and returns the repo path.
func commitFiles(t *testing.T, s *store, repo string, files map[string][]byte) string {
	t.Helper()
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	for name, content := range files {
		path := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "files")
	repoPath, _ := s.repoPath(repo)
	push := exec.Command("git", "-C", work, "push", "--force", repoPath, "main")
	push.Env = append(os.Environ(), "TRACE_ADMIN=1")
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	return repoPath
}

// TestRawAndPagesDoNotBufferOversizedBlobs checks that a blob larger than
// the 8 MiB cap is refused without reading it into memory: before the fix,
// the whole blob was buffered and only then compared with the cap.
func TestRawAndPagesDoNotBufferOversizedBlobs(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.createRepo("team/big", false); err != nil {
		t.Fatal(err)
	}
	// Zeros compress well, so the repository stays small on disk.
	big := bytes.Repeat([]byte{0}, 48<<20)
	commitFiles(t, s, "team/big", map[string][]byte{"big.bin": big, "site/index.html": []byte("<p>ok</p>"), "site/dir/x.txt": []byte("x")})
	big = nil
	if err := s.setPages("team/big", "main", "", true); err != nil {
		t.Fatal(err)
	}
	measure := func(read func() error) (uint64, error) {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err := read()
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc, err
	}
	allocated, err := measure(func() error { _, _, err := s.rawFile("team/big", "main", "big.bin"); return err })
	if err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("oversized raw file was not refused: %v", err)
	}
	if allocated > 16<<20 {
		t.Fatalf("raw read buffered %d bytes for an oversized blob", allocated)
	}
	allocated, err = measure(func() error { _, _, err := s.pagesFile("team/big", "big.bin"); return err })
	if err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("oversized pages file was not refused: %v", err)
	}
	if allocated > 16<<20 {
		t.Fatalf("pages read buffered %d bytes for an oversized blob", allocated)
	}
	if body, _, err := s.pagesFile("team/big", "site/index.html"); err != nil || string(body) != "<p>ok</p>" {
		t.Fatalf("small pages file: %q %v", body, err)
	}
	// A directory path must not render Git's tree listing as page content.
	if body, _, err := s.pagesFile("team/big", "site/dir"); err == nil {
		t.Fatalf("pages served a tree listing: %q", body)
	}
	if body, _, err := s.rawFile("team/big", "main", "site"); err == nil {
		t.Fatalf("raw served a tree: %q", body)
	}
}
