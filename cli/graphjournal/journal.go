// Package graphjournal provides graph-based execution journaling for trace.
package graphjournal

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	graphcontracts "github.com/GrayCodeAI/hawk-core-contracts/graph"
)

// JournalEntry represents a single entry in the execution journal.
type JournalEntry struct {
	ID            string                 `json:"id"`
	Type          string                 `json:"type"` // "agent", "tool", "function", "transition"
	NodeID        string                 `json:"node_id"`
	Status        string                 `json:"status"` // "started", "completed", "failed"
	Input         interface{}            `json:"input,omitempty"`
	Output        interface{}            `json:"output,omitempty"`
	Error         string                 `json:"error,omitempty"`
	Duration      time.Duration          `json:"duration"`
	Timestamp     time.Time              `json:"timestamp"`
	CorrelationID string                 `json:"correlation_id,omitempty"`
	Attrs         map[string]interface{} `json:"attrs,omitempty"`
}

// JournalGraph represents an execution journal as a graph.
type JournalGraph struct {
	mu      sync.RWMutex
	ID      string                  `json:"id"`
	Name    string                  `json:"name"`
	Entries []*JournalEntry         `json:"entries"`
	Nodes   map[string]*JournalNode `json:"nodes"`
	Edges   []JournalEdge           `json:"edges"`
}

// JournalNode represents a node in the execution journal graph.
type JournalNode struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`
	Name        string                 `json:"name"`
	Status      string                 `json:"status"`
	StartedAt   time.Time              `json:"started_at"`
	CompletedAt time.Time              `json:"completed_at"`
	Duration    time.Duration          `json:"duration"`
	Attrs       map[string]interface{} `json:"attrs,omitempty"`
}

// JournalEdge represents an edge in the execution journal graph.
type JournalEdge struct {
	From   string                 `json:"from"`
	To     string                 `json:"to"`
	Kind   string                 `json:"kind"` // "calls", "produces", "depends_on"
	Weight float64                `json:"weight"`
	Attrs  map[string]interface{} `json:"attrs,omitempty"`
}

// NewJournalGraph creates a new journal graph.
func NewJournalGraph(id, name string) *JournalGraph {
	return &JournalGraph{
		ID:      id,
		Name:    name,
		Entries: []*JournalEntry{},
		Nodes:   make(map[string]*JournalNode),
		Edges:   []JournalEdge{},
	}
}

// AddEntry adds a journal entry.
func (g *JournalGraph) AddEntry(entry *JournalEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Entries = append(g.Entries, entry)

	// Create or update node
	if node, ok := g.Nodes[entry.NodeID]; ok {
		node.Status = entry.Status
		if entry.Type == "transition" {
			node.CompletedAt = entry.Timestamp
			node.Duration = entry.Duration
		}
	} else {
		node := &JournalNode{
			ID:        entry.NodeID,
			Type:      entry.Type,
			Name:      entry.NodeID,
			Status:    entry.Status,
			StartedAt: entry.Timestamp,
			Attrs:     entry.Attrs,
		}
		if entry.Type == "transition" {
			node.CompletedAt = entry.Timestamp
			node.Duration = entry.Duration
		}
		g.Nodes[entry.NodeID] = node
	}
}

// AddNode adds a node to the journal graph.
func (g *JournalGraph) AddNode(node *JournalNode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Nodes[node.ID] = node
}

// AddEdge adds an edge to the journal graph.
func (g *JournalGraph) AddEdge(edge JournalEdge) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Edges = append(g.Edges, edge)
}

// GetNode retrieves a node by ID.
func (g *JournalGraph) GetNode(id string) (*JournalNode, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	node, ok := g.Nodes[id]
	return node, ok
}

// GetNodes returns all nodes.
func (g *JournalGraph) GetNodes() []*JournalNode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]*JournalNode, 0, len(g.Nodes))
	for _, node := range g.Nodes {
		result = append(result, node)
	}
	return result
}

// GetEdges returns all edges.
func (g *JournalGraph) GetEdges() []JournalEdge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.Edges
}

// ToJSON serializes the journal graph to JSON.
func (g *JournalGraph) ToJSON() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return json.Marshal(g)
}

// FromJSON deserializes a journal graph from JSON.
func FromJSON(data []byte) (*JournalGraph, error) {
	var g JournalGraph
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("failed to deserialize journal graph: %w", err)
	}
	return &g, nil
}

// ToGraphSpec converts the journal graph to a portable graph spec.
func (g *JournalGraph) ToGraphSpec() *graphcontracts.GraphSpec {
	g.mu.RLock()
	defer g.mu.RUnlock()

	nodes := make([]graphcontracts.NodeSpec, 0, len(g.Nodes))
	for id, node := range g.Nodes {
		config := map[string]string{
			"type":     node.Type,
			"name":     node.Name,
			"status":   node.Status,
			"duration": fmt.Sprintf("%v", node.Duration),
		}

		nodes = append(nodes, graphcontracts.NodeSpec{
			ID:     id,
			Type:   graphcontracts.NodeTypeExecution,
			Name:   node.Name,
			Config: config,
		})
	}

	edges := make([]graphcontracts.EdgeSpec, 0, len(g.Edges))
	for _, edge := range g.Edges {
		edges = append(edges, graphcontracts.EdgeSpec{
			From:   edge.From,
			To:     edge.To,
			Weight: edge.Weight,
		})
	}

	return &graphcontracts.GraphSpec{
		ID:    g.ID,
		Name:  g.Name,
		Nodes: nodes,
		Edges: edges,
	}
}
