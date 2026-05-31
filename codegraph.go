package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// NodeKind represents the type of symbol in the code graph.
type NodeKind string

const (
	NodePackage   NodeKind = "package"
	NodeStruct    NodeKind = "struct"
	NodeInterface NodeKind = "interface"
	NodeFunction  NodeKind = "function"
	NodeMethod    NodeKind = "method"
	NodeVariable  NodeKind = "variable"
	NodeConstant  NodeKind = "constant"
)

// EdgeKind represents the relationship between two nodes.
type EdgeKind string

const (
	EdgeCalls       EdgeKind = "calls"
	EdgeCalledBy    EdgeKind = "called_by"
	EdgeImplements  EdgeKind = "implements"
	EdgeUses        EdgeKind = "uses"
	EdgeImports     EdgeKind = "imports"
	EdgeInherits    EdgeKind = "inherits"
	EdgeContains    EdgeKind = "contains"
)

// Node represents a symbol in the code graph.
type Node struct {
	ID         string    `json:"id"`
	Kind       NodeKind  `json:"kind"`
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Line       int       `json:"line"`
	EndLine    int       `json:"end_line"`
	TokenCount int       `json:"token_count"`
	Language   string    `json:"language"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Edge represents a relationship between two nodes.
type Edge struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Kind      EdgeKind  `json:"kind"`
	Calls     int       `json:"calls"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NodeQuery filters nodes for querying.
type NodeQuery struct {
	Kind   NodeKind
	Path   string
	Limit  int
	Offset int
}

// Stats holds aggregate statistics about the graph.
type Stats struct {
	Nodes  int
	Edges  int
	ByKind map[string]int
}

// CodeGraphData represents a serializable export of the code graph.
type CodeGraphData struct {
	Version    string      `json:"version"`
	SnapshotAt time.Time   `json:"snapshot_at"`
	Nodes      []Node      `json:"nodes"`
	Edges      []Edge      `json:"edges"`
}

// CodeGraph is a directed graph of code symbols and their relationships.
type CodeGraph struct {
	mu    sync.RWMutex
	nodes map[string]*Node
	edges map[string]*Edge // key: "from->to->kind"
}

// NewCodeGraph creates an empty code graph.
func NewCodeGraph() *CodeGraph {
	return &CodeGraph{
		nodes: make(map[string]*Node),
		edges: make(map[string]*Edge),
	}
}

// AddNode adds or updates a node in the graph.
func (cg *CodeGraph) AddNode(node Node) error {
	if node.ID == "" {
		return fmt.Errorf("node ID cannot be empty")
	}

	if !isValidNodeKind(node.Kind) {
		return fmt.Errorf("invalid node kind: %s", node.Kind)
	}

	cg.mu.Lock()
	defer cg.mu.Unlock()

	now := time.Now()
	if node.CreatedAt.IsZero() {
		node.CreatedAt = now
	}
	node.UpdatedAt = now

	cg.nodes[node.ID] = &node
	return nil
}

// AddEdge adds or updates an edge in the graph.
func (cg *CodeGraph) AddEdge(edge Edge) error {
	cg.mu.Lock()
	defer cg.mu.Unlock()

	if _, ok := cg.nodes[edge.From]; !ok {
		return fmt.Errorf("source node %s does not exist", edge.From)
	}
	if _, ok := cg.nodes[edge.To]; !ok {
		return fmt.Errorf("target node %s does not exist", edge.To)
	}

	key := cg.edgeKey(edge)
	now := time.Now()
	if edge.CreatedAt.IsZero() {
		edge.CreatedAt = now
	}
	edge.UpdatedAt = now

	cg.edges[key] = &edge
	return nil
}

// GetNode retrieves a node by ID. Returns a copy.
func (cg *CodeGraph) GetNode(id string) (Node, bool) {
	cg.mu.RLock()
	defer cg.mu.RUnlock()

	node, ok := cg.nodes[id]
	if !ok {
		return Node{}, false
	}
	return *node, true
}

// GetEdges returns all outgoing edges from a node.
func (cg *CodeGraph) GetEdges(nodeID string) []Edge {
	cg.mu.RLock()
	defer cg.mu.RUnlock()

	var result []Edge
	for _, edge := range cg.edges {
		if edge.From == nodeID {
			result = append(result, *edge)
		}
	}
	return result
}

// QueryNodes returns nodes matching the query.
func (cg *CodeGraph) QueryNodes(query NodeQuery) []Node {
	cg.mu.RLock()
	defer cg.mu.RUnlock()

	var result []Node
	for _, node := range cg.nodes {
		if query.Kind != "" && node.Kind != query.Kind {
			continue
		}
		if query.Path != "" && node.Path != query.Path {
			continue
		}
		result = append(result, *node)
	}

	// Apply offset
	if query.Offset > 0 && query.Offset < len(result) {
		result = result[query.Offset:]
	} else if query.Offset >= len(result) {
		return []Node{}
	}

	// Apply limit
	if query.Limit > 0 && query.Limit < len(result) {
		result = result[:query.Limit]
	}

	return result
}

// Stats returns aggregate statistics about the graph.
func (cg *CodeGraph) Stats() Stats {
	cg.mu.RLock()
	defer cg.mu.RUnlock()

	byKind := make(map[string]int)
	for _, node := range cg.nodes {
		byKind[string(node.Kind)]++
	}

	return Stats{
		Nodes:  len(cg.nodes),
		Edges:  len(cg.edges),
		ByKind: byKind,
	}
}

// Snapshot creates a serializable export of the graph.
func (cg *CodeGraph) Snapshot() CodeGraphData {
	cg.mu.RLock()
	defer cg.mu.RUnlock()

	nodes := make([]Node, 0, len(cg.nodes))
	for _, node := range cg.nodes {
		nodes = append(nodes, *node)
	}

	edges := make([]Edge, 0, len(cg.edges))
	for _, edge := range cg.edges {
		edges = append(edges, *edge)
	}

	return CodeGraphData{
		Version:    "1.0",
		SnapshotAt: time.Now(),
		Nodes:      nodes,
		Edges:      edges,
	}
}

// Persist writes the graph to a file as JSON.
func (cg *CodeGraph) Persist(path string) error {
	snapshot := cg.Snapshot()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// LoadSnapshot loads a code graph from a JSON file.
func LoadSnapshot(path string) (*CodeGraph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	var snapshot CodeGraphData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}

	cg := NewCodeGraph()
	for _, node := range snapshot.Nodes {
		cg.nodes[node.ID] = &node
	}
	for i := range snapshot.Edges {
		key := cg.edgeKey(snapshot.Edges[i])
		cg.edges[key] = &snapshot.Edges[i]
	}

	return cg, nil
}

func (cg *CodeGraph) edgeKey(edge Edge) string {
	return fmt.Sprintf("%s->%s->%s", edge.From, edge.To, edge.Kind)
}

func isValidNodeKind(kind NodeKind) bool {
	switch kind {
	case NodePackage, NodeStruct, NodeInterface, NodeFunction, NodeMethod, NodeVariable, NodeConstant:
		return true
	}
	return false
}
