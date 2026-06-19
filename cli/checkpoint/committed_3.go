package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/utils/binary"
)

// getFetchingTree returns a FetchingTree for the metadata branch.
// If a blob fetcher is configured on the store, File() calls on the returned
// tree will automatically fetch missing blobs from the remote.
func (s *GitStore) getFetchingTree(ctx context.Context) (*FetchingTree, error) {
	tree, err := s.getSessionsBranchTree()
	if err != nil {
		return nil, err
	}
	return NewFetchingTree(ctx, tree, s.repo.Storer, s.blobFetcher), nil
}

// getSessionsBranchTree returns the tree object for the trace/checkpoints/v1 branch.
// Falls back to origin/trace/checkpoints/v1 if the local branch doesn't exist.
func (s *GitStore) getSessionsBranchTree() (*object.Tree, error) {
	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	ref, err := s.repo.Reference(refName, true)
	if err != nil {
		// Local branch doesn't exist, try remote-tracking branch
		remoteRefName := plumbing.NewRemoteReferenceName("origin", paths.MetadataBranchName)
		ref, err = s.repo.Reference(remoteRefName, true)
		if err != nil {
			return nil, fmt.Errorf("sessions branch not found: %w", err)
		}
	}

	commit, err := s.repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit object: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get commit tree: %w", err)
	}

	return tree, nil
}

// CreateBlobFromContent creates a blob object from in-memory content.
// Exported for use by strategy package (session_test.go)
func CreateBlobFromContent(repo *git.Repository, content []byte) (plumbing.Hash, error) {
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(int64(len(content)))

	writer, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to get object writer: %w", err)
	}

	_, err = writer.Write(content)
	if err != nil {
		_ = writer.Close()
		return plumbing.ZeroHash, fmt.Errorf("failed to write blob content: %w", err)
	}
	if err := writer.Close(); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to close blob writer: %w", err)
	}

	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store blob object: %w", err)
	}
	return hash, nil
}

// copyMetadataDir copies all files from a directory to the checkpoint path.
// Used to include additional metadata files like task checkpoints, subagent transcripts, etc.
func (s *GitStore) copyMetadataDir(metadataDir, basePath string, entries map[string]object.TreeEntry) error {
	err := filepath.Walk(metadataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip symlinks to prevent reading files outside the metadata directory.
		// A symlink could point to sensitive files (e.g., /etc/passwd) which would
		// then be captured in the checkpoint and stored in git history.
		// NOTE: filepath.Walk uses os.Stat (follows symlinks), so info.Mode() never
		// reports ModeSymlink. We use os.Lstat to check the entry itself.
		// This check MUST come before IsDir() because Walk follows symlinked
		// directories and would recurse into them otherwise.
		linfo, lstatErr := os.Lstat(path)
		if lstatErr != nil {
			return fmt.Errorf("failed to lstat %s: %w", path, lstatErr)
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			return nil
		}

		// Get relative path within metadata dir
		relPath, err := filepath.Rel(metadataDir, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s: %w", path, err)
		}

		// Prevent path traversal via symlinks pointing outside the metadata dir
		if strings.HasPrefix(relPath, "..") {
			return fmt.Errorf("path traversal detected: %s", relPath)
		}

		// Create blob from file with secrets redaction
		blobHash, mode, err := createRedactedBlobFromFile(s.repo, path, relPath)
		if err != nil {
			return fmt.Errorf("failed to create blob for %s: %w", path, err)
		}

		// Store at checkpoint path (use forward slashes for git tree compatibility on Windows)
		fullPath := basePath + filepath.ToSlash(relPath)
		entries[fullPath] = object.TreeEntry{
			Name: fullPath,
			Mode: mode,
			Hash: blobHash,
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk metadata directory: %w", err)
	}
	return nil
}

// createRedactedBlobFromFile reads a file, applies secrets redaction, and creates a git blob.
// JSONL files get JSONL-aware redaction; all other files get plain string redaction.
func createRedactedBlobFromFile(repo *git.Repository, filePath, treePath string) (plumbing.Hash, filemode.FileMode, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return plumbing.ZeroHash, 0, fmt.Errorf("failed to stat file: %w", err)
	}

	mode := filemode.Regular
	if info.Mode()&0o111 != 0 {
		mode = filemode.Executable
	}

	content, err := os.ReadFile(filePath) //nolint:gosec // filePath comes from walking the metadata directory
	if err != nil {
		return plumbing.ZeroHash, 0, fmt.Errorf("failed to read file: %w", err)
	}

	// Skip redaction for binary files — they can't contain text secrets and
	// running string replacement on them would corrupt the data.
	isBin, binErr := binary.IsBinary(bytes.NewReader(content))
	if binErr != nil || isBin {
		hash, err := CreateBlobFromContent(repo, content)
		if err != nil {
			return plumbing.ZeroHash, 0, fmt.Errorf("failed to create blob: %w", err)
		}
		return hash, mode, nil
	}

	if strings.HasSuffix(treePath, ".jsonl") {
		redacted, jsonlErr := redact.JSONLBytes(content)
		if jsonlErr != nil {
			content = redact.Bytes(content)
		} else {
			content = redacted.Bytes()
		}
	} else {
		content = redact.Bytes(content)
	}

	hash, err := CreateBlobFromContent(repo, content)
	if err != nil {
		return plumbing.ZeroHash, 0, fmt.Errorf("failed to create blob: %w", err)
	}
	return hash, mode, nil
}

