package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServerCreatesServer(t *testing.T) {
	s := NewServer()
	assert.NotNil(t, s)
	assert.NotNil(t, s.tools)
}

func TestRegisterToolAddsTool(t *testing.T) {
	s := NewServer()
	tool := &Tool{
		Name:        "echo",
		Description: "Echoes input back",
		Handler: func(params json.RawMessage) (json.RawMessage, error) {
			return params, nil
		},
	}
	s.RegisterTool(tool)

	registered, ok := s.tools["echo"]
	require.True(t, ok)
	assert.Equal(t, "echo", registered.Name)
	assert.Equal(t, "Echoes input back", registered.Description)
}

func TestListToolsReturnsRegisteredTools(t *testing.T) {
	s := NewServer()
	s.RegisterTool(&Tool{Name: "a", Description: "tool a", Handler: func(p json.RawMessage) (json.RawMessage, error) { return p, nil }})
	s.RegisterTool(&Tool{Name: "b", Description: "tool b", Handler: func(p json.RawMessage) (json.RawMessage, error) { return p, nil }})
	s.RegisterTool(&Tool{Name: "c", Description: "tool c", Handler: func(p json.RawMessage) (json.RawMessage, error) { return p, nil }})

	tools := s.ListTools()
	assert.Len(t, tools, 3)

	names := make(map[string]bool)
	for _, t := range tools {
		names[t.Name] = true
	}
	assert.True(t, names["a"])
	assert.True(t, names["b"])
	assert.True(t, names["c"])
}

func TestHandleRequestToolsList(t *testing.T) {
	s := NewServer()
	s.RegisterTool(&Tool{Name: "foo", Description: "foo tool", Handler: func(p json.RawMessage) (json.RawMessage, error) { return p, nil }})
	s.RegisterTool(&Tool{Name: "bar", Description: "bar tool", Handler: func(p json.RawMessage) (json.RawMessage, error) { return p, nil }})

	resp := s.HandleRequest(&Request{Method: "tools/list"})
	assert.Empty(t, resp.Error)

	// Result should be the list of tools.
	tools, ok := resp.Result.([]*Tool)
	require.True(t, ok, "expected []*Tool result")
	assert.Len(t, tools, 2)
}

func TestHandleRequestToolsCallRoutesToHandler(t *testing.T) {
	s := NewServer()
	s.RegisterTool(&Tool{
		Name:        "greet",
		Description: "Greets someone",
		Handler: func(params json.RawMessage) (json.RawMessage, error) {
			var input struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(params, &input); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]string{"greeting": "hello " + input.Name})
		},
	})

	callParams, err := json.Marshal(toolsCallParams{
		Name:   "greet",
		Params: json.RawMessage(`{"name":"world"}`),
	})
	require.NoError(t, err)

	resp := s.HandleRequest(&Request{
		Method: "tools/call",
		Params: callParams,
	})
	require.Empty(t, resp.Error)

	resultBytes, err := json.Marshal(resp.Result)
	require.NoError(t, err)

	var out map[string]string
	require.NoError(t, json.Unmarshal(resultBytes, &out))
	assert.Equal(t, "hello world", out["greeting"])
}

func TestHandleRequestUnknownMethod(t *testing.T) {
	s := NewServer()

	resp := s.HandleRequest(&Request{Method: "foo/bar"})
	assert.Equal(t, "unknown method: foo/bar", resp.Error)
	assert.Nil(t, resp.Result)
}
