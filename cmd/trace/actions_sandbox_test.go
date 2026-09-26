package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMacOSSandboxRunsJobsAndConfinesThem runs real commands under the
// generated sandbox-exec profile: the shell must start and write inside the
// workspace (even when it is reached through a symlink such as /var), while
// files outside the workspace stay unreadable and unwritable.
func TestMacOSSandboxRunsJobsAndConfinesThem(t *testing.T) {
	if runtime.GOOS != "darwin" || !commandAvailable("sandbox-exec") {
		t.Skip("sandbox-exec is only available on macOS")
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("server data"), 0600); err != nil {
		t.Fatal(err)
	}
	runSandboxed := func(command string) (string, error) {
		cmd, err := actionCommand(context.Background(), command, workspace, true)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Dir = workspace
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := runSandboxed("printf ok > result.txt && printf x > /dev/null"); err != nil {
		t.Fatalf("sandboxed job could not write its workspace: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(workspace, "result.txt")); err != nil || string(b) != "ok" {
		t.Fatalf("sandboxed job output: %q %v", b, err)
	}
	if out, err := runSandboxed("cat " + outside); err == nil || strings.Contains(out, "server data") {
		t.Fatalf("sandboxed job read a file outside its workspace: %v\n%s", err, out)
	}
	escape := filepath.Join(filepath.Dir(outside), "escape.txt")
	if _, err := runSandboxed("printf x > " + escape); err == nil {
		t.Fatal("sandboxed job wrote outside its workspace")
	}
	if _, err := os.Stat(escape); err == nil {
		t.Fatal("sandboxed job created a file outside its workspace")
	}
}
