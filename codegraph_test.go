package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNewCodeGraph tests CodeGraph constructor and initial state.
func TestNewCodeGraph(t *testing.T) {
	t.Run("create empty graph", func(t *testing.T) {
		cg := NewCodeGraph()

		if cg == nil {
			t.Fatal("NewCodeGraph should return non-nil instance")
		}

		stats := cg.Stats()
		if stats.Nodes != 0 {
			t.Errorf("Expected 0 nodes, got %d", stats.Nodes)
		}
		if stats.Edges != 0 {
			t.Errorf("Expected 0 edges, got %d", stats.Edges)
		}
	})

	t.Run("graph is ready for use", func(t *testing.T) {
		cg := NewCodeGraph()

		// Should be able to add nodes immediately
		node := Node{
			ID:   "file.go:main",
			Kind: NodeFunction,
			Name: "main",
			Path: "file.go",
		}

		err := cg.AddNode(node)
		if err != nil {
			t.Errorf("Should be able to add node: %v", err)
		}
	})
}

// TestAddNode tests adding nodes to the graph.
func TestAddNode(t *testing.T) {
	t.Run("add single node", func(t *testing.T) {
		cg := NewCodeGraph()

		node := Node{
			ID:       "main.go:main",
			Kind:     NodeFunction,
			Name:     "main",
			Path:     "main.go",
			Line:     10,
			EndLine:  25,
			TokenCount: 50,
			Language: "go",
		}

		err := cg.AddNode(node)
		if err != nil {
			t.Fatalf("AddNode failed: %v", err)
		}

		stats := cg.Stats()
		if stats.Nodes != 1 {
			t.Errorf("Expected 1 node, got %d", stats.Nodes)
		}
	})

	t.Run("add multiple node kinds", func(t *testing.T) {
		cg := NewCodeGraph()

		nodes := []Node{
			{ID: "file.go:pkg", Kind: NodePackage, Name: "main", Path: "file.go"},
			{ID: "file.go:MyStruct", Kind: NodeStruct, Name: "MyStruct", Path: "file.go", Line: 10},
			{ID: "file.go:MyInterface", Kind: NodeInterface, Name: "MyInterface", Path: "file.go", Line: 20},
			{ID: "file.go:MyFunc", Kind: NodeFunction, Name: "MyFunc", Path: "file.go", Line: 30},
			{ID: "file.go:myMethod", Kind: NodeMethod, Name: "myMethod", Path: "file.go", Line: 40},
			{ID: "file.go:globalVar", Kind: NodeVariable, Name: "globalVar", Path: "file.go", Line: 50},
			{ID: "file.go:MyConst", Kind: NodeConstant, Name: "MyConst", Path: "file.go", Line: 60},
		}

		for _, node := range nodes {
			err := cg.AddNode(node)
			if err != nil {
				t.Errorf("AddNode failed for %s: %v", node.ID, err)
			}
		}

		stats := cg.Stats()
		if stats.Nodes != 7 {
			t.Errorf("Expected 7 nodes, got %d", stats.Nodes)
		}
	})

	t.Run("duplicate node ID overwrites", func(t *testing.T) {
		cg := NewCodeGraph()

		node1 := Node{ID: "main.go:main", Kind: NodeFunction, Name: "main", TokenCount: 50}
		node2 := Node{ID: "main.go:main", Kind: NodeFunction, Name: "main", TokenCount: 100}

		_ = cg.AddNode(node1)
		_ = cg.AddNode(node2)

		// Should still be 1 node (overwritten)
		stats := cg.Stats()
		if stats.Nodes != 1 {
			t.Errorf("Expected 1 node (overwritten), got %d", stats.Nodes)
		}

		// Retrieve and verify it's the updated version
		retrieved, exists := cg.GetNode("main.go:main")
		if !exists {
			t.Fatal("Node should exist")
		}
		if retrieved.TokenCount != 100 {
			t.Errorf("Expected token count 100, got %d", retrieved.TokenCount)
		}
	})

	t.Run("add node with empty ID fails", func(t *testing.T) {
		cg := NewCodeGraph()

		node := Node{ID: "", Kind: NodeFunction, Name: "main"}
		err := cg.AddNode(node)

		if err == nil {
			t.Error("Expected error for empty node ID")
		}
	})

	t.Run("add node with invalid kind fails", func(t *testing.T) {
		cg := NewCodeGraph()

		node := Node{ID: "test", Kind: "invalid", Name: "test"}
		err := cg.AddNode(node)

		if err == nil {
			t.Error("Expected error for invalid node kind")
		}
	})
}

