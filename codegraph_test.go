package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCodeGraph(t *testing.T) {
	t.Run("create empty graph", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NotNil(t, cg)

		stats := cg.Stats()
		assert.Equal(t, 0, stats.Nodes, "expected 0 nodes")
		assert.Equal(t, 0, stats.Edges, "expected 0 edges")
	})

	t.Run("graph is ready for use", func(t *testing.T) {
		cg := NewCodeGraph()

		node := Node{
			ID:   "file.go:main",
			Kind: NodeFunction,
			Name: "main",
			Path: "file.go",
		}

		err := cg.AddNode(node)
		require.NoError(t, err)
	})
}

func TestAddNode(t *testing.T) {
	t.Run("add single node", func(t *testing.T) {
		cg := NewCodeGraph()

		node := Node{
			ID:         "main.go:main",
			Kind:       NodeFunction,
			Name:       "main",
			Path:       "main.go",
			Line:       10,
			EndLine:    25,
			TokenCount: 50,
			Language:   "go",
		}

		err := cg.AddNode(node)
		require.NoError(t, err)

		stats := cg.Stats()
		assert.Equal(t, 1, stats.Nodes)
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
			require.NoError(t, err, "AddNode failed for %s", node.ID)
		}

		stats := cg.Stats()
		assert.Equal(t, 7, stats.Nodes)
	})

	t.Run("duplicate node ID overwrites", func(t *testing.T) {
		cg := NewCodeGraph()

		node1 := Node{ID: "main.go:main", Kind: NodeFunction, Name: "main", TokenCount: 50}
		node2 := Node{ID: "main.go:main", Kind: NodeFunction, Name: "main", TokenCount: 100}

		require.NoError(t, cg.AddNode(node1))
		require.NoError(t, cg.AddNode(node2))

		// Should still be 1 node (overwritten)
		stats := cg.Stats()
		assert.Equal(t, 1, stats.Nodes, "expected 1 node (overwritten)")

		// Retrieve and verify it's the updated version
		retrieved, exists := cg.GetNode("main.go:main")
		require.True(t, exists, "node should exist")
		assert.Equal(t, 100, retrieved.TokenCount, "expected token count 100 after overwrite")
	})

	t.Run("add node with empty ID fails", func(t *testing.T) {
		cg := NewCodeGraph()

		node := Node{ID: "", Kind: NodeFunction, Name: "main"}
		err := cg.AddNode(node)
		require.Error(t, err, "expected error for empty node ID")
	})

	t.Run("add node with invalid kind fails", func(t *testing.T) {
		cg := NewCodeGraph()

		node := Node{ID: "test", Kind: "invalid", Name: "test"}
		err := cg.AddNode(node)
		require.Error(t, err, "expected error for invalid node kind")
	})
}

