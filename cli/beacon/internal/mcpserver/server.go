// Package mcpserver implements the small local MCP server surface Beacon needs
// for activity querying. It intentionally supports the server methods exercised
// by current desktop clients and tests: initialize, tools/list, tools/call, and
// notifications without responses.
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/activity"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/learning"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

type Options struct {
	LogPath string
}

type Server struct {
	logPath string
	tools   map[string]Tool
	order   []string
}

type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
	handler     func(context.Context, map[string]interface{}) (interface{}, error)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type callToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

type toolResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type memorySearchResult struct {
	Memories []memorySummary `json:"memories"`
	Returned int             `json:"returned"`
	Limit    int             `json:"limit"`
}

type memoryContextResult struct {
	Context  []memorySummary `json:"context"`
	Returned int             `json:"returned"`
	Limit    int             `json:"limit"`
}

type memorySummary struct {
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	Title         string   `json:"title"`
	Applicability string   `json:"applicability,omitempty"`
	Body          string   `json:"body,omitempty"`
	Tags          []string `json:"tags,omitempty"`
}

func New(opts Options) *Server {
	server := &Server{
		logPath: opts.LogPath,
		tools:   map[string]Tool{},
	}
	server.registerTools()
	return server
}

func ValidateTransport(transport string) error {
	switch transport {
	case "", TransportStdio, TransportHTTP:
		return nil
	default:
		return fmt.Errorf("transport must be %q or %q", TransportStdio, TransportHTTP)
	}
}

func (s *Server) ToolNames() []string {
	names := make([]string, len(s.order))
	copy(names, s.order)
	return names
}

func (s *Server) HasExpectedTools() error {
	expected := []string{"search_activity", "summarize_activity", "get_activity_event", "list_activity_filters", "search_memory", "get_memory", "get_memory_context"}
	for _, name := range expected {
		if _, ok := s.tools[name]; !ok {
			return fmt.Errorf("missing MCP tool %q", name)
		}
	}
	return nil
}

func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		response, ok := s.handle(ctx, scanner.Bytes())
		if !ok {
			continue
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func (s *Server) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeHTTPJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16*1024*1024))
		if err != nil {
			writeHTTPError(w, http.StatusBadRequest, err.Error())
			return
		}
		response, ok := s.handle(r.Context(), body)
		if !ok {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeHTTPJSON(w, response)
	})
	return mux
}

func (s *Server) handle(ctx context.Context, data []byte) (rpcResponse, bool) {
	var request rpcRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return errorResponse(nil, -32700, "parse error"), true
	}
	id, hasID := requestID(request.ID)
	if !hasID && strings.HasPrefix(request.Method, "notifications/") {
		return rpcResponse{}, false
	}
	if !hasID {
		return rpcResponse{}, false
	}
	result, err := s.dispatch(ctx, request)
	if err != nil {
		return errorResponse(id, -32000, err.Error()), true
	}
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}, true
}

func (s *Server) dispatch(ctx context.Context, request rpcRequest) (interface{}, error) {
	switch request.Method {
	case "initialize":
		return map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]string{
				"name":    "beacon",
				"version": version.GetVersion(),
			},
		}, nil
	case "tools/list":
		return map[string]interface{}{"tools": s.listTools()}, nil
	case "tools/call":
		var params callToolParams
		if len(request.Params) > 0 {
			if err := json.Unmarshal(request.Params, &params); err != nil {
				return nil, fmt.Errorf("invalid tool call params: %w", err)
			}
		}
		return s.callTool(ctx, params)
	default:
		return nil, fmt.Errorf("unsupported MCP method %q", request.Method)
	}
}

func (s *Server) listTools() []Tool {
	tools := make([]Tool, 0, len(s.order))
	for _, name := range s.order {
		tool := s.tools[name]
		tool.handler = nil
		tools = append(tools, tool)
	}
	return tools
}

func (s *Server) callTool(ctx context.Context, params callToolParams) (toolResult, error) {
	tool, ok := s.tools[params.Name]
	if !ok {
		return toolResult{}, fmt.Errorf("unknown tool %q", params.Name)
	}
	value, err := tool.handler(ctx, params.Arguments)
	if err != nil {
		return toolResult{IsError: true, Content: []textContent{{Type: "text", Text: err.Error()}}}, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return toolResult{}, err
	}
	return toolResult{Content: []textContent{{Type: "text", Text: string(data)}}}, nil
}

func (s *Server) register(tool Tool) {
	s.tools[tool.Name] = tool
	s.order = append(s.order, tool.Name)
}

