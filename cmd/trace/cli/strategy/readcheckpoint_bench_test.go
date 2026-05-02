package strategy

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/benchutil"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// BenchmarkReadCheckpointMetadata measures the time to read and decode a single
// checkpoint's metadata from the trace/checkpoints/v1 branch.
// Tests both the streaming lite decoder and varying numbers of sessions.
func BenchmarkReadCheckpointMetadata(b *testing.B) {
	b.Run("1Checkpoint", benchReadCheckpointMetadata(1))
	b.Run("10Checkpoints", benchReadCheckpointMetadata(10))
	b.Run("50Checkpoints", benchReadCheckpointMetadata(50))
	b.Run("200Checkpoints", benchReadCheckpointMetadata(200))
}

func benchReadCheckpointMetadata(checkpointCount int) func(*testing.B) {
	return func(b *testing.B) {
		repo := benchutil.NewBenchRepo(b, benchutil.RepoOpts{FileCount: 10})
		repo.SeedMetadataBranch(b, checkpointCount)

		// Get the metadata branch tree and find a checkpoint path to read
		tree, cpPath := getMetadataBranchCheckpoint(b, repo.Repo)

		b.ResetTimer()
		b.ReportMetric(float64(checkpointCount), "total_checkpoints")

		for b.Loop() {
			info, err := ReadCheckpointMetadata(tree, cpPath)
			if err != nil {
				b.Fatalf("ReadCheckpointMetadata: %v", err)
			}
			if info.SessionID == "" {
				b.Fatal("expected non-empty SessionID")
			}
		}
	}
}

// BenchmarkListCheckpoints measures the full ListCheckpoints path which iterates
// all sharded checkpoints and decodes each one using the lite streaming decoder.
func BenchmarkListCheckpoints(b *testing.B) {
	b.Run("1Checkpoint", benchListCheckpoints(1))
	b.Run("10Checkpoints", benchListCheckpoints(10))
	b.Run("50Checkpoints", benchListCheckpoints(50))
	b.Run("200Checkpoints", benchListCheckpoints(200))
}

func benchListCheckpoints(checkpointCount int) func(*testing.B) {
	return func(b *testing.B) {
		repo := benchutil.NewBenchRepo(b, benchutil.RepoOpts{FileCount: 10})
		repo.SeedMetadataBranch(b, checkpointCount)

		b.Chdir(repo.Dir)
		paths.ClearWorktreeRootCache()

		b.ResetTimer()
		b.ReportMetric(float64(checkpointCount), "total_checkpoints")

		for b.Loop() {
			checkpoints, err := ListCheckpoints(context.Background())
			if err != nil {
				b.Fatalf("ListCheckpoints: %v", err)
			}
			if len(checkpoints) != checkpointCount {
				b.Fatalf("expected %d checkpoints, got %d", checkpointCount, len(checkpoints))
			}
		}
	}
}

// BenchmarkDecodeSummaryLiteFromTree measures just the root metadata.json
// decoding using the lite streaming decoder.
func BenchmarkDecodeSummaryLiteFromTree(b *testing.B) {
	repo := benchutil.NewBenchRepo(b, benchutil.RepoOpts{FileCount: 10})
	repo.SeedMetadataBranch(b, 5)

	cpTree := getCheckpointTree(b, repo.Repo)

	b.ResetTimer()
	for b.Loop() {
		summary, ok := decodeSummaryLiteFromTree(cpTree)
		if !ok {
			b.Fatal("decodeSummaryLiteFromTree returned false")
		}
		if summary.CheckpointID == "" {
			b.Fatal("expected non-empty CheckpointID")
		}
	}
}

