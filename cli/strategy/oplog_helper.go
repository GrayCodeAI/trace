package strategy

import (
	"context"

	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/oplog"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// RecordOplogEntry appends an operation-log entry for a state-mutating ref
// change (rewind, reset --hard, fork, checkpoint cleanup), so 'trace undo'
// can revert it later. Exported so both this package's internal call sites
// and the cli package (rewind_2.go's performGitResetHard caller, fork_cmd.go)
// can share one implementation.
//
// Callers should treat a non-nil error as worth logging, not as a reason to
// fail the caller's own operation: by the time this is called the ref
// mutation itself has already succeeded, and an audit-log write failure
// shouldn't unwind a successful rewind/reset/fork/cleanup.
func RecordOplogEntry(ctx context.Context, repo *git.Repository, op oplog.Op, ref string, before, after plumbing.Hash, checkpointID string) error {
	entryID, err := id.Generate()
	if err != nil {
		return err
	}
	authorName, authorEmail := GetGitAuthorFromRepo(repo)
	entry := oplog.Entry{
		ID:           entryID.String(),
		Op:           op,
		Ref:          ref,
		BeforeHash:   before.String(),
		AfterHash:    after.String(),
		CheckpointID: checkpointID,
	}
	return oplog.Append(ctx, repo, entry, authorName, authorEmail)
}