func (s *Server) registerTools() {
	s.register(Tool{
		Name:        "search_activity",
		Description: "Search local Beacon agent activity logs and return compact event summaries.",
		InputSchema: querySchema("Search filters for local activity events."),
		handler: func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			query, err := parseQuery(s.logPath, args)
			if err != nil {
				return nil, err
			}
			return activity.Search(query)
		},
	})
	s.register(Tool{
		Name:        "summarize_activity",
		Description: "Summarize local Beacon agent activity over a time window and optional filters.",
		InputSchema: querySchema("Summary filters for local activity events."),
		handler: func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			query, err := parseQuery(s.logPath, args)
			if err != nil {
				return nil, err
			}
			return activity.Summarize(query)
		},
	})
	s.register(Tool{
		Name:        "get_activity_event",
		Description: "Fetch one compact Beacon activity log entry by ID returned from search_activity.",
		InputSchema: objectSchema(map[string]interface{}{
			"id": map[string]interface{}{"type": "string", "description": "Event ID, such as line-12 or archive-1-line-4."},
		}, []string{"id"}),
		handler: func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			id, _ := args["id"].(string)
			if strings.TrimSpace(id) == "" {
				return nil, errors.New("id is required")
			}
			return activity.GetEvent(s.logPath, id)
		},
	})
	s.register(Tool{
		Name:        "list_activity_filters",
		Description: "List common filter values found in local Beacon activity logs.",
		InputSchema: querySchema("Filters for selecting the activity window to inspect."),
		handler: func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			query, err := parseQuery(s.logPath, args)
			if err != nil {
				return nil, err
			}
			return activity.ListFilters(query)
		},
	})
	s.register(Tool{
		Name:        "search_memory",
		Description: "Search approved Beacon project memory and return compact summaries.",
		InputSchema: memoryQuerySchema("Search filters for approved project memory."),
		handler: func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			query, err := s.parseMemoryQuery(args)
			if err != nil {
				return nil, err
			}
			memories, err := s.memoryStore().ListMemories(query)
			if err != nil {
				return nil, err
			}
			limit := normalizeMemoryLimit(query.Limit)
			if len(memories) > limit {
				memories = memories[:limit]
			}
			return memorySearchResult{Memories: memorySummaries(memories), Returned: len(memories), Limit: limit}, nil
		},
	})
	s.register(Tool{
		Name:        "get_memory",
		Description: "Fetch one approved Beacon memory item by ID.",
		InputSchema: objectSchema(map[string]interface{}{
			"id": map[string]interface{}{"type": "string", "description": "Approved memory ID."},
		}, []string{"id"}),
		handler: func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			id := stringArg(args, "id")
			if id == "" {
				return nil, errors.New("id is required")
			}
			memory, ok, err := s.memoryStore().GetMemory(id)
			if err != nil {
				return nil, err
			}
			if !ok || memory.SupersededBy != "" {
				return nil, fmt.Errorf("memory not found: %s", id)
			}
			return memory, nil
		},
	})
	s.register(Tool{
		Name:        "get_memory_context",
		Description: "Return a small approved-memory context set for the current task.",
		InputSchema: memoryQuerySchema("Task and project filters for approved memory context."),
		handler: func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			query, err := s.parseMemoryQuery(args)
			if err != nil {
				return nil, err
			}
			if query.Limit <= 0 || query.Limit > 5 {
				query.Limit = 5
			}
			memories, err := s.memoryStore().ListMemories(query)
			if err != nil {
				return nil, err
			}
			if len(memories) > query.Limit {
				memories = memories[:query.Limit]
			}
			return memoryContextResult{Context: memorySummaries(memories), Returned: len(memories), Limit: query.Limit}, nil
		},
	})
}

func (s *Server) memoryStore() *learning.Store {
	return learning.Open(learning.PathForRuntimeLog(s.logPath))
}

func (s *Server) parseMemoryQuery(args map[string]interface{}) (learning.Query, error) {
	limit := intArg(args, "limit")
	if limit <= 0 {
		limit = 5
	}
	query := learning.Query{
		ProjectID:   stringArg(args, "project_id"),
		ProjectPath: stringArg(args, "project"),
		Q:           firstNonEmpty(stringArg(args, "q"), stringArg(args, "task")),
		Kind:        stringArg(args, "kind"),
		Limit:       normalizeMemoryLimit(limit),
	}
	if query.ProjectID == "" && query.ProjectPath == "" {
		project, err := learning.ResolveProject("")
		if err != nil {
			return learning.Query{}, err
		}
		query.ProjectID = project.ID
	}
	return query, nil
}

func memoryQuerySchema(description string) map[string]interface{} {
	return objectSchema(map[string]interface{}{
		"limit":      map[string]interface{}{"type": "integer", "description": "Maximum number of memories to return."},
		"q":          map[string]interface{}{"type": "string", "description": "Free-text query across approved memory."},
		"task":       map[string]interface{}{"type": "string", "description": "Current task text used to select relevant memory."},
		"kind":       map[string]interface{}{"type": "string", "description": "Memory kind, such as workflow, correction, debugging_pattern, gotcha, or convention."},
		"project":    map[string]interface{}{"type": "string", "description": "Project path for memory scoping."},
		"project_id": map[string]interface{}{"type": "string", "description": "Resolved Beacon project ID for memory scoping."},
	}, nil, description)
}