// TestAddEdge tests adding edges to the graph.
func TestAddEdge(t *testing.T) {
	t.Run("add single edge", func(t *testing.T) {
		cg := NewCodeGraph()

		// Add nodes first
		_ = cg.AddNode(Node{ID: "caller", Kind: NodeFunction, Name: "caller"})
		_ = cg.AddNode(Node{ID: "callee", Kind: NodeFunction, Name: "callee"})

		edge := Edge{
			From: "caller",
			To:   "callee",
			Kind: EdgeCalls,
		}

		err := cg.AddEdge(edge)
		if err != nil {
			t.Fatalf("AddEdge failed: %v", err)
		}

		stats := cg.Stats()
		if stats.Edges != 1 {
			t.Errorf("Expected 1 edge, got %d", stats.Edges)
		}
	})

	t.Run("add multiple edge kinds", func(t *testing.T) {
		cg := NewCodeGraph()

		// Add nodes
		_ = cg.AddNode(Node{ID: "main", Kind: NodeFunction})
		_ = cg.AddNode(Node{ID: "helper", Kind: NodeFunction})
		_ = cg.AddNode(Node{ID: "MyStruct", Kind: NodeStruct})
		_ = cg.AddNode(Node{ID: "MyInterface", Kind: NodeInterface})
		_ = cg.AddNode(Node{ID: "pkg", Kind: NodePackage})

		edges := []Edge{
			{From: "main", To: "helper", Kind: EdgeCalls},
			{From: "MyStruct", To: "MyInterface", Kind: EdgeImplements},
			{From: "main", To: "MyStruct", Kind: EdgeUses},
			{From: "main", To: "pkg", Kind: EdgeImports},
			{From: "helper", To: "main", Kind: EdgeCalledBy},
		}

		for _, edge := range edges {
			err := cg.AddEdge(edge)
			if err != nil {
				t.Errorf("AddEdge failed: %v", err)
			}
		}

		stats := cg.Stats()
		if stats.Edges != 5 {
			t.Errorf("Expected 5 edges, got %d", stats.Edges)
		}
	})

	t.Run("duplicate edge overwrites", func(t *testing.T) {
		cg := NewCodeGraph()

		_ = cg.AddNode(Node{ID: "a", Kind: NodeFunction})
		_ = cg.AddNode(Node{ID: "b", Kind: NodeFunction})

		edge1 := Edge{From: "a", To: "b", Kind: EdgeCalls, Calls: 5}
		edge2 := Edge{From: "a", To: "b", Kind: EdgeCalls, Calls: 10}

		_ = cg.AddEdge(edge1)
		_ = cg.AddEdge(edge2)

		stats := cg.Stats()
		if stats.Edges != 1 {
			t.Errorf("Expected 1 edge (overwritten), got %d", stats.Edges)
		}
	})

	t.Run("edge with non-existent source fails", func(t *testing.T) {
		cg := NewCodeGraph()

		_ = cg.AddNode(Node{ID: "target", Kind: NodeFunction})

		edge := Edge{From: "nonexistent", To: "target", Kind: EdgeCalls}
		err := cg.AddEdge(edge)

		if err == nil {
			t.Error("Expected error for non-existent source node")
		}
	})

	t.Run("edge with non-existent target fails", func(t *testing.T) {
		cg := NewCodeGraph()

		_ = cg.AddNode(Node{ID: "source", Kind: NodeFunction})

		edge := Edge{From: "source", To: "nonexistent", Kind: EdgeCalls}
		err := cg.AddEdge(edge)

		if err == nil {
			t.Error("Expected error for non-existent target node")
		}
	})
}