// GetGitAuthorFromRepo retrieves the git user.name and user.email,
// checking both the repository-local config and the global ~/.gitconfig.
func GetGitAuthorFromRepo(repo *git.Repository) (name, email string) {
	// ConfigScoped merges local + global (local wins), matching git's own resolution.
	// Requires a ConfigLoader plugin to be registered; the hawk binary blank-imports
	// go-git/v6/x/plugin to register the default Auto loader.
	if cfg, err := repo.ConfigScoped(config.GlobalScope); err == nil {
		name = cfg.User.Name
		email = cfg.User.Email
	}

	// If not found in local config, try global config
	if name == "" || email == "" {
		//lint:ignore SA1019 // the v6 is not yet released, revisit once it is.
		globalCfg, err := config.LoadConfig(config.GlobalScope)
		if err == nil {
			if name == "" {
				name = globalCfg.User.Name
			}
			if email == "" {
				email = globalCfg.User.Email
			}
		}
	}

	// Provide sensible defaults if git user is not configured
	if name == "" {
		name = "Unknown"
	}
	if email == "" {
		email = "unknown@local"
	}

	return name, email
}

// CreateCommit creates a git commit object with the given tree, parent, message, and author.
// If parentHash is ZeroHash, the commit is created without a parent (orphan commit).
func CreateCommit(ctx context.Context, repo *git.Repository, treeHash, parentHash plumbing.Hash, message, authorName, authorEmail string) (plumbing.Hash, error) {
	now := time.Now()
	sig := object.Signature{
		Name:  authorName,
		Email: authorEmail,
		When:  now,
	}

	commit := &object.Commit{
		TreeHash:  treeHash,
		Author:    sig,
		Committer: sig,
		Message:   message,
	}

	if parentHash != plumbing.ZeroHash {
		commit.ParentHashes = []plumbing.Hash{parentHash}
	}

	SignCommitBestEffort(ctx, commit)

	obj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode commit: %w", err)
	}

	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store commit: %w", err)
	}

	return hash, nil
}

// SignCommitBestEffort signs the commit using an on-demand object signer.
// If signing is disabled, no signer can be created, or signing fails, the commit
// is left unsigned and the error is logged.
func SignCommitBestEffort(ctx context.Context, commit *object.Commit) {
	if !settings.IsSignCheckpointCommitsEnabled(ctx) {
		return
	}

	signer, ok := objectSignerLoader(ctx)
	if !ok {
		return
	}

	if signer == nil {
		return
	}

	encoded := &plumbing.MemoryObject{}
	var err error
	if err = commit.EncodeWithoutSignature(encoded); err != nil {
		logging.Warn(ctx, "failed to encode commit for signing", slog.String("error", err.Error()))
		return
	}

	r, err := encoded.Reader()
	if err != nil {
		logging.Warn(ctx, "failed to read encoded commit", slog.String("error", err.Error()))
		return
	}
	defer r.Close()

	sig, err := signer.Sign(r)
	if err != nil {
		logging.Warn(ctx, "failed to sign commit", slog.String("error", err.Error()))
		return
	}

	commit.Signature = string(sig)
}

