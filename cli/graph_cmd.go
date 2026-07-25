package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	graphcontracts "github.com/GrayCodeAI/hawk-core-contracts/graph"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/spf13/cobra"
)

const (
	traceGraphSchemaVersion       = "hawk.graph/v1"
	traceCorrelationSchemaVersion = "trace.correlation/v1"
)

const hawkSessionMetadataKey = "hawk_session_id"

type traceGraphExport struct {
	SchemaVersion string                 `json:"schema_version"`
	GeneratedAt   time.Time              `json:"generated_at"`
	Scope         graphcontracts.Scope   `json:"scope"`
	Nodes         []graphcontracts.Node  `json:"nodes"`
	Edges         []graphcontracts.Edge  `json:"edges"`
	Events        []graphcontracts.Event `json:"events"`
}

type traceCorrelationExport struct {
	SchemaVersion            string                  `json:"schema_version"`
	HawkSessionID            string                  `json:"hawk_session_id"`
	CheckpointLookupComplete bool                    `json:"checkpoint_lookup_complete"`
	Matches                  []traceCorrelationMatch `json:"matches"`
}

type traceCorrelationMatch struct {
	TraceSessionID string     `json:"trace_session_id"`
	CheckpointIDs  []string   `json:"checkpoint_ids"`
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	Phase          string     `json:"phase,omitempty"`
}

func newGraphCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Export Trace execution data as a portable graph",
		Long: `Project Trace sessions and checkpoints into the shared Hawk graph contract.

Trace remains the source of truth for session and checkpoint storage. The graph
command emits a read-only, portable projection for orchestration, analysis, and
visualization.`,
		Args: cobra.NoArgs,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := paths.WorktreeRoot(cmd.Context()); err != nil {
				return fmt.Errorf("not a git repository: %w", err)
			}
			return nil
		},
	}

	cmd.AddCommand(newGraphExportCmd())
	cmd.AddCommand(newGraphCorrelationCmd())
	return cmd
}

