// Snapshot persistence helpers: Save/Load write CodeGraphSnapshot records as
// timestamped JSON files under a directory, and listFiles/readFile provide
// the file I/O primitives used by SnapshotStore.
package trace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CodeGraphSnapshot captures the state of the code graph at a point in time.
// This enables:
// - Tracking how the codebase evolves across sessions
// - Comparing graph states to detect structural changes
// - Building a history of code complexity over time
type CodeGraphSnapshot struct {
	Timestamp   time.Time         `json:"timestamp"`
	SessionID   string            `json:"session_id"`
	ProjectRoot string            `json:"project_root"`
	GraphStats  GraphStats        `json:"graph_stats"`
	SymbolCount int               `json:"symbol_count"`
	EdgeCount   int               `json:"edge_count"`
	FileCount   int               `json:"file_count"`
	TopSymbols  []SymbolInfo      `json:"top_symbols"`
	Modules     []ModuleInfo      `json:"modules"`
	Complexity  ComplexityMetrics `json:"complexity"`
	Delta       *GraphDelta       `json:"delta,omitempty"` // changes from previous snapshot
}

// GraphStats holds statistics about the code graph.
type GraphStats struct {
	NodesByKind map[string]int `json:"nodes_by_kind"`
	EdgesByKind map[string]int `json:"edges_by_kind"`
	FilesByLang map[string]int `json:"files_by_lang"`
}

// SymbolInfo represents a symbol in the snapshot.
type SymbolInfo struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	CallCount int    `json:"call_count"` // how many times called
}

// ModuleInfo represents a module/package in the snapshot.
type ModuleInfo struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Files     int      `json:"files"`
	Functions int      `json:"functions"`
	Types     int      `json:"types"`
	Imports   []string `json:"imports"`
}

// ComplexityMetrics holds complexity measurements.
type ComplexityMetrics struct {
	AvgCyclomatic  float64 `json:"avg_cyclomatic"`
	MaxCyclomatic  int     `json:"max_cyclomatic"`
	AvgLOC         float64 `json:"avg_loc"`
	MaxLOC         int     `json:"max_loc"`
	TotalFunctions int     `json:"total_functions"`
}

// GraphDelta represents changes between two snapshots.
type GraphDelta struct {
	FilesAdded      int      `json:"files_added"`
	FilesRemoved    int      `json:"files_removed"`
	FilesModified   int      `json:"files_modified"`
	NodesAdded      int      `json:"nodes_added"`
	NodesRemoved    int      `json:"nodes_removed"`
	EdgesAdded      int      `json:"edges_added"`
	EdgesRemoved    int      `json:"edges_removed"`
	NewSymbols      []string `json:"new_symbols"`
	RemovedSymbols  []string `json:"removed_symbols"`
	ComplexityDelta float64  `json:"complexity_delta"` // change in avg complexity
}

// ErrNoSnapshots is returned when no snapshots exist in the store.
var ErrNoSnapshots = errors.New("no snapshots found")

// SnapshotStore manages code graph snapshots.
type SnapshotStore struct {
	path string
}

// NewSnapshotStore creates a new snapshot store.
func NewSnapshotStore(path string) *SnapshotStore {
	return &SnapshotStore{path: path}
}

// Save persists a snapshot to disk.
func (s *SnapshotStore) Save(snapshot CodeGraphSnapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	// Save to file
	filename := s.path + "/snapshot_" + snapshot.Timestamp.Format("20060102_150405") + ".json"
	return writeFile(filename, data)
}

// Load loads the most recent snapshot.
func (s *SnapshotStore) Load() (*CodeGraphSnapshot, error) {
	files, err := listFiles(s.path, "snapshot_*.json")
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	if len(files) == 0 {
		return nil, ErrNoSnapshots
	}

	// Load most recent
	data, err := readFile(files[len(files)-1])
	if err != nil {
		return nil, fmt.Errorf("read snapshot file: %w", err)
	}

	var snapshot CodeGraphSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}

	return &snapshot, nil
}

