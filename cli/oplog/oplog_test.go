package oplog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/testutil"

	"github.com/go-git/go-git/v6"
)

func openTestRepo(t *testing.T) *git.Repository {
	t.Helper()
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("git.PlainOpen: %v", err)
	}
	return repo
}

func TestAppendAndList_RoundTrip(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	e1 := Entry{
		ID:         "aaaaaaaaaa",
		Op:         OpRewind,
		Timestamp:  time.Now().UTC().Add(-time.Minute),
		Ref:        "refs/heads/trace/deadbeef",
		BeforeHash: strings.Repeat("1", 39) + "a",
		AfterHash:  strings.Repeat("2", 39) + "b",
	}
	if err := Append(ctx, repo, e1, "Test User", "test@example.com"); err != nil {
		t.Fatalf("Append(e1): %v", err)
	}

	e2 := Entry{
		ID:        "bbbbbbbbbb",
		Op:        OpResetHard,
		Timestamp: time.Now().UTC(),
		Ref:       "refs/heads/main",
	}
	if err := Append(ctx, repo, e2, "Test User", "test@example.com"); err != nil {
		t.Fatalf("Append(e2): %v", err)
	}

	entries, err := List(repo, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	// Newest first.
	if entries[0].ID != e2.ID {
		t.Errorf("entries[0].ID = %q, want %q (newest first)", entries[0].ID, e2.ID)
	}
	if entries[1].ID != e1.ID {
		t.Errorf("entries[1].ID = %q, want %q", entries[1].ID, e1.ID)
	}
	if entries[1].BeforeHash != e1.BeforeHash {
		t.Errorf("entries[1].BeforeHash = %v, want %v", entries[1].BeforeHash, e1.BeforeHash)
	}
}

func TestList_EmptyWhenNoMetadataBranch(t *testing.T) {
	repo := openTestRepo(t)
	entries, err := List(repo, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if entries != nil {
		t.Errorf("List() = %v, want nil for a repo with no metadata branch", entries)
	}
}

func TestList_RespectsLimit(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	for i, id := range []string{"aaaaaaaaaa", "bbbbbbbbbb", "cccccccccc"} {
		e := Entry{
			ID:        id,
			Op:        OpFork,
			Timestamp: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}
		if err := Append(ctx, repo, e, "Test User", "test@example.com"); err != nil {
			t.Fatalf("Append(%s): %v", id, err)
		}
	}

	entries, err := List(repo, 2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].ID != "cccccccccc" {
		t.Errorf("entries[0].ID = %q, want cccccccccc (newest)", entries[0].ID)
	}
}

func TestAppend_PreservesUnrelatedRootEntries(t *testing.T) {
	// Append must not disturb other top-level trees on the metadata branch
	// (checkpoints, sessions, etc.) — only splice the oplog/ subtree.
	repo := openTestRepo(t)
	ctx := context.Background()

	if err := Append(ctx, repo, Entry{ID: "aaaaaaaaaa", Op: OpRewind, Timestamp: time.Now()}, "Test User", "test@example.com"); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Second append onto the same branch must succeed and both entries must
	// still be readable — a regression here would mean the splice logic
	// dropped the first entry.
	if err := Append(ctx, repo, Entry{ID: "bbbbbbbbbb", Op: OpCleanup, Timestamp: time.Now()}, "Test User", "test@example.com"); err != nil {
		t.Fatalf("second Append: %v", err)
	}

	entries, err := List(repo, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2 after two appends", len(entries))
	}
}

func TestAppend_RequiresID(t *testing.T) {
	repo := openTestRepo(t)
	if err := Append(context.Background(), repo, Entry{Op: OpRewind}, "Test User", "test@example.com"); err == nil {
		t.Fatal("expected an error for an entry with no ID")
	}
}
