package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/GrayCodeAI/trace/cli/oplog"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/testutil"

	"github.com/go-git/go-git/v6/plumbing"
)

func TestRunUndo_RestoresRewrittenRef(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)

	testutil.WriteFile(t, dir, "a.txt", "one")
	testutil.GitAdd(t, dir, "a.txt")
	testutil.GitCommit(t, dir, "first commit")
	before := testutil.GetHeadHash(t, dir)

	testutil.WriteFile(t, dir, "a.txt", "two")
	testutil.GitAdd(t, dir, "a.txt")
	testutil.GitCommit(t, dir, "second commit")
	after := testutil.GetHeadHash(t, dir)

	repo, err := openRepository(context.Background())
	if err != nil {
		t.Fatalf("openRepository: %v", err)
	}

	// Simulate what resetShadowBranchToCheckpoint/performGitResetHard
	// record: a rewind moved "refs/heads/master" (or main) from `before` to
	// `after` — actually mutate the branch ref directly here, mirroring the
	// production ref-rewrite pattern, then record it in the oplog exactly
	// as the real mutation points do.
	headRef, err := repo.Head()
	if err != nil {
		t.Fatalf("repo.Head: %v", err)
	}
	branchRefName := headRef.Name()

	if err := strategy.RecordOplogEntry(
		context.Background(), repo, oplog.OpRewind, branchRefName.String(),
		plumbing.NewHash(before), plumbing.NewHash(after), "",
	); err != nil {
		t.Fatalf("RecordOplogEntry: %v", err)
	}

	// Now move the branch ref forward again, as if the rewind itself had
	// already applied (before -> after was the rewind; here we just verify
	// undo restores the ref to `before`).
	if err := repo.Storer.SetReference(plumbing.NewHashReference(branchRefName, plumbing.NewHash(after))); err != nil {
		t.Fatalf("SetReference: %v", err)
	}

	var out bytes.Buffer
	if err := runUndo(context.Background(), &out); err != nil {
		t.Fatalf("runUndo: %v", err)
	}

	ref, err := repo.Reference(branchRefName, true)
	if err != nil {
		t.Fatalf("repo.Reference: %v", err)
	}
	if ref.Hash().String() != before {
		t.Errorf("branch ref = %s, want %s (restored to before-hash)", ref.Hash().String(), before)
	}

	if out.String() == "" {
		t.Error("runUndo produced no output summary")
	}
}

func TestRunUndo_EmptyOplog(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)

	testutil.WriteFile(t, dir, "a.txt", "one")
	testutil.GitAdd(t, dir, "a.txt")
	testutil.GitCommit(t, dir, "first commit")

	var out bytes.Buffer
	if err := runUndo(context.Background(), &out); err != nil {
		t.Fatalf("runUndo: %v", err)
	}
	if out.String() != "Nothing to undo — the operation log is empty.\n" {
		t.Errorf("runUndo output = %q, want the empty-oplog message", out.String())
	}
}

func TestRunUndo_RefusesToChainUndos(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)

	testutil.WriteFile(t, dir, "a.txt", "one")
	testutil.GitAdd(t, dir, "a.txt")
	testutil.GitCommit(t, dir, "first commit")

	repo, err := openRepository(context.Background())
	if err != nil {
		t.Fatalf("openRepository: %v", err)
	}
	authorName, authorEmail := strategy.GetGitAuthorFromRepo(repo)
	entry := oplog.Entry{
		ID:  "aaaaaaaaaa",
		Op:  oplog.OpUndo,
		Ref: "refs/heads/does-not-matter",
	}
	if err := oplog.Append(context.Background(), repo, entry, authorName, authorEmail); err != nil {
		t.Fatalf("oplog.Append: %v", err)
	}

	var out bytes.Buffer
	if err := runUndo(context.Background(), &out); err == nil {
		t.Fatal("expected runUndo to refuse chaining an undo, got nil error")
	}
}
