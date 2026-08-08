package trail

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

// idLength is the number of random bytes backing a trail ID (12 hex chars).
const idLength = 6

// idRegex validates the format: exactly 12 lowercase hex characters.
var idRegex = regexp.MustCompile(`^[0-9a-f]{12}$`)

// GenerateID creates a new random trail ID: 12 lowercase hex characters.
func GenerateID() (ID, error) {
	bytes := make([]byte, idLength)
	if _, err := rand.Read(bytes); err != nil {
		return EmptyID, fmt.Errorf("failed to generate random trail ID: %w", err)
	}
	return ID(hex.EncodeToString(bytes)), nil
}

// ValidateID validates a trail ID string.
func ValidateID(s string) error {
	if !idRegex.MatchString(s) {
		return fmt.Errorf("invalid trail ID %q: must be 12 lowercase hex characters", s)
	}
	return nil
}

// Path returns the sharded storage path for this trail ID.
// Uses first 2 characters as shard (256 buckets), remaining as folder name.
// Example: "a3b2c4d5e6f7" -> "a3/b2c4d5e6f7"
func (id ID) Path() string {
	if len(id) < 3 {
		return string(id)
	}
	return string(id[:2]) + "/" + string(id[2:])
}

// ShardParts returns the shard prefix and suffix separately.
// Example: "a3b2c4d5e6f7" -> ("a3", "b2c4d5e6f7")
func (id ID) ShardParts() (shard, suffix string) {
	if len(id) < 3 {
		return string(id), ""
	}
	return string(id[:2]), string(id[2:])
}

// Discussion holds the threaded comment discussion on a trail.
type Discussion struct {
	Comments []Comment `json:"comments"`
}

// Comment represents a single comment on a trail.
type Comment struct {
	ID         string         `json:"id"`
	Author     string         `json:"author"`
	Body       string         `json:"body"`
	CreatedAt  time.Time      `json:"created_at"`
	Resolved   bool           `json:"resolved"`
	ResolvedBy *string        `json:"resolved_by"`
	ResolvedAt *time.Time     `json:"resolved_at"`
	Replies    []CommentReply `json:"replies,omitempty"`
}

// CommentReply represents a reply to a comment.
type CommentReply struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// CheckpointRef links a checkpoint to a trail.
type CheckpointRef struct {
	CheckpointID string    `json:"checkpoint_id"`
	CommitSHA    string    `json:"commit_sha"`
	CreatedAt    time.Time `json:"created_at"`
	Summary      *string   `json:"summary"`
}

// Checkpoints holds the list of checkpoint references for a trail.
type Checkpoints struct {
	Checkpoints []CheckpointRef `json:"checkpoints"`
}