// TestGetNode tests node retrieval.
func TestGetNode(t *testing.T) {
	t.Run("retrieve existing node", func(t *testing.T) {
		cg := NewCodeGraph()

		original := Node{
			ID:         "main.go:main",
			Kind:       NodeFunction,
			Name:       "main",
			Path:       "main.go",
			Line:       10,
			TokenCount: 50,
		}
		_ = cg.AddNode(original)

		retrieved, exists := cg.GetNode("main.go:main")
		if !exists {
			t.Fatal("Node should exist")
		}

		if retrieved.ID != original.ID {
			t.Errorf("ID mismatch: %s vs %s", retrieved.ID, original.ID)
		}
		if retrieved.Kind != original.Kind {
			t.Errorf("Kind mismatch: %s vs %s", retrieved.Kind, original.Kind)
		}
		if retrieved.Name != original.Name {
			t.Errorf("Name mismatch: %s vs %s", retrieved.Name, original.Name)
		}
		if retrieved.TokenCount != original.TokenCount {
			t.Errorf("TokenCount mismatch: %d vs %d", retrieved.TokenCount, original.TokenCount)
		}
	})

	t.Run("retrieve non-existent node", func(t *testing.T) {
		cg := NewCodeGraph()

		_, exists := cg.GetNode("nonexistent")
		if exists {
			t.Error("Non-existent node should not be found")
		}
	})

	t.Run("retrieved node is a copy", func(t *testing.T) {
		cg := NewCodeGraph()

		original := Node{ID: "test", Kind: NodeFunction, Name: "test", TokenCount: 50}
		_ = cg.AddNode(original)

		retrieved, _ := cg.GetNode("test")
		retrieved.TokenCount = 999

		// Get again and verify it wasn't modified
		again, _ := cg.GetNode("test")
		if again.TokenCount != 50 {
			t.Error("Modifying retrieved node should not affect graph")
		}
	})
}

// TestGetEdges tests edge retrieval.
func TestGetEdges(t *testing.T) {
	t.Run("get outgoing edges", func(t *testing.T) {
		cg := NewCodeGraph()

		_ = cg.AddNode(Node{ID: "a", Kind: NodeFunction})
		_ = cg.AddNode(Node{ID: "b", Kind: NodeFunction})
		_ = cg.AddNode(Node{ID: "c", Kind: NodeFunction})

		_ = cg.AddEdge(Edge{From: "a", To: "b", Kind: EdgeCalls})
		_ = cg.AddEdge(Edge{From: "a", To: "c", Kind: EdgeCalls})
		_ = cg.AddEdge(Edge{From: "b", To: "c", Kind: EdgeCalls})

		edges := cg.GetEdges("a")
		if len(edges) != 2 {
			t.Errorf("Expected 2 outgoing edges from 'a', got %d", len(edges))
		}
	})

	t.Run("get edges for node with no outgoing edges", func(t *testing.T) {
		cg := NewCodeGraph()

		_ = cg.AddNode(Node{ID: "a", Kind: NodeFunction})
		_ = cg.AddNode(Node{ID: "b", Kind: NodeFunction})
		_ = cg.AddEdge(Edge{From: "a", To: "b", Kind: EdgeCalls})

		edges := cg.GetEdges("b")
		if len(edges) != 0 {
			t.Errorf("Expected 0 outgoing edges from 'b', got %d", len(edges))
		}
	})

	t.Run("get edges for non-existent node", func(t *testing.T) {
		cg := NewCodeGraph()

		edges := cg.GetEdges("nonexistent")
		if len(edges) != 0 {
			t.Errorf("Expected 0 edges for non-existent node, got %d", len(edges))
		}
	})

	t.Run("edges are copies", func(t *testing.T) {
		cg := NewCodeGraph()

		_ = cg.AddNode(Node{ID: "a", Kind: NodeFunction})
		_ = cg.AddNode(Node{ID: "b", Kind: NodeFunction})
		_ = cg.AddEdge(Edge{From: "a", To: "b", Kind: EdgeCalls, Calls: 5})

		edges := cg.GetEdges("a")
		if len(edges) == 0 {
			t.Fatal("Expected at least one edge")
		}

		edges[0].Calls = 999

		// Get again and verify it wasn't modified
		again := cg.GetEdges("a")
		if again[0].Calls != 5 {
			t.Error("Modifying edge slice should not affect graph")
		}
	})
}

