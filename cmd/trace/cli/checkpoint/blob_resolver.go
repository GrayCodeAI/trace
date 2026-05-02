package checkpoint

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/storer"
)

// TranscriptBlobRef identifies a blob within a checkpoint tree on the metadata branch.
// It captures the blob hash from the tree entry without requiring the blob itself to be local.
type TranscriptBlobRef struct {
	// SessionIndex is the 0-based session index within the checkpoint.
	SessionIndex int

	// Hash is the blob's SHA-1 hash from the tree entry.
	Hash plumbing.Hash

	// Path is the blob's path relative to the checkpoint directory,
	// e.g. "0/full.jsonl" or "0/full.jsonl.001".
	Path string
}

// BlobResolver checks blob existence and reads blobs from go-git's local
// object store (loose objects + packfiles). It performs no remote operations.
type BlobResolver struct {
	storer storer.EncodedObjectStorer
}

// NewBlobResolver creates a BlobResolver backed by the given object store.
func NewBlobResolver(s storer.EncodedObjectStorer) *BlobResolver {
	return &BlobResolver{storer: s}
}

// HasBlob returns true if the blob exists in the local object store.
// Checks both loose objects and packfile indices without reading blob content.
func (r *BlobResolver) HasBlob(hash plumbing.Hash) bool {
	return r.storer.HasEncodedObject(hash) == nil
}

// ReadBlob reads a blob's content from the local object store.
// Returns plumbing.ErrObjectNotFound if the blob is not present locally.
func (r *BlobResolver) ReadBlob(hash plumbing.Hash) ([]byte, error) {
	obj, err := r.storer.EncodedObject(plumbing.BlobObject, hash)
	if err != nil {
		return nil, err //nolint:wrapcheck // Propagating plumbing.ErrObjectNotFound
	}

	reader, err := obj.Reader()
	if err != nil {
		return nil, fmt.Errorf("blob reader %s: %w", hash, err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", hash, err)
	}
	return data, nil
}

// CollectTranscriptBlobHashes walks the metadata branch tree for a checkpoint
// and returns blob hashes for all transcript files (full.jsonl and chunks)
// across all sessions. Only reads tree objects — works after a treeless fetch
// where blobs have not been downloaded.
//
// The function navigates the sharded checkpoint directory structure:
//
//	<id[:2]>/<id[2:]>/
//	├── 0/
//	│   ├── full.jsonl          ← collected
//	│   ├── full.jsonl.001      ← collected (chunk)
//	│   └── metadata.json
//	├── 1/
//	│   └── full.jsonl          ← collected
//	└── metadata.json
func CollectTranscriptBlobHashes(tree *object.Tree, checkpointID id.CheckpointID) ([]TranscriptBlobRef, error) {
	checkpointTree, err := tree.Tree(checkpointID.Path())
	if err != nil {
		return nil, fmt.Errorf("checkpoint tree %s: %w", checkpointID.Path(), err)
	}

	var refs []TranscriptBlobRef

	// Enumerate session subdirectories (0, 1, 2, ...)
	for i := 0; ; i++ {
		sessionDir := strconv.Itoa(i)
		sessionTree, treeErr := checkpointTree.Tree(sessionDir)
		if treeErr != nil {
			break // no more sessions
		}

		// Collect transcript blob hashes from tree entries.
		// tree.Entries contains the direct children — no blob reads needed.
		for _, entry := range sessionTree.Entries {
			if entry.Name == paths.TranscriptFileName || entry.Name == paths.TranscriptFileNameLegacy {
				refs = append(refs, TranscriptBlobRef{
					SessionIndex: i,
					Hash:         entry.Hash,
					Path:         sessionDir + "/" + entry.Name,
				})
			}
			// Check for chunk files (full.jsonl.001, full.jsonl.002, etc.)
			if strings.HasPrefix(entry.Name, paths.TranscriptFileName+".") {
				idx := agent.ParseChunkIndex(entry.Name, paths.TranscriptFileName)
				if idx > 0 {
					refs = append(refs, TranscriptBlobRef{
						SessionIndex: i,
						Hash:         entry.Hash,
						Path:         sessionDir + "/" + entry.Name,
					})
				}
			}
		}
	}

	return refs, nil //nolint:nilerr // treeErr from session enumeration loop is used to break, not propagated
}
