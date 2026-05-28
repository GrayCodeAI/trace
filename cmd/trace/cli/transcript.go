package cli

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	agentpkg "github.com/GrayCodeAI/trace/cmd/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/transcript"
)

// compressGzipThreshold is the minimum size in bytes before a transcript
// is gzip-compressed on disk. Small transcripts stay uncompressed for
// faster reads and backward compatibility.
const compressGzipThreshold = 32 * 1024 // 32 KiB

// resolveTranscriptPath determines the correct file path for an agent's session transcript.
// Computes the path dynamically from the current repo location for cross-machine portability.
func resolveTranscriptPath(ctx context.Context, sessionID string, agent agentpkg.Agent) (string, error) {
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get worktree root: %w", err)
	}

	sessionDir, err := agent.GetSessionDir(repoRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get agent session directory: %w", err)
	}
	return agent.ResolveSessionFile(sessionDir, sessionID), nil
}

// searchTranscriptInProjectDirs searches for a session transcript across an agent's
// project directories that could plausibly belong to the current repository.
// Agents like Claude Code and Gemini CLI derive the project directory from the cwd,
// so the transcript may be stored under a different project directory if the session
// was started from a different working directory.
//
// The search is scoped to the agent's base directory (e.g., ~/.claude/projects) and only
// walks immediate subdirectories (plus one extra level for agents like Gemini that nest
// chats under <project>/chats/).
// Only agents implementing SessionBaseDirProvider support this fallback search.
func searchTranscriptInProjectDirs(sessionID string, ag agentpkg.Agent) (string, error) {
	provider, ok := agentpkg.AsSessionBaseDirProvider(ag)
	if !ok {
		return "", fmt.Errorf("fallback transcript search not supported for agent %q", ag.Name())
	}
	baseDir, err := provider.GetSessionBaseDir()
	if err != nil {
		return "", fmt.Errorf("failed to get base directory: %w", err)
	}

	// Walk subdirectories with a max depth of 3 (baseDir/project/subdir/file)
	// to avoid scanning unrelated project trees.
	const maxExtraDepth = 3

	var found string
	walkErr := filepath.WalkDir(baseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip inaccessible dirs
		}
		if !d.IsDir() {
			return nil
		}
		// Limit walk depth using relative path from base
		rel, relErr := filepath.Rel(baseDir, path)
		if relErr != nil {
			return filepath.SkipDir
		}
		depth := strings.Count(rel, string(filepath.Separator))
		if depth > maxExtraDepth {
			return filepath.SkipDir
		}
		candidate := ag.ResolveSessionFile(path, sessionID)
		if _, statErr := os.Stat(candidate); statErr == nil {
			found = candidate
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("failed to search project directories: %w", walkErr)
	}
	if found != "" {
		return found, nil
	}
	return "", errors.New("transcript not found in any project directory")
}

// AgentTranscriptPath returns the path to a subagent's transcript file.
// Subagent transcripts are stored as agent-{agentId}.jsonl in the same directory
// as the main transcript.
func AgentTranscriptPath(transcriptDir, agentID string) string {
	return filepath.Join(transcriptDir, fmt.Sprintf("agent-%s.jsonl", agentID))
}

// toolResultBlock represents a tool_result in a user message
type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
}

// userMessageWithToolResults represents a user message that may contain tool results
type userMessageWithToolResults struct {
	Content []toolResultBlock `json:"content"`
}

// FindCheckpointUUID finds the UUID of the message containing the tool_result
// for the given tool_use_id. This is used to find the checkpoint point for
// transcript truncation when rewinding to a task.
// Returns the UUID and true if found, empty string and false otherwise.
func FindCheckpointUUID(lines []transcriptLine, toolUseID string) (string, bool) {
	for _, line := range lines {
		if line.Type != transcript.TypeUser {
			continue
		}

		var msg userMessageWithToolResults
		if err := json.Unmarshal(line.Message, &msg); err != nil {
			continue
		}

		for _, block := range msg.Content {
			if block.Type == "tool_result" && block.ToolUseID == toolUseID {
				return line.UUID, true
			}
		}
	}
	return "", false
}