// TestQueryNodes tests node querying.
func TestQueryNodes(t *testing.T) {
	t.Run("query by kind", func(t *testing.T) {
		cg := NewCodeGraph()

		_ = cg.AddNode(Node{ID: "f1", Kind: NodeFunction, Name: "func1"})
		_ = cg.AddNode(Node{ID: "f2", Kind: NodeFunction, Name: "func2"})
		_ = cg.AddNode(Node{ID: "s1", Kind: NodeStruct, Name: "struct1"})
		_ = cg.AddNode(Node{ID: "i1", Kind: NodeInterface, Name: "iface1"})

		functions := cg.QueryNodes(NodeQuery{Kind: NodeFunction})
		if len(functions) != 2 {
			t.Errorf("Expected 2 functions, got %d", len(functions))
		}

		structs := cg.QueryNodes(NodeQuery{Kind: NodeStruct})
		if len(structs) != 1 {
			t.Errorf("Expected 1 struct, got %d", len(structs))
		}
	})

	t.Run("query by path", func(t *testing.T) {
		cg := NewCodeGraph()

		_ = cg.AddNode(Node{ID: "main.go:f1", Kind: NodeFunction, Path: "main.go"})
		_ = cg.AddNode(Node{ID: "main.go:f2", Kind: NodeFunction, Path: "main.go"})
		_ = cg.AddNode(Node{ID: "util.go:f1", Kind: NodeFunction, Path: "util.go"})

		mainNodes := cg.QueryNodes(NodeQuery{Path: "main.go"})
		if len(mainNodes) != 2 {
			t.Errorf("Expected 2 nodes in main.go, got %d", len(mainNodes))
		}
	})

	t.Run("query with limit", func(t *testing.T) {
		cg := NewCodeGraph()

		for i := 0; i < 10; i++ {
			_ = cg.AddNode(Node{
				ID:   string(rune('A' + i)),
				Kind: NodeFunction,
				Name: string(rune('A' + i)),
			})
		}

		nodes := cg.QueryNodes(NodeQuery{Kind: NodeFunction, Limit: 3})
		if len(nodes) != 3 {
			t.Errorf("Expected 3 nodes (limited), got %d", len(nodes))
		}
	})

	t.Run("query with offset", func(t *testing.T) {
		cg := NewCodeGraph()

		for i := 0; i < 10; i++ {
			_ = cg.AddNode(Node{
				ID:   string(rune('A' + i)),
				Kind: NodeFunction,
			})
		}

		// Get all nodes first
		all := cg.QueryNodes(NodeQuery{Kind: NodeFunction})

		// Then get with offset
		offset := cg.QueryNodes(NodeQuery{Kind: NodeFunction, Offset: 5})

		if len(offset) != len(all)-5 {
			t.Errorf("Expected %d nodes (with offset), got %d", len(all)-5, len(offset))
		}
	})

	t.Run("query with no matches", func(t *testing.T) {
		cg := NewCodeGraph()

		_ = cg.AddNode(Node{ID: "f1", Kind: NodeFunction, Path: "main.go"})

		nodes := cg.QueryNodes(NodeQuery{Path: "nonexistent.go"})
		if len(nodes) != 0 {
			t.Errorf("Expected 0 nodes, got %d", len(nodes))
		}
	})
}

