package paths

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"time"
)

// GetLastTimestampFromFile reads the last non-empty line from a JSONL file
// and extracts the timestamp field. Returns zero time if file doesn't exist
// or no valid timestamp is found.
func GetLastTimestampFromFile(path string) time.Time {
	file, err := os.Open(path) //nolint:gosec // path is from controlled session directory
	if err != nil {
		return time.Time{}
	}
	defer file.Close()

	var lastLine string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			lastLine = line
		}
	}

	return ParseTimestampFromJSONL(lastLine)
}

// GetLastTimestampFromBytes extracts the timestamp from the last non-empty line
// of JSONL content. Returns zero time if not found.
func GetLastTimestampFromBytes(data []byte) time.Time {
	var lastLine string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			lastLine = line
		}
	}

	return ParseTimestampFromJSONL(lastLine)
}

// ParseTimestampFromJSONL extracts the timestamp from a JSONL line.
// Returns zero time if the line is empty or doesn't contain a valid timestamp.
func ParseTimestampFromJSONL(line string) time.Time {
	if line == "" {
		return time.Time{}
	}

	var entry struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return time.Time{}
	}

	t, err := time.Parse(time.RFC3339, entry.Timestamp)
	if err != nil {
		return time.Time{}
	}
	return t
}