func memorySummaries(memories []asymptoteobserve.LearningMemoryV1) []memorySummary {
	out := make([]memorySummary, 0, len(memories))
	for _, memory := range memories {
		out = append(out, memorySummary{
			ID:            memory.ID,
			Kind:          memory.Kind,
			Title:         memory.Title,
			Applicability: memory.Applicability,
			Body:          asymptoteobserve.CleanString(memory.Body, 1200, true),
			Tags:          append([]string(nil), memory.Tags...),
		})
	}
	return out
}

func normalizeMemoryLimit(limit int) int {
	if limit <= 0 {
		return 5
	}
	if limit > 20 {
		return 20
	}
	return limit
}

func parseQuery(logPath string, args map[string]interface{}) (activity.Query, error) {
	query := activity.Query{LogPath: logPath}
	if args == nil {
		return query, nil
	}
	query.Limit = intArg(args, "limit")
	query.Q = stringArg(args, "q")
	query.Harness = stringArg(args, "harness")
	query.Model = stringArg(args, "model")
	query.Action = stringArg(args, "action")
	query.Severity = stringArg(args, "severity")
	query.Category = stringArg(args, "category")
	query.Repository = stringArg(args, "repository")
	query.Session = stringArg(args, "session")
	query.File = stringArg(args, "file")
	query.Command = stringArg(args, "command")
	query.MCP = stringArg(args, "mcp")
	query.Approval = stringArg(args, "approval")
	query.Decision = stringArg(args, "decision")
	query.Policy = stringArg(args, "policy")
	query.Review = stringArg(args, "review")
	query.WazuhLevel = stringArg(args, "wazuh_level")
	var err error
	if query.Since, err = timeArg(args, "since"); err != nil {
		return activity.Query{}, err
	}
	if query.Until, err = timeArg(args, "until"); err != nil {
		return activity.Query{}, err
	}
	return query, nil
}

func querySchema(description string) map[string]interface{} {
	return objectSchema(map[string]interface{}{
		"limit":       map[string]interface{}{"type": "integer", "description": "Maximum number of events to return."},
		"since":       map[string]interface{}{"type": "string", "description": "RFC3339 lower time bound, inclusive."},
		"until":       map[string]interface{}{"type": "string", "description": "RFC3339 upper time bound, inclusive."},
		"q":           map[string]interface{}{"type": "string", "description": "Free-text query across structured event fields."},
		"harness":     map[string]interface{}{"type": "string", "description": "Agent harness name, such as claude, cursor, or codex."},
		"model":       map[string]interface{}{"type": "string", "description": "Model name substring."},
		"action":      map[string]interface{}{"type": "string", "description": "Beacon event action."},
		"severity":    map[string]interface{}{"type": "string", "description": "Beacon severity."},
		"category":    map[string]interface{}{"type": "string", "description": "Event category such as prompt, tool, command, file, mcp, or approval."},
		"repository":  map[string]interface{}{"type": "string", "description": "Repository substring."},
		"session":     map[string]interface{}{"type": "string", "description": "Session ID substring."},
		"file":        map[string]interface{}{"type": "string", "description": "File path substring."},
		"command":     map[string]interface{}{"type": "string", "description": "Command or tool command substring."},
		"mcp":         map[string]interface{}{"type": "string", "description": "MCP server or tool substring."},
		"approval":    map[string]interface{}{"type": "string", "description": "Approval decision or reason substring."},
		"decision":    map[string]interface{}{"type": "string", "description": "Approval or policy decision substring."},
		"policy":      map[string]interface{}{"type": "string", "description": "Policy ID, name, decision, or reason substring."},
		"review":      map[string]interface{}{"type": "string", "description": "Set true to return events needing review."},
		"wazuh_level": map[string]interface{}{"type": "string", "description": "Wazuh-compatible severity level."},
	}, nil, description)
}

func objectSchema(properties map[string]interface{}, required []string, descriptions ...string) map[string]interface{} {
	schema := map[string]interface{}{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	if len(descriptions) > 0 && descriptions[0] != "" {
		schema["description"] = descriptions[0]
	}
	return schema
}

func requestID(raw json.RawMessage) (interface{}, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var id interface{}
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, false
	}
	return id, true
}

func errorResponse(id interface{}, code int, message string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

func stringArg(args map[string]interface{}, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func intArg(args map[string]interface{}, key string) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

func timeArg(args map[string]interface{}, key string) (time.Time, error) {
	value := stringArg(args, key)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339: %w", key, err)
	}
	return parsed, nil
}

func writeHTTPJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeHTTPError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