// TestGraphStatistics tests the Stats method.
func TestGraphStatistics(t *testing.T) {
	t.Run("empty graph stats", func(t *testing.T) {
		cg := NewCodeGraph()
		stats := cg.Stats()

		if stats.Nodes != 0 {
			t.Errorf("Expected 0 nodes, got %d", stats.Nodes)
		}
		if stats.Edges != 0 {
			t.Errorf("Expected 0 edges, got %d", stats.Edges)
		}
		if len(stats.ByKind) != 0 {
			t.Errorf("Expected empty ByKind map, got %d entries", len(stats.ByKind))
		}
	})

	t.Run("populated graph stats", func(t *testing.T) {
		cg := NewCodeGraph()

		_ = cg.AddNode(Node{ID: "f1", Kind: NodeFunction})
		_ = cg.AddNode(Node{ID: "f2", Kind: NodeFunction})
		_ = cg.AddNode(Node{ID: "s1", Kind: NodeStruct})
		_ = cg.AddNode(Node{ID: "i1", Kind: NodeInterface})

		_ = cg.AddEdge(Edge{From: "f1", To: "f2", Kind: EdgeCalls})
		_ = cg.AddEdge(Edge{From: "f2", To: "s1", Kind: EdgeUses})

		stats := cg.Stats()

		if stats.Nodes != 4 {
			t.Errorf("Expected 4 nodes, got %d", stats.Nodes)
		}
		if stats.Edges != 2 {
			t.Errorf("Expected 2 edges, got %d", stats.Edges)
		}
		if stats.ByKind["function"] != 2 {
			t.Errorf("Expected 2 functions, got %d", stats.ByKind["function"])
		}
		if stats.ByKind["struct"] != 1 {
			t.Errorf("Expected 1 struct, got %d", stats.ByKind["struct"])
		}
	})
}