// BenchmarkDecodeSessionMetadataLite measures decoding a single session
// metadata.json using the lite streaming decoder.
func BenchmarkDecodeSessionMetadataLite(b *testing.B) {
	repo := benchutil.NewBenchRepo(b, benchutil.RepoOpts{FileCount: 10})
	repo.SeedMetadataBranch(b, 5)

	// Get tree and find a session metadata path
	tree := getMetadataBranchTree(b, repo.Repo)
	sessionPath := findSessionMetadataPath(b, repo.Repo, tree)

	b.ResetTimer()
	for b.Loop() {
		meta, err := decodeSessionMetadataLite(tree, sessionPath)
		if err != nil {
			b.Fatalf("decodeSessionMetadataLite: %v", err)
		}
		if meta.SessionID == "" {
			b.Fatal("expected non-empty SessionID")
		}
	}
}

// --- helpers ---

// getMetadataBranchTree returns the root tree of trace/checkpoints/v1.
func getMetadataBranchTree(b *testing.B, repo *gogit.Repository) *object.Tree {
	b.Helper()
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		b.Fatalf("get metadata branch ref: %v", err)
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		b.Fatalf("get metadata branch commit: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		b.Fatalf("get metadata branch tree: %v", err)
	}
	return tree
}

// getMetadataBranchCheckpoint returns the metadata branch tree and a checkpoint path
// (e.g., "ab/cdef123456") for benchmarking ReadCheckpointMetadata.
func getMetadataBranchCheckpoint(b *testing.B, repo *gogit.Repository) (*object.Tree, string) {
	b.Helper()
	tree := getMetadataBranchTree(b, repo)

	// Find first valid checkpoint path: <2-char-bucket>/<remaining>
	for _, bucketEntry := range tree.Entries {
		if bucketEntry.Mode != filemode.Dir || len(bucketEntry.Name) != 2 {
			continue
		}
		bucketTree, err := repo.TreeObject(bucketEntry.Hash)
		if err != nil {
			continue
		}
		for _, cpEntry := range bucketTree.Entries {
			if cpEntry.Mode != filemode.Dir {
				continue
			}
			return tree, fmt.Sprintf("%s/%s", bucketEntry.Name, cpEntry.Name)
		}
	}
	b.Fatal("no checkpoint found on metadata branch")
	return nil, ""
}

// getCheckpointTree returns the tree object for a single checkpoint directory.
func getCheckpointTree(b *testing.B, repo *gogit.Repository) *object.Tree {
	b.Helper()
	tree := getMetadataBranchTree(b, repo)

	for _, bucketEntry := range tree.Entries {
		if bucketEntry.Mode != filemode.Dir || len(bucketEntry.Name) != 2 {
			continue
		}
		bucketTree, err := repo.TreeObject(bucketEntry.Hash)
		if err != nil {
			continue
		}
		for _, cpEntry := range bucketTree.Entries {
			if cpEntry.Mode != filemode.Dir {
				continue
			}
			cpTree, err := repo.TreeObject(cpEntry.Hash)
			if err != nil {
				continue
			}
			return cpTree
		}
	}
	b.Fatal("no checkpoint tree found")
	return nil
}

// findSessionMetadataPath finds the path to a session metadata.json on the
// metadata branch tree (stripping leading "/" from SessionFilePaths).
func findSessionMetadataPath(b *testing.B, repo *gogit.Repository, tree *object.Tree) string {
	b.Helper()

	for _, bucketEntry := range tree.Entries {
		if bucketEntry.Mode != filemode.Dir || len(bucketEntry.Name) != 2 {
			continue
		}
		bucketTree, err := repo.TreeObject(bucketEntry.Hash)
		if err != nil {
			continue
		}
		for _, cpEntry := range bucketTree.Entries {
			if cpEntry.Mode != filemode.Dir {
				continue
			}
			cpTree, err := repo.TreeObject(cpEntry.Hash)
			if err != nil {
				continue
			}
			// Look for metadata.json in the checkpoint tree
			summary, ok := decodeSummaryLiteFromTree(cpTree)
			if !ok || len(summary.Sessions) == 0 {
				continue
			}
			path := strings.TrimPrefix(summary.Sessions[0].Metadata, "/")
			if path != "" {
				return path
			}
		}
	}
	b.Fatal("no session metadata path found")
	return ""
}