// TruncateTranscriptAtUUID returns transcript lines up to and including the
// line with the given UUID. If the UUID is not found or is empty, returns
// the trace transcript.
//
//nolint:revive // Exported for testing purposes
func TruncateTranscriptAtUUID(lines []transcriptLine, uuid string) []transcriptLine {
	if uuid == "" {
		return lines
	}

	for i, line := range lines {
		if line.UUID == uuid {
			return lines[:i+1]
		}
	}

	// UUID not found, return full transcript
	return lines
}

// writeTranscript writes transcript lines to a file in JSONL format.
// When the serialized content exceeds compressGzipThreshold bytes the file
// is gzip-compressed (detected transparently by readTranscriptBytes).
func writeTranscript(path string, lines []transcriptLine) error {
	var buf bytes.Buffer
	for _, line := range lines {
		data, err := json.Marshal(line)
		if err != nil {
			return fmt.Errorf("failed to marshal line: %w", err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}

	raw := buf.Bytes()
	if len(raw) >= compressGzipThreshold {
		if err := os.WriteFile(path+".gz", gzipCompress(raw), 0o644); err != nil { //nolint:gosec // Writing to controlled git metadata path
			return fmt.Errorf("writing compressed transcript: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil { //nolint:gosec // Writing to controlled git metadata path
		return fmt.Errorf("writing transcript: %w", err)
	}
	return nil
}

// gzipCompress returns the gzip-compressed form of data.
func gzipCompress(data []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(data) //nolint:errcheck // In-memory buffer write cannot fail
	_ = w.Close()
	return buf.Bytes()
}

// gzipDecompress returns the decompressed form of gzip-compressed data.
func gzipDecompress(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = r.Close() }()
	data, err = io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("decompressing transcript: %w", err)
	}
	return data, nil
}

// readTranscriptBytes reads a transcript file, transparently decompressing
// gzip if the file has a .gz extension. Returns (data, exists, error).
func readTranscriptBytes(path string) ([]byte, bool, error) {
	// Try compressed path first.
	gzPath := path + ".gz"
	data, err := os.ReadFile(gzPath) //nolint:gosec // Reading from controlled transcript path
	if err == nil {
		decompressed, dErr := gzipDecompress(data)
		if dErr != nil {
			return nil, true, fmt.Errorf("failed to decompress transcript: %w", dErr)
		}
		return decompressed, true, nil
	}

	// Fall back to uncompressed path.
	data, err = os.ReadFile(path) //nolint:gosec // Reading from controlled transcript path
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to read transcript: %w", err)
	}
	return data, true, nil
}

// TranscriptPosition contains the position information for a transcript file.
type TranscriptPosition struct {
	LastUUID  string // Last non-empty UUID (from user/assistant messages)
	LineCount int    // Total number of lines
}

// GetTranscriptPosition reads a transcript file and returns the last UUID and line count.
// Returns empty position if file doesn't exist or is empty.
// Only considers UUIDs from actual messages (user/assistant), not summary rows which use leafUuid.
// Transparently handles gzip-compressed transcripts (.gz files).
func GetTranscriptPosition(path string) (TranscriptPosition, error) {
	if path == "" {
		return TranscriptPosition{}, nil
	}

	data, exists, err := readTranscriptBytes(path)
	if err != nil {
		return TranscriptPosition{}, err
	}
	if !exists {
		return TranscriptPosition{}, nil
	}

	var pos TranscriptPosition
	reader := bufio.NewReader(bytes.NewReader(data))

	for {
		lineBytes, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return TranscriptPosition{}, fmt.Errorf("failed to read transcript: %w", err)
		}

		if len(lineBytes) == 0 {
			if err == io.EOF {
				break
			}
			continue
		}

		pos.LineCount++

		// Parse line to extract UUID (only from user/assistant messages, not summaries)
		var line transcriptLine
		if err := json.Unmarshal(lineBytes, &line); err == nil {
			if line.UUID != "" {
				pos.LastUUID = line.UUID
			}
		}

		if err == io.EOF {
			break
		}
	}

	return pos, nil
}
