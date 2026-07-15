// Package oplog is a jj-style operation log: a record of every
// state-mutating git operation trace performs (rewind, reset --hard, fork,
// checkpoint cleanup), kept separately from the operation each of those
// performs so a bad operation can itself be undone.
//
// Before this package existed, the only record of these operations was
// git's own reflog — not queryable via trace, expiring on its own schedule,
// and not covering the shadow-branch ref rewrites rewind/cleanup perform
// directly via SetReference (those don't touch HEAD, so they never appear
// in the reflog at all).
//
// Entries are stored as JSON blobs on the trace/checkpoints/v1 orphan
// branch, in the same sharded-path convention checkpoints already use
// (oplog/<id[:2]>/<id[2:]>/entry.json). That means entries participate in
// the same push/fetch/merge sync checkpoints already have — no new sync
// mechanism was built for this.
package oplog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/paths"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// Op identifies the kind of state-mutating operation recorded in the log.
type Op string

const (
	OpRewind    Op = "rewind"
	OpResetHard Op = "reset_hard"
	OpFork      Op = "fork"
	OpCleanup   Op = "cleanup"
	OpUndo      Op = "undo"
)

// Entry is a single record in the operation log.
//
// BeforeHash/AfterHash are stored as plain hex strings (via
// plumbing.Hash.String()), not plumbing.Hash itself — that type keeps its
// bytes in unexported fields with no custom (Un)MarshalJSON, so a struct
// containing it round-trips through encoding/json as all-zero with no error
// at all, which is exactly the kind of silent-corruption bug this package
// exists to avoid.
type Entry struct {
	ID           string    `json:"id"`
	Op           Op        `json:"op"`
	Timestamp    time.Time `json:"timestamp"`
	Ref          string    `json:"ref"`
	BeforeHash   string    `json:"before_hash"`
	AfterHash    string    `json:"after_hash"`
	CheckpointID string    `json:"checkpoint_id,omitempty"`
	Detail       string    `json:"detail,omitempty"`
}

const oplogDir = "oplog"

// Append records a new operation-log entry, committing it onto the
// trace/checkpoints/v1 orphan branch (creating the branch if it doesn't yet
// exist). authorName/authorEmail identify the committer for the oplog
// commit itself; callers typically already have these on hand via
// strategy.GetGitAuthorFromRepo.
func Append(ctx context.Context, repo *git.Repository, e Entry, authorName, authorEmail string) error {
	if e.ID == "" {
		return errors.New("oplog: entry ID is required")
	}
	if len(e.ID) < 3 {
		return fmt.Errorf("oplog: entry ID %q is too short to shard", e.ID)
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}

	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("oplog: marshal entry: %w", err)
	}
	blobHash, err := checkpoint.CreateBlobFromContent(repo, data)
	if err != nil {
		return fmt.Errorf("oplog: create blob: %w", err)
	}

	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	var parentHash, rootTreeHash plumbing.Hash
	ref, err := repo.Reference(refName, true)
	switch {
	case err == nil:
		parentHash = ref.Hash()
		commit, cErr := repo.CommitObject(parentHash)
		if cErr != nil {
			return fmt.Errorf("oplog: read metadata branch commit: %w", cErr)
		}
		rootTreeHash = commit.TreeHash
	case errors.Is(err, plumbing.ErrReferenceNotFound):
		parentHash = plumbing.ZeroHash
		rootTreeHash = plumbing.ZeroHash
	default:
		return fmt.Errorf("oplog: read metadata branch ref: %w", err)
	}

	newRootTreeHash, err := spliceOplogEntry(ctx, repo, rootTreeHash, e.ID, blobHash)
	if err != nil {
		return err
	}

	msg := fmt.Sprintf("oplog: %s\n\nRecorded by trace to allow undoing this operation.\n", e.Op)
	commitHash, err := checkpoint.CreateCommit(ctx, repo, newRootTreeHash, parentHash, msg, authorName, authorEmail)
	if err != nil {
		return fmt.Errorf("oplog: create commit: %w", err)
	}

	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, commitHash)); err != nil {
		return fmt.Errorf("oplog: update metadata branch ref: %w", err)
	}
	return nil
}