// TestCodeGraphSnapshot tests serialization and persistence.
func TestCodeGraphSnapshot(t *testing.T) {
	t.Run("persist and load", func(t *testing.T) {
		// Create a temporary directory for the test
		tmpDir := t.TempDir()
		snapshotPath := filepath.Join(tmpDir, "graph.snapshot")

		// Create and populate a graph
		cg1 := NewCodeGraph()

		now := time.Now().UTC()
		_ = cg1.AddNode(Node{
			ID:         "main",
			Kind:       NodeFunction,
			Name:       "main",
			Path:       "main.go",
			Line:       10,
			EndLine:    50,
			TokenCount: 100,
			Language:   "go",
			CreatedAt:  now,
			UpdatedAt:  now,
		})

		_ = cg1.AddNode(Node{
			ID:         "helper",
			Kind:       NodeFunction,
			Name:       "helper",
			Path:       "helper.go",
			Line:       20,
			EndLine:    30,
			TokenCount: 50,
			Language:   "go",
			CreatedAt:  now,
			UpdatedAt:  now,
		})

		_ = cg1.AddEdge(Edge{
			From:      "main",
			To:        "helper",
			Kind:      EdgeCalls,
			Calls:     5,
			CreatedAt: now,
			UpdatedAt: now,
		})

		// Persist to disk
		err := cg1.Persist(snapshotPath)
		if err != nil {
			t.Fatalf("Persist failed: %v", err)
		}

		// Verify file was created
		if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
			t.Fatal("Snapshot file was not created")
		}

		// Load into a new graph
		cg2, err := LoadSnapshot(snapshotPath)
		if err != nil {
			t.Fatalf("LoadSnapshot failed: %v", err)
		}

		// Verify stats match
		stats1 := cg1.Stats()
		stats2 := cg2.Stats()

		if stats1.Nodes != stats2.Nodes {
			t.Errorf("Node count mismatch: %d vs %d", stats1.Nodes, stats2.Nodes)
		}
		if stats1.Edges != stats2.Edges {
			t.Errorf("Edge count mismatch: %d vs %d", stats1.Edges, stats2.Edges)
		}

		// Verify node data
		node1, _ := cg1.GetNode("main")
		node2, _ := cg2.GetNode("main")

		if node1.Name != node2.Name {
			t.Errorf("Node name mismatch: %s vs %s", node1.Name, node2.Name)
		}
		if node1.TokenCount != node2.TokenCount {
			t.Errorf("Token count mismatch: %d vs %d", node1.TokenCount, node2.TokenCount)
		}

		// Verify edge data
		edges1 := cg1.GetEdges("main")
		edges2 := cg2.GetEdges("main")

		if len(edges1) != len(edges2) {
			t.Errorf("Edge count mismatch: %d vs %d", len(edges1), len(edges2))
		}

		if edges1[0].Calls != edges2[0].Calls {
			t.Errorf("Edge calls mismatch: %d vs %d", edges1[0].Calls, edges2[0].Calls)
		}
	})

	t.Run("load non-existent file", func(t *testing.T) {
		_, err := LoadSnapshot("/nonexistent/path/graph.snapshot")
		if err == nil {
			t.Error("Expected error for non-existent file")
		}
	})

	t.Run("load corrupt file", func(t *testing.T) {
		tmpDir := t.TempDir()
		corruptPath := filepath.Join(tmpDir, "corrupt.snapshot")

		// Write invalid JSON
		err := os.WriteFile(corruptPath, []byte("{invalid json}"), 0644)
		if err != nil {
			t.Fatalf("Failed to create corrupt file: %v", err)
		}

		_, err = LoadSnapshot(corruptPath)
		if err == nil {
			t.Error("Expected error for corrupt file")
		}
	})
}

// TestCodeGraphSnapshotJSON tests JSON marshaling/unmarshaling.
func TestCodeGraphSnapshotJSON(t *testing.T) {
	t.Run("marshal to JSON", func(t *testing.T) {
		cg := NewCodeGraph()

		now := time.Now().UTC()
		_ = cg.AddNode(Node{
			ID:         "main",
			Kind:       NodeFunction,
			Name:       "main",
			CreatedAt:  now,
			UpdatedAt:  now,
		})

		_ = cg.AddEdge(Edge{
			From:      "main",
			To:        "main", // self-reference for simplicity
			Kind:      EdgeCalls,
			CreatedAt: now,
			UpdatedAt: now,
		})

		snapshot := cg.Snapshot()

		data, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		// Verify it's valid JSON
		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("Output is not valid JSON: %v", err)
		}

		// Check for expected fields
		if _, ok := parsed["version"]; !ok {
			t.Error("Expected version field in JSON")
		}
		if _, ok := parsed["snapshot_at"]; !ok {
			t.Error("Expected snapshot_at field in JSON")
		}
		if _, ok := parsed["nodes"]; !ok {
			t.Error("Expected nodes field in JSON")
		}
		if _, ok := parsed["edges"]; !ok {
			t.Error("Expected edges field in JSON")
		}
	})

	t.Run("unmarshal from JSON", func(t *testing.T) {
		jsonData := []byte(`{
			"version": "1.0",
			"snapshot_at": "2026-01-15T10:30:00Z",
			"nodes": [
				{
					"id": "f1",
					"kind": "function",
					"name": "func1",
					"path": "file.go",
					"line": 10,
					"end_line": 20,
					"token_count": 50,
					"language": "go",
					"created_at": "2026-01-15T10:00:00Z",
					"updated_at": "2026-01-15T10:00:00Z"
				},
				{
					"id": "f2",
					"kind": "function",
					"name": "func2",
					"path": "file.go",
					"line": 25,
					"end_line": 35,
					"token_count": 30,
					"language": "go",
					"created_at": "2026-01-15T10:00:00Z",
					"updated_at": "2026-01-15T10:00:00Z"
				}
			],
			"edges": [
				{
					"from": "f1",
					"to": "f2",
					"kind": "calls",
					"calls": 3,
					"created_at": "2026-01-15T10:00:00Z",
					"updated_at": "2026-01-15T10:00:00Z"
				}
			]
		}`)

		var snapshot CodeGraphData
		err := json.Unmarshal(jsonData, &snapshot)
		if err != nil {
			t.Fatalf("JSON unmarshal failed: %v", err)
		}

		if snapshot.Version != "1.0" {
			t.Errorf("Expected version 1.0, got %s", snapshot.Version)
		}

		if len(snapshot.Nodes) != 2 {
			t.Errorf("Expected 2 nodes, got %d", len(snapshot.Nodes))
		}

		if len(snapshot.Edges) != 1 {
			t.Errorf("Expected 1 edge, got %d", len(snapshot.Edges))
		}

		if snapshot.Nodes[0].Name != "func1" {
			t.Errorf("Expected node name func1, got %s", snapshot.Nodes[0].Name)
		}

		if snapshot.Edges[0].Calls != 3 {
			t.Errorf("Expected 3 calls, got %d", snapshot.Edges[0].Calls)
		}
	})
}