func newGraphCorrelationCmd() *cobra.Command {
	var hawkSessionID string

	cmd := &cobra.Command{
		Use:   "correlation",
		Short: "Resolve authoritative Trace IDs for a Hawk session",
		Long: `Resolve exact Hawk-to-Trace identity captured at Trace session start.

Trace matches only the stored TRACE_TAG_HAWK_SESSION_ID metadata value. It
returns an empty matches array when no authoritative mapping exists and never
infers identity from branches, commits, timestamps, or prompt content.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hawkSessionID = strings.TrimSpace(hawkSessionID)
			if hawkSessionID == "" {
				return fmt.Errorf("--hawk-session is required")
			}

			ctx := cmd.Context()
			states, err := strategy.ListSessionStates(ctx)
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}
			committed := []checkpoint.CommittedInfo(nil)
			checkpointLookupComplete := true
			lookup, lookupErr := newExplainCheckpointLookup(ctx)
			if lookupErr != nil {
				checkpointLookupComplete = false
			} else {
				committed = lookup.committed
			}

			export := buildTraceCorrelationExport(states, committed, hawkSessionID)
			export.CheckpointLookupComplete = checkpointLookupComplete
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(export); err != nil {
				return fmt.Errorf("encode graph correlation: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&hawkSessionID, "hawk-session", "", "Exact Hawk persisted-session ID")
	return cmd
}

func buildTraceCorrelationExport(
	states []*strategy.SessionState,
	committed []checkpoint.CommittedInfo,
	hawkSessionID string,
) traceCorrelationExport {
	hawkSessionID = strings.TrimSpace(hawkSessionID)
	export := traceCorrelationExport{
		SchemaVersion:            traceCorrelationSchemaVersion,
		HawkSessionID:            hawkSessionID,
		CheckpointLookupComplete: true,
		Matches:                  make([]traceCorrelationMatch, 0),
	}
	if hawkSessionID == "" {
		return export
	}
	checkpointsBySession := make(map[string]map[string]struct{})
	for _, info := range committed {
		checkpointID := strings.TrimSpace(string(info.CheckpointID))
		if checkpointID == "" {
			continue
		}
		for _, traceSessionID := range graphCheckpointSessionIDs(info, "") {
			if checkpointsBySession[traceSessionID] == nil {
				checkpointsBySession[traceSessionID] = make(map[string]struct{})
			}
			checkpointsBySession[traceSessionID][checkpointID] = struct{}{}
		}
	}

	for _, state := range states {
		if state == nil || state.Metadata[hawkSessionMetadataKey] != hawkSessionID {
			continue
		}
		traceSessionID := strings.TrimSpace(state.SessionID)
		if traceSessionID == "" {
			continue
		}
		checkpointIDs := make([]string, 0, len(checkpointsBySession[traceSessionID]))
		for checkpointID := range checkpointsBySession[traceSessionID] {
			checkpointIDs = append(checkpointIDs, checkpointID)
		}
		sort.Strings(checkpointIDs)
		phase := session.PhaseFromString(string(state.Phase))
		if state.EndedAt != nil {
			phase = session.PhaseEnded
		}
		export.Matches = append(export.Matches, traceCorrelationMatch{
			TraceSessionID: traceSessionID,
			CheckpointIDs:  checkpointIDs,
			StartedAt:      state.StartedAt.UTC(),
			EndedAt:        state.EndedAt,
			Phase:          string(phase),
		})
	}
	sort.Slice(export.Matches, func(i, j int) bool {
		return export.Matches[i].TraceSessionID < export.Matches[j].TraceSessionID
	})
	return export
}

func newGraphExportCmd() *cobra.Command {
	var sessionPrefix string
	var limit int

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export sessions, checkpoints, relationships, and lifecycle events",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 1 {
				return fmt.Errorf("--limit must be greater than zero")
			}

			ctx := cmd.Context()
			root, err := paths.WorktreeRoot(ctx)
			if err != nil {
				return fmt.Errorf("resolve repository: %w", err)
			}
			states, err := strategy.ListSessionStates(ctx)
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}
			lookup, err := newExplainCheckpointLookup(ctx)
			if err != nil {
				return fmt.Errorf("list checkpoints for graph export: %w", err)
			}

			export, err := buildTraceGraphExport(
				states,
				lookup.committed,
				time.Now().UTC(),
				filepath.Base(filepath.Clean(root)),
				strings.TrimSpace(sessionPrefix),
				limit,
			)
			if err != nil {
				return err
			}

			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(export); err != nil {
				return fmt.Errorf("encode graph export: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sessionPrefix, "session", "", "Filter by session ID or prefix")
	cmd.Flags().IntVar(&limit, "limit", branchCheckpointsLimit, "Maximum checkpoints to include")
	return cmd
}

func buildTraceGraphExport(
	states []*strategy.SessionState,
	committed []checkpoint.CommittedInfo,
	generatedAt time.Time,
	repositoryID string,
	sessionPrefix string,
	limit int,
) (traceGraphExport, error) {
	if generatedAt.IsZero() {
		return traceGraphExport{}, fmt.Errorf("build graph export: generated time is required")
	}
	if limit < 1 {
		return traceGraphExport{}, fmt.Errorf("build graph export: limit must be greater than zero")
	}

	generatedAt = generatedAt.UTC()
	scope := graphcontracts.Scope{RepositoryID: strings.TrimSpace(repositoryID)}
	export := traceGraphExport{
		SchemaVersion: traceGraphSchemaVersion,
		GeneratedAt:   generatedAt,
		Scope:         scope,
		Nodes:         make([]graphcontracts.Node, 0),
		Edges:         make([]graphcontracts.Edge, 0),
		Events:        make([]graphcontracts.Event, 0),
	}
	sessionNodes := make(map[string]struct{})

	for _, state := range states {
		if state == nil {
			continue
		}
		sessionID := strings.TrimSpace(state.SessionID)
		if !strings.HasPrefix(sessionID, sessionPrefix) {
			continue
		}
		node, events, err := sessionGraphFacts(state, generatedAt, scope)
		if err != nil {
			return traceGraphExport{}, err
		}
		export.Nodes = append(export.Nodes, node)
		export.Events = append(export.Events, events...)
		sessionNodes[sessionID] = struct{}{}
	}

	checkpoints := filterGraphCheckpoints(committed, sessionPrefix, limit)
	for _, info := range checkpoints {
		checkpointNode, checkpointEvent, err := checkpointGraphFacts(info, generatedAt, scope)
		if err != nil {
			return traceGraphExport{}, err
		}
		export.Nodes = append(export.Nodes, checkpointNode)
		export.Events = append(export.Events, checkpointEvent)

		for _, sessionID := range graphCheckpointSessionIDs(info, sessionPrefix) {
			if _, exists := sessionNodes[sessionID]; !exists {
				placeholder, err := checkpointOnlySessionNode(sessionID, info, generatedAt, scope)
				if err != nil {
					return traceGraphExport{}, err
				}
				export.Nodes = append(export.Nodes, placeholder)
				sessionNodes[sessionID] = struct{}{}
			}

			edge := graphcontracts.Edge{
				ID:   "trace/edge/" + sessionID + "/produced/" + string(info.CheckpointID),
				Kind: graphcontracts.EdgeProduced,
				From: graphcontracts.Ref{
					Kind: graphcontracts.NodeExecution,
					ID:   traceSessionNodeID(sessionID),
				},
				To: graphcontracts.Ref{
					Kind: graphcontracts.NodeExecution,
					ID:   traceCheckpointNodeID(string(info.CheckpointID)),
				},
				Scope:     scope,
				CreatedAt: graphFactTime(info.CreatedAt, generatedAt),
				Provenance: graphcontracts.Provenance{
					Producer: "trace",
					SourceID: string(info.CheckpointID),
				},
			}
			if err := edge.Validate(); err != nil {
				return traceGraphExport{}, fmt.Errorf("build graph edge %q: %w", edge.ID, err)
			}
			export.Edges = append(export.Edges, edge)
		}
	}

	sort.Slice(export.Nodes, func(i, j int) bool { return export.Nodes[i].ID < export.Nodes[j].ID })
	sort.Slice(export.Edges, func(i, j int) bool { return export.Edges[i].ID < export.Edges[j].ID })
	sort.Slice(export.Events, func(i, j int) bool { return export.Events[i].ID < export.Events[j].ID })
	return export, nil
}

func sessionGraphFacts(
	state *strategy.SessionState,
	generatedAt time.Time,
	scope graphcontracts.Scope,
) (graphcontracts.Node, []graphcontracts.Event, error) {
	sessionID := strings.TrimSpace(state.SessionID)
	createdAt := graphFactTime(state.StartedAt, generatedAt)
	phase := session.PhaseFromString(string(state.Phase))
	if state.EndedAt != nil {
		phase = session.PhaseEnded
	}

	attributes := map[string]string{
		"entity_type":       "session",
		"status":            string(phase),
		"checkpoint_count":  strconv.Itoa(state.StepCount),
		"files_touched":     strconv.Itoa(len(state.FilesTouched)),
		"fully_condensed":   strconv.FormatBool(state.FullyCondensed),
		"attached_manually": strconv.FormatBool(state.AttachedManually),
	}
	addGraphAttribute(attributes, "agent_type", string(state.AgentType))
	addGraphAttribute(attributes, "model_name", state.ModelName)
	addGraphAttribute(attributes, "kind", string(state.Kind))
	addGraphAttribute(attributes, "turn_id", state.TurnID)
	addGraphAttribute(attributes, "worktree_id", state.WorktreeID)
	addGraphAttribute(attributes, "last_checkpoint_id", string(state.LastCheckpointID))

	node := graphcontracts.Node{
		ID:          traceSessionNodeID(sessionID),
		Kind:        graphcontracts.NodeExecution,
		Scope:       scope,
		CreatedAt:   createdAt,
		EffectiveAt: createdAt,
		Provenance: graphcontracts.Provenance{
			Producer: "trace",
			SourceID: sessionID,
			Evidence: []graphcontracts.ArtifactRef{{URI: "trace://session/" + sessionID}},
		},
		Attributes: attributes,
	}
	if err := node.Validate(); err != nil {
		return graphcontracts.Node{}, nil, fmt.Errorf("build session graph node %q: %w", sessionID, err)
	}

	createdEvent := graphcontracts.Event{
		ID:             "trace/event/session/" + sessionID + "/created",
		Type:           graphcontracts.EventCreated,
		Subject:        graphcontracts.Ref{Kind: node.Kind, ID: node.ID},
		Scope:          scope,
		OccurredAt:     createdAt,
		CorrelationID:  sessionID,
		IdempotencyKey: "trace/session/" + sessionID + "/created",
		Provenance:     node.Provenance,
	}
	if err := createdEvent.Validate(); err != nil {
		return graphcontracts.Node{}, nil, fmt.Errorf("build session created event %q: %w", sessionID, err)
	}
	events := []graphcontracts.Event{createdEvent}

	if state.EndedAt != nil {
		endedAt := graphFactTime(*state.EndedAt, generatedAt)
		endedEvent := graphcontracts.Event{
			ID:             "trace/event/session/" + sessionID + "/ended",
			Type:           graphcontracts.EventTransitioned,
			Subject:        graphcontracts.Ref{Kind: node.Kind, ID: node.ID},
			Scope:          scope,
			OccurredAt:     endedAt,
			CorrelationID:  sessionID,
			CausationID:    createdEvent.ID,
			IdempotencyKey: "trace/session/" + sessionID + "/ended",
			Provenance:     node.Provenance,
		}
		if err := endedEvent.Validate(); err != nil {
			return graphcontracts.Node{}, nil, fmt.Errorf("build session ended event %q: %w", sessionID, err)
		}
		events = append(events, endedEvent)
	}

	return node, events, nil
}

func checkpointGraphFacts(
	info checkpoint.CommittedInfo,
	generatedAt time.Time,
	scope graphcontracts.Scope,
) (graphcontracts.Node, graphcontracts.Event, error) {
	checkpointID := strings.TrimSpace(string(info.CheckpointID))
	createdAt := graphFactTime(info.CreatedAt, generatedAt)
	sessionCount := info.SessionCount
	if sessionCount == 0 {
		sessionCount = len(graphCheckpointSessionIDs(info, ""))
	}
	attributes := map[string]string{
		"entity_type":      "checkpoint",
		"checkpoint_count": strconv.Itoa(info.CheckpointsCount),
		"session_count":    strconv.Itoa(sessionCount),
		"files_touched":    strconv.Itoa(len(info.FilesTouched)),
		"is_task":          strconv.FormatBool(info.IsTask),
	}
	addGraphAttribute(attributes, "agent_type", string(info.Agent))
	addGraphAttribute(attributes, "tool_use_id", info.ToolUseID)

	node := graphcontracts.Node{
		ID:          traceCheckpointNodeID(checkpointID),
		Kind:        graphcontracts.NodeExecution,
		Scope:       scope,
		CreatedAt:   createdAt,
		EffectiveAt: createdAt,
		Provenance: graphcontracts.Provenance{
			Producer: "trace",
			SourceID: checkpointID,
			Evidence: []graphcontracts.ArtifactRef{{URI: "trace://checkpoint/" + checkpointID}},
		},
		Attributes: attributes,
	}
	if err := node.Validate(); err != nil {
		return graphcontracts.Node{}, graphcontracts.Event{}, fmt.Errorf("build checkpoint graph node %q: %w", checkpointID, err)
	}

	event := graphcontracts.Event{
		ID:             "trace/event/checkpoint/" + checkpointID + "/created",
		Type:           graphcontracts.EventCreated,
		Subject:        graphcontracts.Ref{Kind: node.Kind, ID: node.ID},
		Scope:          scope,
		OccurredAt:     createdAt,
		CorrelationID:  checkpointID,
		IdempotencyKey: "trace/checkpoint/" + checkpointID + "/created",
		Provenance:     node.Provenance,
	}
	if err := event.Validate(); err != nil {
		return graphcontracts.Node{}, graphcontracts.Event{}, fmt.Errorf("build checkpoint event %q: %w", checkpointID, err)
	}
	return node, event, nil
}

func checkpointOnlySessionNode(
	sessionID string,
	info checkpoint.CommittedInfo,
	generatedAt time.Time,
	scope graphcontracts.Scope,
) (graphcontracts.Node, error) {
	node := graphcontracts.Node{
		ID:        traceSessionNodeID(sessionID),
		Kind:      graphcontracts.NodeExecution,
		Scope:     scope,
		CreatedAt: graphFactTime(info.CreatedAt, generatedAt),
		Provenance: graphcontracts.Provenance{
			Producer: "trace",
			SourceID: string(info.CheckpointID),
			Evidence: []graphcontracts.ArtifactRef{{URI: "trace://checkpoint/" + string(info.CheckpointID)}},
		},
		Attributes: map[string]string{
			"entity_type": "session",
			"status":      "checkpoint_only",
		},
	}
	if err := node.Validate(); err != nil {
		return graphcontracts.Node{}, fmt.Errorf("build checkpoint-only session node %q: %w", sessionID, err)
	}
	return node, nil
}

func filterGraphCheckpoints(
	committed []checkpoint.CommittedInfo,
	sessionPrefix string,
	limit int,
) []checkpoint.CommittedInfo {
	filtered := make([]checkpoint.CommittedInfo, 0, len(committed))
	for _, info := range committed {
		if sessionPrefix != "" && len(graphCheckpointSessionIDs(info, sessionPrefix)) == 0 {
			continue
		}
		filtered = append(filtered, info)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return string(filtered[i].CheckpointID) < string(filtered[j].CheckpointID)
		}
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered
}

func graphCheckpointSessionIDs(info checkpoint.CommittedInfo, sessionPrefix string) []string {
	candidates := info.SessionIDs
	if len(candidates) == 0 && info.SessionID != "" {
		candidates = []string{info.SessionID}
	}
	seen := make(map[string]struct{}, len(candidates))
	sessionIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		sessionID := strings.TrimSpace(candidate)
		if sessionID == "" || !strings.HasPrefix(sessionID, sessionPrefix) {
			continue
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		sessionIDs = append(sessionIDs, sessionID)
	}
	sort.Strings(sessionIDs)
	return sessionIDs
}

func traceSessionNodeID(sessionID string) string {
	return "trace/session/" + sessionID
}

func traceCheckpointNodeID(checkpointID string) string {
	return "trace/checkpoint/" + checkpointID
}

func graphFactTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value.UTC()
}

func addGraphAttribute(attributes map[string]string, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		attributes[key] = value
	}
}