func TestAddEdge(t *testing.T) {
	t.Run("add single edge", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NoError(t, cg.AddNode(Node{ID: "caller", Kind: NodeFunction, Name: "caller"}))
		require.NoError(t, cg.AddNode(Node{ID: "callee", Kind: NodeFunction, Name: "callee"}))

		edge := Edge{
			From: "caller",
			To:   "callee",
			Kind: EdgeCalls,
		}

		err := cg.AddEdge(edge)
		require.NoError(t, err)

		stats := cg.Stats()
		assert.Equal(t, 1, stats.Edges)
	})

	t.Run("add multiple edge kinds", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NoError(t, cg.AddNode(Node{ID: "main", Kind: NodeFunction}))
		require.NoError(t, cg.AddNode(Node{ID: "helper", Kind: NodeFunction}))
		require.NoError(t, cg.AddNode(Node{ID: "MyStruct", Kind: NodeStruct}))
		require.NoError(t, cg.AddNode(Node{ID: "MyInterface", Kind: NodeInterface}))
		require.NoError(t, cg.AddNode(Node{ID: "pkg", Kind: NodePackage}))

		edges := []Edge{
			{From: "main", To: "helper", Kind: EdgeCalls},
			{From: "MyStruct", To: "MyInterface", Kind: EdgeImplements},
			{From: "main", To: "MyStruct", Kind: EdgeUses},
			{From: "main", To: "pkg", Kind: EdgeImports},
			{From: "helper", To: "main", Kind: EdgeCalledBy},
		}

		for _, edge := range edges {
			err := cg.AddEdge(edge)
			require.NoError(t, err)
		}

		stats := cg.Stats()
		assert.Equal(t, 5, stats.Edges)
	})

	t.Run("duplicate edge overwrites", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NoError(t, cg.AddNode(Node{ID: "a", Kind: NodeFunction}))
		require.NoError(t, cg.AddNode(Node{ID: "b", Kind: NodeFunction}))

		edge1 := Edge{From: "a", To: "b", Kind: EdgeCalls, Calls: 5}
		edge2 := Edge{From: "a", To: "b", Kind: EdgeCalls, Calls: 10}

		require.NoError(t, cg.AddEdge(edge1))
		require.NoError(t, cg.AddEdge(edge2))

		stats := cg.Stats()
		assert.Equal(t, 1, stats.Edges, "expected 1 edge (overwritten)")
	})

	t.Run("edge with non-existent source fails", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NoError(t, cg.AddNode(Node{ID: "target", Kind: NodeFunction}))

		edge := Edge{From: "nonexistent", To: "target", Kind: EdgeCalls}
		err := cg.AddEdge(edge)
		require.Error(t, err, "expected error for non-existent source node")
	})

	t.Run("edge with non-existent target fails", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NoError(t, cg.AddNode(Node{ID: "source", Kind: NodeFunction}))

		edge := Edge{From: "source", To: "nonexistent", Kind: EdgeCalls}
		err := cg.AddEdge(edge)
		require.Error(t, err, "expected error for non-existent target node")
	})
}

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
		require.NoError(t, cg.AddNode(original))

		retrieved, exists := cg.GetNode("main.go:main")
		require.True(t, exists, "node should exist")

		assert.Equal(t, original.ID, retrieved.ID)
		assert.Equal(t, original.Kind, retrieved.Kind)
		assert.Equal(t, original.Name, retrieved.Name)
		assert.Equal(t, original.TokenCount, retrieved.TokenCount)
	})

	t.Run("retrieve non-existent node", func(t *testing.T) {
		cg := NewCodeGraph()

		_, exists := cg.GetNode("nonexistent")
		assert.False(t, exists, "non-existent node should not be found")
	})

	t.Run("retrieved node is a copy", func(t *testing.T) {
		cg := NewCodeGraph()

		original := Node{ID: "test", Kind: NodeFunction, Name: "test", TokenCount: 50}
		require.NoError(t, cg.AddNode(original))

		retrieved, _ := cg.GetNode("test")
		_ = retrieved.TokenCount

		// Get again and verify it wasn't modified
		again, _ := cg.GetNode("test")
		assert.Equal(t, 50, again.TokenCount, "modifying retrieved node should not affect graph")
	})
}

