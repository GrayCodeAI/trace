package cli

import (
	"testing"
	"time"

	graphcontracts "github.com/GrayCodeAI/hawk-core-contracts/graph"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	checkpointid "github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/strategy"
)

func TestBuildTraceGraphExport(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.July, 25, 10, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(20 * time.Minute)
	generatedAt := endedAt.Add(time.Minute)
	states := []*strategy.SessionState{{
		SessionID:    "session-alpha",
		StartedAt:    startedAt,
		EndedAt:      &endedAt,
		Phase:        session.PhaseEnded,
		StepCount:    1,
		FilesTouched: []string{"cli/root.go"},
	}}
	checkpoints := []checkpoint.CommittedInfo{{
		CheckpointID:     checkpointid.CheckpointID("abc123def456"),
		SessionID:        "session-alpha",
		SessionIDs:       []string{"session-alpha"},
		CreatedAt:        endedAt,
		CheckpointsCount: 1,
		FilesTouched:     []string{"cli/root.go"},
		SessionCount:     1,
	}}

	export, err := buildTraceGraphExport(states, checkpoints, generatedAt, "hawk", "", 100)
	if err != nil {
		t.Fatalf("buildTraceGraphExport() error = %v", err)
	}
	if export.SchemaVersion != traceGraphSchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", export.SchemaVersion, traceGraphSchemaVersion)
	}
	if export.Scope.RepositoryID != "hawk" {
		t.Fatalf("Scope.RepositoryID = %q, want hawk", export.Scope.RepositoryID)
	}
	if len(export.Nodes) != 2 {
		t.Fatalf("len(Nodes) = %d, want 2", len(export.Nodes))
	}
	if len(export.Edges) != 1 {
		t.Fatalf("len(Edges) = %d, want 1", len(export.Edges))
	}
	if len(export.Events) != 3 {
		t.Fatalf("len(Events) = %d, want 3", len(export.Events))
	}
	if export.Edges[0].Kind != graphcontracts.EdgeProduced {
		t.Fatalf("Edges[0].Kind = %q, want %q", export.Edges[0].Kind, graphcontracts.EdgeProduced)
	}
	if export.Edges[0].From.ID != traceSessionNodeID("session-alpha") {
		t.Fatalf("Edges[0].From.ID = %q", export.Edges[0].From.ID)
	}
	if export.Edges[0].To.ID != traceCheckpointNodeID("abc123def456") {
		t.Fatalf("Edges[0].To.ID = %q", export.Edges[0].To.ID)
	}
}

func TestBuildTraceGraphExportCreatesCheckpointOnlySessionNode(t *testing.T) {
	t.Parallel()

	generatedAt := time.Date(2026, time.July, 25, 11, 0, 0, 0, time.UTC)
	checkpoints := []checkpoint.CommittedInfo{{
		CheckpointID: checkpointid.CheckpointID("abc123def456"),
		SessionID:    "archived-session",
		CreatedAt:    generatedAt.Add(-time.Hour),
		SessionCount: 1,
	}}

	export, err := buildTraceGraphExport(nil, checkpoints, generatedAt, "trace", "", 100)
	if err != nil {
		t.Fatalf("buildTraceGraphExport() error = %v", err)
	}
	node := findTraceGraphNode(export.Nodes, traceSessionNodeID("archived-session"))
	if node == nil {
		t.Fatal("checkpoint-only session node was not exported")
	}
	if got := node.Attributes["status"]; got != "checkpoint_only" {
		t.Fatalf("checkpoint-only status = %q, want checkpoint_only", got)
	}
	if len(export.Edges) != 1 {
		t.Fatalf("len(Edges) = %d, want 1", len(export.Edges))
	}
}