// TestConcurrentAccess tests thread safety of CodeGraph operations.
func TestConcurrentAccess(t *testing.T) {
	cg := NewCodeGraph()

	const numGoroutines = 10
	const operationsPerGoroutine = 100

	done := make(chan bool, numGoroutines)

	// Concurrent node additions
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()

			for j := 0; j < operationsPerGoroutine; j++ {
				node := Node{
					ID:   string(rune('A'+id)) + string(rune('0'+j%10)),
					Kind: NodeFunction,
					Name: string(rune('A'+id)) + string(rune('0'+j%10)),
				}
				_ = cg.AddNode(node)
			}
		}(i)
	}

	// Wait for all node additions
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Reset for edge operations
	done = make(chan bool, numGoroutines)

	// Concurrent reads and queries
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer func() { done <- true }()

			for j := 0; j < operationsPerGoroutine; j++ {
				_ = cg.Stats()
				_ = cg.QueryNodes(NodeQuery{Kind: NodeFunction})
				_, _ = cg.GetNode("A0")
			}
		}()
	}

	// Wait for all reads
	for i := 0; i < numGoroutines; i++ {
		<-done
	}
}

// BenchmarkAddNode benchmarks node addition.
func BenchmarkAddNode(b *testing.B) {
	cg := NewCodeGraph()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		node := Node{
			ID:   string(rune(i % 1000)),
			Kind: NodeFunction,
			Name: "benchmark",
		}
		_ = cg.AddNode(node)
	}
}

// BenchmarkAddEdge benchmarks edge addition.
func BenchmarkAddEdge(b *testing.B) {
	cg := NewCodeGraph()

	// Pre-populate with nodes
	for i := 0; i < 100; i++ {
		_ = cg.AddNode(Node{
			ID:   string(rune(i)),
			Kind: NodeFunction,
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		edge := Edge{
			From: string(rune(i % 100)),
			To:   string(rune((i + 1) % 100)),
			Kind: EdgeCalls,
		}
		_ = cg.AddEdge(edge)
	}
}

// BenchmarkQueryNodes benchmarks node querying.
func BenchmarkQueryNodes(b *testing.B) {
	cg := NewCodeGraph()

	// Pre-populate
	for i := 0; i < 1000; i++ {
		_ = cg.AddNode(Node{
			ID:   string(rune(i)),
			Kind: NodeFunction,
			Path: string(rune(i % 10)),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cg.QueryNodes(NodeQuery{Kind: NodeFunction})
	}
}