// spliceOplogEntry rebuilds only the "oplog/" subtree of the metadata
// branch's root tree, leaving every other top-level entry (checkpoints,
// sessions, etc.) untouched and byte-identical — those subtrees keep their
// existing hashes rather than being needlessly re-encoded.
func spliceOplogEntry(ctx context.Context, repo *git.Repository, rootTreeHash plumbing.Hash, id string, blobHash plumbing.Hash) (plumbing.Hash, error) {
	oplogEntries := map[string]object.TreeEntry{}
	var otherRootEntries []object.TreeEntry

	if rootTreeHash != plumbing.ZeroHash {
		rootTree, err := repo.TreeObject(rootTreeHash)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("oplog: read root tree: %w", err)
		}
		for _, entry := range rootTree.Entries {
			if entry.Name == oplogDir {
				oplogTree, err := repo.TreeObject(entry.Hash)
				if err != nil {
					return plumbing.ZeroHash, fmt.Errorf("oplog: read oplog subtree: %w", err)
				}
				if err := checkpoint.FlattenTree(repo, oplogTree, "", oplogEntries); err != nil {
					return plumbing.ZeroHash, fmt.Errorf("oplog: flatten oplog subtree: %w", err)
				}
				continue
			}
			otherRootEntries = append(otherRootEntries, entry)
		}
	}

	shardPath := id[:2] + "/" + id[2:] + "/entry.json"
	oplogEntries[shardPath] = object.TreeEntry{Mode: filemode.Regular, Hash: blobHash}

	newOplogTreeHash, err := checkpoint.BuildTreeFromEntries(ctx, repo, oplogEntries)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("oplog: build oplog subtree: %w", err)
	}

	newRootEntries := append(otherRootEntries, object.TreeEntry{Name: oplogDir, Mode: filemode.Dir, Hash: newOplogTreeHash})
	sort.Slice(newRootEntries, func(i, j int) bool { return newRootEntries[i].Name < newRootEntries[j].Name })

	newTree := &object.Tree{Entries: newRootEntries}
	obj := repo.Storer.NewEncodedObject()
	if err := newTree.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("oplog: encode root tree: %w", err)
	}
	return repo.Storer.SetEncodedObject(obj)
}

// List returns operation-log entries, newest first. limit <= 0 means no
// limit. Returns (nil, nil) if the metadata branch or the oplog subtree
// doesn't exist yet (nothing has been recorded).
func List(repo *git.Repository, limit int) ([]Entry, error) {
	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	ref, err := repo.Reference(refName, true)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("oplog: read metadata branch ref: %w", err)
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("oplog: read metadata branch commit: %w", err)
	}
	rootTree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("oplog: read metadata branch tree: %w", err)
	}

	var oplogTree *object.Tree
	for _, e := range rootTree.Entries {
		if e.Name == oplogDir {
			t, tErr := repo.TreeObject(e.Hash)
			if tErr != nil {
				return nil, fmt.Errorf("oplog: read oplog subtree: %w", tErr)
			}
			oplogTree = t
			break
		}
	}
	if oplogTree == nil {
		return nil, nil
	}

	flat := map[string]object.TreeEntry{}
	if err := checkpoint.FlattenTree(repo, oplogTree, "", flat); err != nil {
		return nil, fmt.Errorf("oplog: flatten oplog subtree: %w", err)
	}

	entries := make([]Entry, 0, len(flat))
	for path, te := range flat {
		if !strings.HasSuffix(path, "/entry.json") {
			continue
		}
		e, err := readEntryBlob(repo, te.Hash)
		if err != nil {
			return nil, fmt.Errorf("oplog: read entry %s: %w", path, err)
		}
		entries = append(entries, e)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Timestamp.After(entries[j].Timestamp) })
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func readEntryBlob(repo *git.Repository, hash plumbing.Hash) (Entry, error) {
	var e Entry
	blob, err := repo.BlobObject(hash)
	if err != nil {
		return e, err
	}
	reader, err := blob.Reader()
	if err != nil {
		return e, err
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return e, err
	}
	return e, nil
}
