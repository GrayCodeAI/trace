package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// sshPushFixture runs Trace's SSH listener for a repository seeded with one
// commit on main and a writer "alice" who authenticates with an SSH key.
type sshPushFixture struct {
	store *store
	addr  string
	key   string
}

func newSSHPushFixture(t *testing.T, repo string) *sshPushFixture {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh client is not installed")
	}
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.createRepo(repo, false); err != nil {
		t.Fatal(err)
	}
	seed := t.TempDir()
	gitTest(t, seed, "init", "--initial-branch=main")
	gitTest(t, seed, "config", "user.name", "Admin")
	gitTest(t, seed, "config", "user.email", "admin@example.invalid")
	gitTest(t, seed, "commit", "--allow-empty", "-m", "seed")
	repoPath, _ := s.repoPath(repo)
	push := exec.Command("git", "-C", seed, "push", repoPath, "main")
	push.Env = append(os.Environ(), "TRACE_ADMIN=1")
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	if _, err := s.addUser("alice", false); err != nil {
		t.Fatal(err)
	}
	if err := s.grantUser("alice", repo, "write"); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.updateUsers(func(db *userDB) error {
		u := db.Users["alice"]
		u.SSHKeys = []string{string(ssh.MarshalAuthorizedKey(publicKey))}
		db.Users["alice"] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
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
	t.Cleanup(func() { _ = listener.Close() })
	go func() { _ = serveSSHListener(s, config, listener) }()
	return &sshPushFixture{store: s, addr: listener.Addr().String(), key: keyPath}
}

// push clones the repository as alice over SSH, commits on branch, and
// pushes it back, returning the push output and error.
func (f *sshPushFixture) push(t *testing.T, repo, branch string) (string, error) {
	t.Helper()
	host, port, _ := net.SplitHostPort(f.addr)
	sshCommand := "ssh -F /dev/null -i " + f.key + " -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -p " + port
	remote := "ssh://alice@" + host + ":" + port + "/" + repo + ".git"
	git := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND="+sshCommand, "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	work := filepath.Join(t.TempDir(), "work")
	if out, err := git(t.TempDir(), "clone", remote, work); err != nil {
		t.Fatalf("SSH clone: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"config", "user.name", "Alice"}, {"config", "user.email", "alice@example.invalid"}, {"checkout", "-B", branch}, {"commit", "--allow-empty", "-m", "alice " + branch}} {
		if out, err := git(work, args...); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return git(work, "push", "origin", branch)
}

func TestSSHPushToArchivedRepositoryIsRejected(t *testing.T) {
	f := newSSHPushFixture(t, "team/old")
	if out, err := f.push(t, "team/old", "feature"); err != nil {
		t.Fatalf("writer SSH push before archiving failed: %v\n%s", err, out)
	}
	if err := f.store.setArchived("team/old", true); err != nil {
		t.Fatal(err)
	}
	if out, err := f.push(t, "team/old", "feature-2"); err == nil {
		t.Fatalf("SSH push to an archived repository succeeded:\n%s", out)
	}
}

func TestSSHPushIgnoresServerTraceAdminEnvironment(t *testing.T) {
	// An operator shell or unit file that exports TRACE_ADMIN=1 must not turn
	// every SSH push into an administrator push.
	t.Setenv("TRACE_ADMIN", "1")
	f := newSSHPushFixture(t, "team/protected")
	if out, err := f.push(t, "team/protected", "main"); err == nil {
		t.Fatalf("non-admin SSH push to protected main succeeded:\n%s", out)
	}
	if out, err := f.push(t, "team/protected", "feature"); err != nil {
		t.Fatalf("non-admin SSH push to a feature branch failed: %v\n%s", err, out)
	}
}