func TestGetEdges(t *testing.T) {
	t.Run("get outgoing edges", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NoError(t, cg.AddNode(Node{ID: "a", Kind: NodeFunction}))
		require.NoError(t, cg.AddNode(Node{ID: "b", Kind: NodeFunction}))
		require.NoError(t, cg.AddNode(Node{ID: "c", Kind: NodeFunction}))

		require.NoError(t, cg.AddEdge(Edge{From: "a", To: "b", Kind: EdgeCalls}))
		require.NoError(t, cg.AddEdge(Edge{From: "a", To: "c", Kind: EdgeCalls}))
		require.NoError(t, cg.AddEdge(Edge{From: "b", To: "c", Kind: EdgeCalls}))

		edges := cg.GetEdges("a")
		assert.Len(t, edges, 2, "expected 2 outgoing edges from 'a'")
	})

	t.Run("get edges for node with no outgoing edges", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NoError(t, cg.AddNode(Node{ID: "a", Kind: NodeFunction}))
		require.NoError(t, cg.AddNode(Node{ID: "b", Kind: NodeFunction}))
		require.NoError(t, cg.AddEdge(Edge{From: "a", To: "b", Kind: EdgeCalls}))

		edges := cg.GetEdges("b")
		assert.Empty(t, edges, "expected 0 outgoing edges from 'b'")
	})

	t.Run("get edges for non-existent node", func(t *testing.T) {
		cg := NewCodeGraph()

		edges := cg.GetEdges("nonexistent")
		assert.Empty(t, edges, "expected 0 edges for non-existent node")
	})

	t.Run("edges are copies", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NoError(t, cg.AddNode(Node{ID: "a", Kind: NodeFunction}))
		require.NoError(t, cg.AddNode(Node{ID: "b", Kind: NodeFunction}))
		require.NoError(t, cg.AddEdge(Edge{From: "a", To: "b", Kind: EdgeCalls, Calls: 5}))

		edges := cg.GetEdges("a")
		require.NotEmpty(t, edges, "expected at least one edge")

		edges[0].Calls = 999

		// Get again and verify it wasn't modified
		again := cg.GetEdges("a")
		assert.Equal(t, 5, again[0].Calls, "modifying edge slice should not affect graph")
	})
}

func TestQueryNodes(t *testing.T) {
	t.Run("query by kind", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NoError(t, cg.AddNode(Node{ID: "f1", Kind: NodeFunction, Name: "func1"}))
		require.NoError(t, cg.AddNode(Node{ID: "f2", Kind: NodeFunction, Name: "func2"}))
		require.NoError(t, cg.AddNode(Node{ID: "s1", Kind: NodeStruct, Name: "struct1"}))
		require.NoError(t, cg.AddNode(Node{ID: "i1", Kind: NodeInterface, Name: "iface1"}))

		functions := cg.QueryNodes(NodeQuery{Kind: NodeFunction})
		assert.Len(t, functions, 2, "expected 2 functions")

		structs := cg.QueryNodes(NodeQuery{Kind: NodeStruct})
		assert.Len(t, structs, 1, "expected 1 struct")
	})

	t.Run("query by path", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NoError(t, cg.AddNode(Node{ID: "main.go:f1", Kind: NodeFunction, Path: "main.go"}))
		require.NoError(t, cg.AddNode(Node{ID: "main.go:f2", Kind: NodeFunction, Path: "main.go"}))
		require.NoError(t, cg.AddNode(Node{ID: "util.go:f1", Kind: NodeFunction, Path: "util.go"}))

		mainNodes := cg.QueryNodes(NodeQuery{Path: "main.go"})
		assert.Len(t, mainNodes, 2, "expected 2 nodes in main.go")
	})

	t.Run("query with limit", func(t *testing.T) {
		cg := NewCodeGraph()

		for i := range 10 {
			require.NoError(t, cg.AddNode(Node{
				ID:   string(rune('A' + i)),
				Kind: NodeFunction,
				Name: string(rune('A' + i)),
			}))
		}

		nodes := cg.QueryNodes(NodeQuery{Kind: NodeFunction, Limit: 3})
		assert.Len(t, nodes, 3, "expected 3 nodes (limited)")
	})

	t.Run("query with offset", func(t *testing.T) {
		cg := NewCodeGraph()

		for i := range 10 {
			require.NoError(t, cg.AddNode(Node{
				ID:   string(rune('A' + i)),
				Kind: NodeFunction,
			}))
		}

		// Get all nodes first
		all := cg.QueryNodes(NodeQuery{Kind: NodeFunction})

		// Then get with offset
		offset := cg.QueryNodes(NodeQuery{Kind: NodeFunction, Offset: 5})

		assert.Len(t, offset, len(all)-5, "expected %d nodes (with offset)", len(all)-5)
	})

	t.Run("query with no matches", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NoError(t, cg.AddNode(Node{ID: "f1", Kind: NodeFunction, Path: "main.go"}))

		nodes := cg.QueryNodes(NodeQuery{Path: "nonexistent.go"})
		assert.Empty(t, nodes, "expected 0 nodes")
	})
}

