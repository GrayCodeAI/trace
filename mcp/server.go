package mcp

import (
	"encoding/json"
	"fmt"
)

// Tool represents an MCP tool with a name, description, and handler function.
type Tool struct {
	Name        string
	Description string
	Handler     func(params json.RawMessage) (json.RawMessage, error)
}

// Server is a basic MCP server that manages tools and routes requests.
type Server struct {
	tools map[string]*Tool
}

// NewServer creates and returns a new Server.
func NewServer() *Server {
	return &Server{
		tools: make(map[string]*Tool),
	}
}

// RegisterTool adds a tool to the server's registry.
func (s *Server) RegisterTool(t *Tool) {
	s.tools[t.Name] = t
}

// ListTools returns all registered tools.
func (s *Server) ListTools() []*Tool {
	result := make([]*Tool, 0, len(s.tools))
	for _, t := range s.tools {
		result = append(result, t)
	}
	return result
}

// Request represents a JSON-RPC style request with a method and optional params.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Response represents a JSON-RPC style response.
type Response struct {
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// HandleRequest routes a request to the appropriate handler based on method.
func (s *Server) HandleRequest(req *Request) *Response {
	switch req.Method {
	case "tools/list":
		return &Response{Result: s.ListTools()}
	case "tools/call":
		return s.handleToolsCall(req.Params)
	default:
		return &Response{Error: fmt.Sprintf("unknown method: %s", req.Method)}
	}
}

// toolsCallParams holds the parameters for a tools/call request.
type toolsCallParams struct {
	Name   string          `json:"name"`
	Params json.RawMessage `json:"params"`
}

func (s *Server) handleToolsCall(params json.RawMessage) *Response {
	var p toolsCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return &Response{Error: fmt.Sprintf("invalid params: %v", err)}
	}
	t, ok := s.tools[p.Name]
	if !ok {
		return &Response{Error: fmt.Sprintf("tool not found: %s", p.Name)}
	}
	result, err := t.Handler(p.Params)
	if err != nil {
		return &Response{Error: fmt.Sprintf("handler error: %v", err)}
	}
	return &Response{Result: json.RawMessage(result)}
}