// readTranscriptFromTree reads a transcript from a git tree, handling both chunked and non-chunked formats.
// It checks for chunk files first (.001, .002, etc.), then falls back to the base file.
// The agentType is used for reassembling chunks in the correct format.
func readTranscriptFromTree(ctx context.Context, tree *FetchingTree, agentType types.AgentType) ([]byte, error) {
	// Collect all transcript-related files
	var chunkFiles []string
	var hasBaseFile bool

	for _, entry := range tree.RawEntries() {
		if entry.Name == paths.TranscriptFileName || entry.Name == paths.TranscriptFileNameLegacy {
			hasBaseFile = true
		}
		// Check for chunk files (full.jsonl.001, full.jsonl.002, etc.)
		if strings.HasPrefix(entry.Name, paths.TranscriptFileName+".") {
			idx := agent.ParseChunkIndex(entry.Name, paths.TranscriptFileName)
			if idx > 0 {
				chunkFiles = append(chunkFiles, entry.Name)
			}
		}
	}

	// If we have chunk files, read and reassemble them
	if len(chunkFiles) > 0 {
		// Sort chunk files by index
		chunkFiles = agent.SortChunkFiles(chunkFiles, paths.TranscriptFileName)

		// Check if base file should be included as chunk 0.
		// NOTE: This assumes the chunking convention where the unsuffixed file
		// (full.jsonl) is chunk 0, and numbered files (.001, .002) are chunks 1+.
		if hasBaseFile {
			chunkFiles = append([]string{paths.TranscriptFileName}, chunkFiles...)
		}

		var chunks [][]byte
		for _, chunkFile := range chunkFiles {
			file, err := tree.File(chunkFile)
			if err != nil {
				logging.Warn(
					ctx, "failed to read transcript chunk file from tree",
					slog.String("chunk_file", chunkFile),
					slog.String("error", err.Error()),
				)
				continue
			}
			content, err := file.Contents()
			if err != nil {
				logging.Warn(
					ctx, "failed to read transcript chunk contents",
					slog.String("chunk_file", chunkFile),
					slog.String("error", err.Error()),
				)
				continue
			}
			chunks = append(chunks, []byte(content))
		}

		if len(chunks) > 0 {
			result, err := agent.ReassembleTranscript(chunks, agentType)
			if err != nil {
				return nil, fmt.Errorf("failed to reassemble transcript: %w", err)
			}
			return result, nil
		}
	}

	// Fall back to reading base file (non-chunked or backwards compatibility)
	if file, err := tree.File(paths.TranscriptFileName); err == nil {
		if content, err := file.Contents(); err == nil {
			return []byte(content), nil
		}
	}

	// Try legacy filename
	if file, err := tree.File(paths.TranscriptFileNameLegacy); err == nil {
		if content, err := file.Contents(); err == nil {
			return []byte(content), nil
		}
	}

	return nil, nil
}

// Author contains author information for a checkpoint.
type Author struct {
	Name  string
	Email string
}

// GetCheckpointAuthor retrieves the author of a checkpoint from the trace/checkpoints/v1 commit history.
// Finds the commit whose subject matches "Checkpoint: <id>" and returns its author.
// Returns empty Author if the checkpoint is not found or the sessions branch doesn't exist.
func (s *GitStore) GetCheckpointAuthor(ctx context.Context, checkpointID id.CheckpointID) (Author, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	if err := ctx.Err(); err != nil {
		return Author{}, err //nolint:wrapcheck // Propagating context cancellation
	}

	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	ref, err := s.repo.Reference(refName, true)
	if err != nil {
		return Author{}, nil
	}

	// Search for the commit whose subject matches "Checkpoint: <id>"
	targetSubject := "Checkpoint: " + checkpointID.String()

	iter, err := s.repo.Log(&git.LogOptions{
		From:  ref.Hash(),
		Order: git.LogOrderCommitterTime,
	})
	if err != nil {
		return Author{}, nil
	}
	defer iter.Close()

	var author Author
	err = iter.ForEach(func(c *object.Commit) error {
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // Propagating context cancellation
		}
		subject := strings.SplitN(c.Message, "\n", 2)[0]
		if subject == targetSubject {
			author = Author{
				Name:  c.Author.Name,
				Email: c.Author.Email,
			}
			return errStopIteration
		}
		return nil
	})

	if err != nil && !errors.Is(err, errStopIteration) {
		return Author{}, nil
	}

	return author, nil
}