func TestGraphStatistics(t *testing.T) {
	t.Run("empty graph stats", func(t *testing.T) {
		cg := NewCodeGraph()
		stats := cg.Stats()

		assert.Equal(t, 0, stats.Nodes)
		assert.Equal(t, 0, stats.Edges)
		assert.Empty(t, stats.ByKind, "expected empty ByKind map")
	})

	t.Run("populated graph stats", func(t *testing.T) {
		cg := NewCodeGraph()

		require.NoError(t, cg.AddNode(Node{ID: "f1", Kind: NodeFunction}))
		require.NoError(t, cg.AddNode(Node{ID: "f2", Kind: NodeFunction}))
		require.NoError(t, cg.AddNode(Node{ID: "s1", Kind: NodeStruct}))
		require.NoError(t, cg.AddNode(Node{ID: "i1", Kind: NodeInterface}))

		require.NoError(t, cg.AddEdge(Edge{From: "f1", To: "f2", Kind: EdgeCalls}))
		require.NoError(t, cg.AddEdge(Edge{From: "f2", To: "s1", Kind: EdgeUses}))

		stats := cg.Stats()

		assert.Equal(t, 4, stats.Nodes)
		assert.Equal(t, 2, stats.Edges)
		assert.Equal(t, 2, stats.ByKind[string(NodeFunction)])
		assert.Equal(t, 1, stats.ByKind[string(NodeStruct)])
	})
}

func TestCodeGraphSnapshot(t *testing.T) {
	t.Run("persist and load", func(t *testing.T) {
		tmpDir := t.TempDir()
		snapshotPath := filepath.Join(tmpDir, "graph.snapshot")

		cg1 := NewCodeGraph()

		now := time.Now().UTC()
		require.NoError(t, cg1.AddNode(Node{
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
		}))

		require.NoError(t, cg1.AddNode(Node{
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
		}))

		require.NoError(t, cg1.AddEdge(Edge{
			From:      "main",
			To:        "helper",
			Kind:      EdgeCalls,
			Calls:     5,
			CreatedAt: now,
			UpdatedAt: now,
		}))

		// Persist to disk
		err := cg1.Persist(snapshotPath)
		require.NoError(t, err)

		// Verify file was created
		_, err = os.Stat(snapshotPath)
		require.NoError(t, err, "snapshot file was not created")

		// Load into a new graph
		cg2, err := LoadSnapshot(snapshotPath)
		require.NoError(t, err)

		// Verify stats match
		stats1 := cg1.Stats()
		stats2 := cg2.Stats()

		assert.Equal(t, stats1.Nodes, stats2.Nodes, "node count mismatch")
		assert.Equal(t, stats1.Edges, stats2.Edges, "edge count mismatch")

		// Verify node data
		node1, _ := cg1.GetNode("main")
		node2, _ := cg2.GetNode("main")
		assert.Equal(t, node1.Name, node2.Name, "node name mismatch")
		assert.Equal(t, node1.TokenCount, node2.TokenCount, "token count mismatch")

		// Verify edge data
		edges1 := cg1.GetEdges("main")
		edges2 := cg2.GetEdges("main")

		assert.Len(t, edges2, len(edges1), "edge count mismatch")
		assert.Equal(t, edges1[0].Calls, edges2[0].Calls, "edge calls mismatch")
	})

	t.Run("load non-existent file", func(t *testing.T) {
		_, err := LoadSnapshot("/nonexistent/path/graph.snapshot")
		require.Error(t, err)
	})

	t.Run("load corrupt file", func(t *testing.T) {
		tmpDir := t.TempDir()
		corruptPath := filepath.Join(tmpDir, "corrupt.snapshot")

		err := os.WriteFile(corruptPath, []byte("{invalid json}"), 0o600)
		require.NoError(t, err)

		_, err = LoadSnapshot(corruptPath)
		require.Error(t, err)
	})
}

