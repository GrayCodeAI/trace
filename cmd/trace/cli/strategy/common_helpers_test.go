package strategy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/checkpoint"

	"github.com/go-git/go-git/v6/plumbing/object"
)

// readCheckpointMetadataFull is the original ReadCheckpointMetadata implementation
// preserved for test code that needs the full deserialization behavior (loading
// every field from both the root CheckpointSummary and per-session CommittedMetadata).
//
// Production code uses ReadCheckpointMetadata which streams via json.Decoder
// and uses minimal structs to avoid allocating large unused fields
// (Summary, InitialAttribution, TokenUsage, etc.).
//
//lint:ignore U1000 // Test helper preserved for tests that need full deserialization
func readCheckpointMetadataFull(tree *object.Tree, checkpointPath string) (*CheckpointInfo, error) {
	metadataPath := checkpointPath + "/metadata.json"
	file, err := tree.File(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to find metadata at %s: %w", metadataPath, err)
	}

	content, err := file.Contents()
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	// Try to parse as CheckpointSummary first (new format)
	var summary checkpoint.CheckpointSummary
	if err := json.Unmarshal([]byte(content), &summary); err == nil {
		// If we have sessions array, this is the new format
		if len(summary.Sessions) > 0 {
			info := &CheckpointInfo{
				CheckpointID:     summary.CheckpointID,
				CheckpointsCount: summary.CheckpointsCount,
				FilesTouched:     summary.FilesTouched,
				SessionCount:     len(summary.Sessions),
			}

			// Read all sessions' metadata to populate SessionIDs and get other fields from first session
			var sessionIDs []string
			for i, sessionPaths := range summary.Sessions {
				if sessionPaths.Metadata != "" {
					// SessionFilePaths now contains absolute paths with leading "/"
					// Strip the leading "/" for tree.File() which expects paths without leading slash
					sessionMetadataPath := strings.TrimPrefix(sessionPaths.Metadata, "/")
					if sessionFile, err := tree.File(sessionMetadataPath); err == nil {
						if sessionContent, err := sessionFile.Contents(); err == nil {
							var sessionMetadata checkpoint.CommittedMetadata
							if json.Unmarshal([]byte(sessionContent), &sessionMetadata) == nil {
								sessionIDs = append(sessionIDs, sessionMetadata.SessionID)
								// Use first session for Agent, SessionID, CreatedAt, IsTask, ToolUseID
								if i == 0 {
									info.Agent = sessionMetadata.Agent
									info.SessionID = sessionMetadata.SessionID
									info.CreatedAt = sessionMetadata.CreatedAt
									info.IsTask = sessionMetadata.IsTask
									info.ToolUseID = sessionMetadata.ToolUseID
								}
							}
						}
					}
				}
			}
			info.SessionIDs = sessionIDs

			return info, nil
		}
	}

	// Fall back to parsing as CheckpointInfo (old format or direct info)
	var metadata CheckpointInfo
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	return &metadata, nil
}
