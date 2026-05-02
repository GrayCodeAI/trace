package checkpoint

import (
	"github.com/go-git/go-git/v6"
)

// Compile-time check that GitStore implements the Store interface.
var _ Store = (*GitStore)(nil)

// GitStore provides operations for both temporary and committed checkpoint storage.
// It implements the Store interface by wrapping a git repository.
type GitStore struct {
	repo        *git.Repository
	blobFetcher BlobFetchFunc
}

// NewGitStore creates a new checkpoint store backed by the given git repository.
func NewGitStore(repo *git.Repository) *GitStore {
	return &GitStore{repo: repo}
}

// SetBlobFetcher configures the store to automatically fetch missing blobs
// on demand when reading from metadata trees. This is used after treeless
// fetches where tree objects are local but blob objects are not.
func (s *GitStore) SetBlobFetcher(f BlobFetchFunc) {
	s.blobFetcher = f
}

// Repository returns the underlying git repository.
// This is useful for strategies that need direct repository access.
func (s *GitStore) Repository() *git.Repository {
	return s.repo
}