func TestBuildTraceGraphExportFiltersSessionAndLimitsCheckpoints(t *testing.T) {
	t.Parallel()

	generatedAt := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	states := []*strategy.SessionState{
		{SessionID: "alpha-one", StartedAt: generatedAt.Add(-3 * time.Hour)},
		{SessionID: "beta-one", StartedAt: generatedAt.Add(-3 * time.Hour)},
	}
	checkpoints := []checkpoint.CommittedInfo{
		{
			CheckpointID: checkpointid.CheckpointID("aaaaaaaaaaaa"),
			SessionID:    "alpha-one",
			CreatedAt:    generatedAt.Add(-2 * time.Hour),
		},
		{
			CheckpointID: checkpointid.CheckpointID("bbbbbbbbbbbb"),
			SessionID:    "alpha-one",
			CreatedAt:    generatedAt.Add(-time.Hour),
		},
		{
			CheckpointID: checkpointid.CheckpointID("cccccccccccc"),
			SessionID:    "beta-one",
			CreatedAt:    generatedAt,
		},
	}

	export, err := buildTraceGraphExport(states, checkpoints, generatedAt, "trace", "alpha", 1)
	if err != nil {
		t.Fatalf("buildTraceGraphExport() error = %v", err)
	}
	if len(export.Nodes) != 2 {
		t.Fatalf("len(Nodes) = %d, want one session and one checkpoint", len(export.Nodes))
	}
	if findTraceGraphNode(export.Nodes, traceCheckpointNodeID("bbbbbbbbbbbb")) == nil {
		t.Fatal("newest matching checkpoint was not exported")
	}
	if findTraceGraphNode(export.Nodes, traceSessionNodeID("beta-one")) != nil {
		t.Fatal("non-matching session was exported")
	}
}

func TestBuildTraceCorrelationExportUsesExactStoredIdentity(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.July, 25, 13, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(time.Hour)
	states := []*strategy.SessionState{
		{
			SessionID: "trace-beta",
			StartedAt: startedAt.Add(time.Minute),
			Metadata:  map[string]string{hawkSessionMetadataKey: "hawk-session-1"},
		},
		{
			SessionID: "trace-alpha",
			StartedAt: startedAt,
			EndedAt:   &endedAt,
			Phase:     session.PhaseEnded,
			Metadata:  map[string]string{hawkSessionMetadataKey: "hawk-session-1"},
		},
		{
			SessionID: "trace-prefix-only",
			StartedAt: startedAt,
			Metadata:  map[string]string{hawkSessionMetadataKey: "hawk-session-10"},
		},
	}
	checkpoints := []checkpoint.CommittedInfo{
		{
			CheckpointID: checkpointid.CheckpointID("bbbbbbbbbbbb"),
			SessionIDs:   []string{"trace-alpha"},
		},
		{
			CheckpointID: checkpointid.CheckpointID("aaaaaaaaaaaa"),
			SessionID:    "trace-alpha",
		},
		{
			CheckpointID: checkpointid.CheckpointID("cccccccccccc"),
			SessionID:    "trace-prefix-only",
		},
	}

	export := buildTraceCorrelationExport(states, checkpoints, " hawk-session-1 ")
	if export.SchemaVersion != traceCorrelationSchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", export.SchemaVersion, traceCorrelationSchemaVersion)
	}
	if !export.CheckpointLookupComplete {
		t.Fatal("pure correlation projection should mark supplied checkpoints complete")
	}
	if export.HawkSessionID != "hawk-session-1" {
		t.Fatalf("HawkSessionID = %q", export.HawkSessionID)
	}
	if len(export.Matches) != 2 {
		t.Fatalf("len(Matches) = %d, want 2", len(export.Matches))
	}
	if export.Matches[0].TraceSessionID != "trace-alpha" {
		t.Fatalf("first match = %q, want trace-alpha", export.Matches[0].TraceSessionID)
	}
	if got := export.Matches[0].CheckpointIDs; len(got) != 2 ||
		got[0] != "aaaaaaaaaaaa" || got[1] != "bbbbbbbbbbbb" {
		t.Fatalf("CheckpointIDs = %#v", got)
	}
	if export.Matches[0].EndedAt == nil || export.Matches[0].Phase != string(session.PhaseEnded) {
		t.Fatalf("ended match = %#v", export.Matches[0])
	}
	if len(export.Matches[1].CheckpointIDs) != 0 {
		t.Fatalf("unexpected checkpoints for trace-beta: %#v", export.Matches[1].CheckpointIDs)
	}
}

func TestBuildTraceCorrelationExportReturnsNoSpeculativeMatch(t *testing.T) {
	t.Parallel()

	states := []*strategy.SessionState{{
		SessionID: "hawk-session-1",
		Metadata:  map[string]string{"unrelated": "hawk-session-1"},
	}}
	export := buildTraceCorrelationExport(states, nil, "hawk-session-1")
	if len(export.Matches) != 0 {
		t.Fatalf("len(Matches) = %d, want no inferred match", len(export.Matches))
	}

	empty := buildTraceCorrelationExport(states, nil, "")
	if empty.Matches == nil || len(empty.Matches) != 0 {
		t.Fatalf("empty lookup matches = %#v, want non-nil empty slice", empty.Matches)
	}
}

func findTraceGraphNode(nodes []graphcontracts.Node, id string) *graphcontracts.Node {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
	}
	return nil
}