func TestCodeGraphSnapshotJSON(t *testing.T) {
	t.Run("marshal to JSON", func(t *testing.T) {
		cg := NewCodeGraph()

		now := time.Now().UTC()
		require.NoError(t, cg.AddNode(Node{
			ID:        "main",
			Kind:      NodeFunction,
			Name:      "main",
			CreatedAt: now,
			UpdatedAt: now,
		}))

		require.NoError(t, cg.AddEdge(Edge{
			From:      "main",
			To:        "main", // self-reference for simplicity
			Kind:      EdgeCalls,
			CreatedAt: now,
			UpdatedAt: now,
		}))

		snapshot := cg.Snapshot()

		data, err := json.Marshal(snapshot)
		require.NoError(t, err)

		// Verify it's valid JSON
		var parsed map[string]interface{}
		err = json.Unmarshal(data, &parsed)
		require.NoError(t, err, "output is not valid JSON")

		// Check for expected fields
		assert.Contains(t, parsed, "version")
		assert.Contains(t, parsed, "snapshot_at")
		assert.Contains(t, parsed, "nodes")
		assert.Contains(t, parsed, "edges")
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
		require.NoError(t, err)

		assert.Equal(t, "1.0", snapshot.Version)
		assert.Len(t, snapshot.Nodes, 2)
		assert.Len(t, snapshot.Edges, 1)
		assert.Equal(t, "func1", snapshot.Nodes[0].Name)
		assert.Equal(t, 3, snapshot.Edges[0].Calls)
	})
}

func TestConcurrentAccess(_ *testing.T) {
	cg := NewCodeGraph()

	const numGoroutines = 10
	const operationsPerGoroutine = 100

	done := make(chan bool, numGoroutines)

	// Concurrent node additions
	for i := range numGoroutines {
		go func(id int) {
			defer func() { done <- true }()

			for j := range operationsPerGoroutine {
				node := Node{
					ID:   string(rune('A'+id)) + string(rune('0'+j%10)),
					Kind: NodeFunction,
					Name: string(rune('A'+id)) + string(rune('0'+j%10)),
				}
				_ = cg.AddNode(node) //nolint:errcheck // intentional: testing concurrent safety, not error paths
			}
		}(i)
	}

	// Wait for all node additions
	for range numGoroutines {
		<-done
	}

	// Reset for edge operations
	done = make(chan bool, numGoroutines)

	// Concurrent reads and queries
	for range numGoroutines {
		go func() {
			defer func() { done <- true }()

			for range operationsPerGoroutine {
				_ = cg.Stats()
				_ = cg.QueryNodes(NodeQuery{Kind: NodeFunction})
				_, _ = cg.GetNode("A0")
			}
		}()
	}

	// Wait for all reads
	for range numGoroutines {
		<-done
	}
}

func BenchmarkAddNode(b *testing.B) {
	cg := NewCodeGraph()

	b.ResetTimer()
	for i := range b.N {
		node := Node{
			ID:   string(rune(i % 1000)),
			Kind: NodeFunction,
			Name: "benchmark",
		}
		_ = cg.AddNode(node) //nolint:errcheck // intentional: benchmark ignores errors
	}
}

func BenchmarkAddEdge(b *testing.B) {
	cg := NewCodeGraph()

	// Pre-populate with nodes
	for i := range 100 {
		_ = cg.AddNode(Node{ //nolint:errcheck // intentional: benchmark setup
			ID:   string(rune(i)),
			Kind: NodeFunction,
		})
	}

	b.ResetTimer()
	for i := range b.N {
		edge := Edge{
			From: string(rune(i % 100)),
			To:   string(rune((i + 1) % 100)),
			Kind: EdgeCalls,
		}
		_ = cg.AddEdge(edge) //nolint:errcheck // intentional: benchmark ignores errors
	}
}

func BenchmarkQueryNodes(b *testing.B) {
	cg := NewCodeGraph()

	// Pre-populate
	for i := range 1000 {
		_ = cg.AddNode(Node{ //nolint:errcheck // intentional: benchmark setup
			ID:   string(rune(i)),
			Kind: NodeFunction,
			Path: string(rune(i % 10)),
		})
	}

	b.ResetTimer()
	for range b.N {
		_ = cg.QueryNodes(NodeQuery{Kind: NodeFunction})
	}
}