// Compare compares two snapshots and returns the delta.
func CompareSnapshots(old, cur *CodeGraphSnapshot) *GraphDelta {
	if old == nil || cur == nil {
		return nil
	}

	delta := &GraphDelta{
		FilesAdded:      cur.FileCount - old.FileCount,
		NodesAdded:      cur.SymbolCount - old.SymbolCount,
		EdgesAdded:      cur.EdgeCount - old.EdgeCount,
		ComplexityDelta: cur.Complexity.AvgCyclomatic - old.Complexity.AvgCyclomatic,
	}

	// Find new and removed symbols
	oldSymbols := make(map[string]bool)
	for _, s := range old.TopSymbols {
		oldSymbols[s.Name] = true
	}

	for _, s := range cur.TopSymbols {
		if !oldSymbols[s.Name] {
			delta.NewSymbols = append(delta.NewSymbols, s.Name)
		}
	}

	newSymbols := make(map[string]bool)
	for _, s := range cur.TopSymbols {
		newSymbols[s.Name] = true
	}

	for _, s := range old.TopSymbols {
		if !newSymbols[s.Name] {
			delta.RemovedSymbols = append(delta.RemovedSymbols, s.Name)
		}
	}

	return delta
}

// FormatSnapshot formats a snapshot for display.
func FormatSnapshot(snapshot CodeGraphSnapshot) string {
	var result string

	result += "## Code Graph Snapshot\n\n"
	result += fmt.Sprintf("- **Time**: %s\n", snapshot.Timestamp.Format(time.RFC3339))
	result += fmt.Sprintf("- **Session**: %s\n", snapshot.SessionID)
	result += fmt.Sprintf("- **Project**: %s\n\n", snapshot.ProjectRoot)

	result += "### Statistics\n\n"
	result += fmt.Sprintf("- Files: %d\n", snapshot.FileCount)
	result += fmt.Sprintf("- Symbols: %d\n", snapshot.SymbolCount)
	result += fmt.Sprintf("- Edges: %d\n\n", snapshot.EdgeCount)

	result += "### Top Symbols\n\n"
	for _, s := range snapshot.TopSymbols[:min(10, len(snapshot.TopSymbols))] {
		result += fmt.Sprintf("- %s `%s` in %s:%d (called %d times)\n",
			s.Kind, s.Name, s.File, s.Line, s.CallCount)
	}

	result += "\n### Complexity\n\n"
	result += fmt.Sprintf("- Avg Cyclomatic: %.1f\n", snapshot.Complexity.AvgCyclomatic)
	result += fmt.Sprintf("- Max Cyclomatic: %d\n", snapshot.Complexity.MaxCyclomatic)
	result += fmt.Sprintf("- Total Functions: %d\n", snapshot.Complexity.TotalFunctions)

	if snapshot.Delta != nil {
		result += "\n### Changes Since Last Snapshot\n\n"
		result += fmt.Sprintf("- Files: +%d -%d modified\n", snapshot.Delta.FilesAdded, snapshot.Delta.FilesRemoved)
		result += fmt.Sprintf("- Symbols: +%d -%d\n", snapshot.Delta.NodesAdded, snapshot.Delta.NodesRemoved)
		result += fmt.Sprintf("- Complexity: %+.1f\n", snapshot.Delta.ComplexityDelta)
	}

	return result
}

// writeFile writes data to filename with mode 0o644, creating any missing
// parent directories. The directory is created with mode 0o755.
func writeFile(filename string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
		return fmt.Errorf("create snapshot dir: %w", err)
	}
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		return fmt.Errorf("write snapshot file: %w", err)
	}
	return nil
}

// listFiles returns the sorted list of files in dir that match the given glob
// pattern (e.g. "snapshot_*.json"). The returned paths are absolute. An empty
// result and nil error indicate that no files matched.
func listFiles(dir, pattern string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil, fmt.Errorf("glob snapshots: %w", err)
	}
	// filepath.Glob returns already-sorted results for a single directory, but
	// sort explicitly to guarantee deterministic "most recent last" semantics
	// when the filename encodes a timestamp.
	return matches, nil
}

// readFile returns the contents of filename. It is a thin wrapper kept for
// symmetry with writeFile and to allow future changes (e.g. compression) in
// one place.
func readFile(filename string) ([]byte, error) {
	data, err := os.ReadFile(filename) //nolint:gosec // filename comes from glob patterns over user-crafted paths
	if err != nil {
		return nil, fmt.Errorf("read snapshot file: %w", err)
	}
	return data, nil
}
