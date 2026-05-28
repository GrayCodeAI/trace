package checkpoint

import (
	"sync"

	"github.com/go-git/go-git/v6"
)

// Compile-time check that GitStore implements the Store interface.
var _ Store = (*GitStore)(nil)

// StorerMu serializes all in-process access to git storers. go-git's
// filesystem storer is not safe for concurrent read+write even across
// separate Repository instances that share the same .git directory.
// The shadow branch flock handles cross-process serialization; this
// mutex handles in-process (cross-goroutine) serialization.
// Exported so the strategy package can also acquire it around OpenRepository
// and other storer access that happens outside GitStore methods.
var StorerMu sync.Mutex

// GitStore provides operations for both temporary and committed checkpoint storage.
// It implements the Store interface by wrapping a git repository.
type GitStore struct {
	repo        *git.Repository
	repoPath    string // root path for opening fresh repo instances
	blobFetcher BlobFetchFunc
}

// NewGitStore creates a new checkpoint store backed by the given git repository.
func NewGitStore(repo *git.Repository) *GitStore {
	wt, err := repo.Worktree()
	var repoPath string
	if err == nil {
		repoPath = wt.Filesystem.Root()
	}
	return &GitStore{repo: repo, repoPath: repoPath}
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

// openFreshRepo opens a new git.Repository instance to avoid storer contention
// with concurrent writers. go-git's storer is not fully thread-safe for
// concurrent write+read on the same instance.
func (s *GitStore) openFreshRepo() (*git.Repository, error) {
	if s.repoPath == "" {
		return s.repo, nil
	}
	return git.PlainOpen(s.repoPath)
}

// withStorerLock acquires the process-wide storer mutex for the duration of fn.
// This serializes all in-process git storer access to prevent races between
// concurrent goroutines reading/writing the same .git directory.
func withStorerLock(fn func() error) error {
	StorerMu.Lock()
	defer StorerMu.Unlock()
	return fn()
}
