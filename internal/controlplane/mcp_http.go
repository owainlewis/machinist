package controlplane

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

const (
	mcpDefaultProtocolVersion = "2025-06-18"
	mcpDefaultJobLimit        = 50
	mcpDefaultOutputBytes     = 32 << 10
	mcpMaxOutputBytes         = 128 << 10
	mcpMaxOutputPages         = 64

	jsonRPCParseError     = -32700
	jsonRPCInvalidRequest = -32600
	jsonRPCMethodNotFound = -32601
	jsonRPCInvalidParams  = -32602
	jsonRPCInternalError  = -32603
)

var mcpSupportedProtocolVersions = []string{
	"2025-11-25",
	"2025-06-18",
	"2025-03-26",
	"2024-11-05",
}

type jsonRPCRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  any              `json:"result,omitempty"`
	Error   *jsonRPCError    `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpTool struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	InputSchema map[string]any     `json:"inputSchema"`
	Annotations *mcpToolAnnotation `json:"annotations,omitempty"`
	scope       string
}

type mcpToolAnnotation struct {
	ReadOnlyHint    bool `json:"readOnlyHint,omitempty"`
	DestructiveHint bool `json:"destructiveHint,omitempty"`
	IdempotentHint  bool `json:"idempotentHint,omitempty"`
}

type mcpCallResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var mcpTools = []mcpTool{
	{
		Name:        "list_jobs",
		Description: "List Machinist jobs, newest first. Optionally filter by repository, state, source, or limit.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":   map[string]any{"type": "string", "description": "Filter by repository in owner/name format."},
				"state":  map[string]any{"type": "string", "description": "Filter by job state: queued, running, done, failed, or cancelled."},
				"source": map[string]any{"type": "string", "description": "Filter by job source (e.g. web, api, mcp, schedule, linear)."},
				"limit":  map[string]any{"type": "integer", "description": "Maximum number of jobs to return (1-200, default 50).", "minimum": 1, "maximum": tickets.MaxTicketLimit},
			},
			"additionalProperties": false,
		},
		Annotations: &mcpToolAnnotation{ReadOnlyHint: true},
		scope:       tickets.ScopeRead,
	},
	{
		Name:        "list_automations",
		Description: "List configured Machinist automations with their schedule, next due time, and latest job.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		Annotations: &mcpToolAnnotation{ReadOnlyHint: true},
		scope:       tickets.ScopeRead,
	},
	{
		Name:        "run_job",
		Description: "Start a Machinist job either by automation name, or as a custom job with a connected repository and prompt.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"automation": map[string]any{"type": "string", "description": "Name of an automation to run now. Mutually exclusive with repo/prompt."},
				"repo":       map[string]any{"type": "string", "description": "Connected repository (owner/name) for a custom job."},
				"prompt":     map[string]any{"type": "string", "description": "Work instructions for a custom job."},
				"preset":     map[string]any{"type": "string", "description": "Optional factory preset name for a custom job (defaults to the configured default preset)."},
				"key":        map[string]any{"type": "string", "description": "Optional idempotency key (at most 200 bytes) for a custom job. Reusing the same key returns the existing job."},
			},
			"additionalProperties": false,
		},
		Annotations: &mcpToolAnnotation{},
		scope:       tickets.ScopeRun,
	},
	{
		Name:        "get_job",
		Description: "Get a job's current state, pipeline steps, latest agent action, pull request link, and run summary.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Job ID (e.g. t_...)."},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
		Annotations: &mcpToolAnnotation{ReadOnlyHint: true},
		scope:       tickets.ScopeRead,
	},
	{
		Name:        "get_job_output",
		Description: "Read decoded output from a job's latest run (or a specific run or step). Pass cursor from the previous response as after to follow new output.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":        map[string]any{"type": "string", "description": "Job ID (e.g. t_...)."},
				"step":      map[string]any{"type": "string", "description": "Optional step name to filter output to."},
				"after":     map[string]any{"type": "integer", "description": "Event sequence cursor; return only output after this sequence.", "minimum": 0},
				"run_id":    map[string]any{"type": "string", "description": "Optional run ID (defaults to the job's latest run)."},
				"max_bytes": map[string]any{"type": "integer", "description": "Maximum bytes of output to return (default 32768, max 131072).", "minimum": 1, "maximum": mcpMaxOutputBytes},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
		Annotations: &mcpToolAnnotation{ReadOnlyHint: true},
		scope:       tickets.ScopeRead,
	},
	{
		Name:        "cancel_job",
		Description: "Cancel an active (queued or running) job. Its pull request stays open.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Job ID to cancel (e.g. t_...)."},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
		Annotations: &mcpToolAnnotation{DestructiveHint: true},
		scope:       tickets.ScopeRun,
	},
}

func findMCPTool(name string) (mcpTool, bool) {
	for _, tool := range mcpTools {
		if tool.Name == name {
			return tool, true
		}
	}
	return mcpTool{}, false
}

// handleMCP serves the MCP Streamable HTTP endpoint (JSON-RPC 2.0 over POST /mcp)
// using Machinist's bearer tokens and token scopes.
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, errors.New("a bearer token is required"))
		return
	}
	auth, err := s.store.Authenticate(r.Context(), token)
	if errors.Is(err, tickets.ErrNotFound) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, errors.New("invalid token"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !tickets.AllowsScope(auth.Role, auth.Scope, tickets.ScopeRead) {
		writeError(w, http.StatusForbidden, fmt.Errorf("token %q is a %s token and cannot use MCP", auth.Name, auth.Role))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, errors.New("MCP endpoint only accepts POST"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonRPCErrorResponse(nil, jsonRPCParseError, fmt.Sprintf("read request body: %v", err)))
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		writeJSON(w, http.StatusBadRequest, jsonRPCErrorResponse(nil, jsonRPCParseError, "empty JSON-RPC request"))
		return
	}

	if trimmed[0] == '[' {
		var rawBatch []json.RawMessage
		if err := json.Unmarshal(trimmed, &rawBatch); err != nil {
			writeJSON(w, http.StatusBadRequest, jsonRPCErrorResponse(nil, jsonRPCParseError, fmt.Sprintf("invalid JSON: %v", err)))
			return
		}
		if len(rawBatch) == 0 {
			writeJSON(w, http.StatusBadRequest, jsonRPCErrorResponse(nil, jsonRPCInvalidRequest, "empty JSON-RPC batch"))
			return
		}
		responses := make([]jsonRPCResponse, 0, len(rawBatch))
		for _, raw := range rawBatch {
			if response, ok := s.dispatchMCPRaw(r.Context(), auth, raw); ok {
				responses = append(responses, response)
			}
		}
		if len(responses) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, responses)
		return
	}

	response, ok := s.dispatchMCPRaw(r.Context(), auth, trimmed)
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func jsonRPCErrorResponse(id *json.RawMessage, code int, message string) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonRPCError{Code: code, Message: message},
	}
}

func (s *Server) dispatchMCPRaw(ctx context.Context, auth tickets.TokenAuth, raw []byte) (jsonRPCResponse, bool) {
	var request jsonRPCRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return jsonRPCErrorResponse(nil, jsonRPCParseError, fmt.Sprintf("invalid JSON: %v", err)), true
	}
	if request.JSONRPC != "2.0" || request.Method == "" {
		return jsonRPCErrorResponse(request.ID, jsonRPCInvalidRequest, "jsonrpc must be \"2.0\" and method is required"), true
	}
	// JSON-RPC notifications omit "id" and receive no response body.
	if request.ID == nil {
		return jsonRPCResponse{}, false
	}
	return s.dispatchMCPRequest(ctx, auth, request), true
}

func (s *Server) dispatchMCPRequest(ctx context.Context, auth tickets.TokenAuth, request jsonRPCRequest) jsonRPCResponse {
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(request.Params) > 0 && string(bytes.TrimSpace(request.Params)) != "null" {
			if err := json.Unmarshal(request.Params, &params); err != nil {
				return jsonRPCErrorResponse(request.ID, jsonRPCInvalidParams, fmt.Sprintf("invalid initialize params: %v", err))
			}
		}
		version := mcpDefaultProtocolVersion
		if slices.Contains(mcpSupportedProtocolVersions, params.ProtocolVersion) {
			version = params.ProtocolVersion
		}
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result: map[string]any{
				"protocolVersion": version,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{
					"name":    "machinist",
					"version": s.version,
				},
			},
		}
	case "ping":
		return jsonRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{}}
	case "tools/list":
		return jsonRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"tools": mcpTools}}
	case "tools/call":
		return s.handleMCPToolCall(ctx, auth, request)
	default:
		return jsonRPCErrorResponse(request.ID, jsonRPCMethodNotFound, fmt.Sprintf("method %q not found", request.Method))
	}
}

func (s *Server) handleMCPToolCall(ctx context.Context, auth tickets.TokenAuth, request jsonRPCRequest) jsonRPCResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil || params.Name == "" {
		return jsonRPCErrorResponse(request.ID, jsonRPCInvalidParams, "tools/call requires a tool name")
	}
	tool, ok := findMCPTool(params.Name)
	if !ok {
		return jsonRPCErrorResponse(request.ID, jsonRPCInvalidParams, fmt.Sprintf("unknown tool %q", params.Name))
	}
	if !tickets.AllowsScope(auth.Role, auth.Scope, tool.scope) {
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result:  mcpToolError(fmt.Errorf("token %q has scope %q and cannot call %q; scope %q is required", auth.Name, auth.Scope, tool.Name, tool.scope)),
		}
	}

	args := bytes.TrimSpace(params.Arguments)
	if len(args) == 0 || string(args) == "null" {
		args = []byte("{}")
	}

	var result any
	var err error
	switch tool.Name {
	case "list_jobs":
		result, err = s.mcpListJobs(ctx, args)
	case "list_automations":
		result, err = s.mcpListAutomations(ctx, args)
	case "run_job":
		result, err = s.mcpRunJob(ctx, auth, args)
	case "get_job":
		result, err = s.mcpGetJob(ctx, args)
	case "get_job_output":
		result, err = s.mcpGetJobOutput(ctx, args)
	case "cancel_job":
		result, err = s.mcpCancelJob(ctx, auth, args)
	default:
		return jsonRPCErrorResponse(request.ID, jsonRPCInvalidParams, fmt.Sprintf("unknown tool %q", tool.Name))
	}
	if err != nil {
		return jsonRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: mcpToolError(err)}
	}
	return jsonRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: mcpToolJSON(result)}
}

func mcpToolError(err error) mcpCallResult {
	return mcpCallResult{
		Content: []mcpContent{{Type: "text", Text: err.Error()}},
		IsError: true,
	}
}

func mcpToolJSON(value any) mcpCallResult {
	encoded, err := json.Marshal(value)
	if err != nil {
		return mcpToolError(err)
	}
	return mcpCallResult{
		Content: []mcpContent{{Type: "text", Text: string(encoded)}},
	}
}

func decodeMCPArgs(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

func githubPRURL(repo string, number int) string {
	if repo == "" || number <= 0 {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/pull/%d", repo, number)
}

type mcpJobSummary struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Repo        string    `json:"repo"`
	Preset      string    `json:"preset"`
	Source      string    `json:"source"`
	SourceURL   string    `json:"source_url,omitempty"`
	State       string    `json:"state"`
	CurrentStep string    `json:"current_step,omitempty"`
	PRNumber    int       `json:"pr_number,omitempty"`
	PRState     string    `json:"pr_state,omitempty"`
	PRURL       string    `json:"pr_url,omitempty"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type mcpJobStep struct {
	Name           string `json:"name"`
	State          string `json:"state"`
	DurationMillis int64  `json:"duration_millis,omitempty"`
	Error          string `json:"error,omitempty"`
}

type mcpJobDetail struct {
	ID            string       `json:"id"`
	Title         string       `json:"title"`
	Repo          string       `json:"repo"`
	Preset        string       `json:"preset"`
	Source        string       `json:"source"`
	SourceURL     string       `json:"source_url,omitempty"`
	State         string       `json:"state"`
	CurrentStep   string       `json:"current_step,omitempty"`
	LatestAction  string       `json:"latest_action,omitempty"`
	Steps         []mcpJobStep `json:"steps"`
	PRNumber      int          `json:"pr_number,omitempty"`
	PRState       string       `json:"pr_state,omitempty"`
	PRURL         string       `json:"pr_url,omitempty"`
	LatestRunID   string       `json:"latest_run_id,omitempty"`
	Summary       string       `json:"summary,omitempty"`
	LatestMessage string       `json:"latest_message,omitempty"`
	Tokens        int64        `json:"tokens"`
	CostUSD       *float64     `json:"cost_usd"`
	CreatedBy     string       `json:"created_by"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

type mcpRunJobResult struct {
	mcpJobDetail
	Created bool `json:"created"`
}

type mcpAutomationJob struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	PRNumber  int       `json:"pr_number,omitempty"`
	PRState   string    `json:"pr_state,omitempty"`
	PRURL     string    `json:"pr_url,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type mcpAutomation struct {
	Name     string            `json:"name"`
	Repo     string            `json:"repo"`
	Preset   string            `json:"preset"`
	Schedule string            `json:"schedule"`
	Timezone string            `json:"timezone"`
	NextDue  time.Time         `json:"next_due"`
	LastJob  *mcpAutomationJob `json:"last_job,omitempty"`
}

type mcpStepOutput struct {
	Name   string `json:"name"`
	Output string `json:"output"`
}

type mcpJobOutput struct {
	JobID     string          `json:"job_id"`
	RunID     string          `json:"run_id,omitempty"`
	State     string          `json:"state"`
	RunStatus string          `json:"run_status,omitempty"`
	Cursor    int64           `json:"cursor"`
	HasMore   bool            `json:"has_more"`
	Truncated bool            `json:"truncated,omitempty"`
	Output    string          `json:"output"`
	Steps     []mcpStepOutput `json:"steps,omitempty"`
}

func (s *Server) mcpListJobs(ctx context.Context, raw []byte) (any, error) {
	var args struct {
		Repo   string `json:"repo"`
		State  string `json:"state"`
		Source string `json:"source"`
		Limit  int    `json:"limit"`
	}
	if err := decodeMCPArgs(raw, &args); err != nil {
		return nil, err
	}
	limit := args.Limit
	if limit == 0 {
		limit = mcpDefaultJobLimit
	}
	if limit < 1 || limit > tickets.MaxTicketLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", tickets.MaxTicketLimit)
	}
	filter := tickets.TicketFilter{
		Repo:   strings.TrimSpace(args.Repo),
		Source: strings.TrimSpace(args.Source),
		Limit:  limit,
	}
	if state := strings.TrimSpace(args.State); state != "" {
		switch state {
		case JobQueued, JobRunning, JobFailed, JobCancelled, tickets.StateWorking, tickets.StatePRReady:
			filter.States = []string{state}
		case JobDone:
			filter.States = []string{tickets.StateDone, tickets.StatePRReady}
		default:
			return nil, fmt.Errorf("unknown state %q; must be queued, running, done, failed, or cancelled", state)
		}
	}
	page, err := s.store.ListTicketsPage(ctx, filter)
	if err != nil {
		return nil, err
	}
	jobs := make([]mcpJobSummary, 0, len(page.Tickets))
	for _, ticket := range page.Tickets {
		runs, err := s.store.Runs(ctx, ticket.ID)
		if err != nil {
			return nil, err
		}
		state := jobState(ticket, runs)
		var currentStep string
		if state == JobRunning {
			if currentStep, err = s.store.CurrentStep(ctx, ticket.ID); err != nil {
				return nil, err
			}
		}
		jobs = append(jobs, mcpJobSummary{
			ID:          ticket.ID,
			Title:       ticket.Title,
			Repo:        ticket.Repo,
			Preset:      ticket.Agent,
			Source:      ticket.Origin,
			SourceURL:   ticket.SourceURL,
			State:       state,
			CurrentStep: currentStep,
			PRNumber:    ticket.PRNumber,
			PRState:     ticket.PRState,
			PRURL:       githubPRURL(ticket.Repo, ticket.PRNumber),
			CreatedBy:   ticket.CreatedBy,
			CreatedAt:   ticket.CreatedAt,
			UpdatedAt:   ticket.UpdatedAt,
		})
	}
	return map[string]any{
		"jobs":     jobs,
		"total":    page.Total,
		"has_more": page.HasMore,
	}, nil
}

func (s *Server) mcpListAutomations(ctx context.Context, raw []byte) (any, error) {
	var args struct{}
	if err := decodeMCPArgs(raw, &args); err != nil {
		return nil, err
	}
	statuses, err := s.schedule.Status(ctx)
	if err != nil {
		return nil, err
	}
	automations := make([]mcpAutomation, 0, len(statuses))
	for _, status := range statuses {
		item := mcpAutomation{
			Name:     status.Name,
			Repo:     status.Repo,
			Preset:   status.Agent,
			Schedule: status.Schedule,
			Timezone: status.Timezone,
			NextDue:  status.NextDue,
		}
		if status.LastTicket != nil {
			runs, err := s.store.Runs(ctx, status.LastTicket.ID)
			if err != nil {
				return nil, err
			}
			item.LastJob = &mcpAutomationJob{
				ID:        status.LastTicket.ID,
				State:     jobState(status.LastTicket.Ticket, runs),
				PRNumber:  status.LastTicket.PRNumber,
				PRState:   status.LastTicket.PRState,
				PRURL:     githubPRURL(status.LastTicket.Repo, status.LastTicket.PRNumber),
				Outcome:   status.LastTicket.Outcome,
				CreatedAt: status.LastTicket.CreatedAt,
				UpdatedAt: status.LastTicket.UpdatedAt,
			}
		}
		automations = append(automations, item)
	}
	return map[string]any{"automations": automations}, nil
}

func newMCPKey() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "mcp-" + hex.EncodeToString(buf), nil
}

func (s *Server) mcpRunJob(ctx context.Context, auth tickets.TokenAuth, raw []byte) (any, error) {
	var args struct {
		Automation string `json:"automation"`
		Repo       string `json:"repo"`
		Prompt     string `json:"prompt"`
		Preset     string `json:"preset"`
		Key        string `json:"key"`
	}
	if err := decodeMCPArgs(raw, &args); err != nil {
		return nil, err
	}
	automation := strings.TrimSpace(args.Automation)
	repo := strings.TrimSpace(args.Repo)
	prompt := strings.TrimSpace(args.Prompt)
	preset := strings.TrimSpace(args.Preset)
	key := strings.TrimSpace(args.Key)

	var ticket tickets.Ticket
	var created bool
	var err error
	if automation != "" {
		if repo != "" || prompt != "" || preset != "" || key != "" {
			return nil, errors.New("specify either automation, or repo and prompt, not both")
		}
		ticket, created, err = s.schedule.RunNow(ctx, automation, auth.Name)
		if errors.Is(err, tickets.ErrNotFound) {
			return nil, fmt.Errorf("automation %q not found", automation)
		}
		if err != nil {
			return nil, err
		}
	} else {
		if repo == "" || prompt == "" {
			return nil, errors.New("provide either automation, or both repo and prompt")
		}
		if key == "" {
			if key, err = newMCPKey(); err != nil {
				return nil, err
			}
		}
		ticket, created, err = s.intake.Run(ctx, sources.RunRequest{
			Key:    key,
			Preset: preset,
			Vars:   map[string]string{"repo": repo},
			Prompt: prompt,
			Origin: tickets.OriginMCP,
		}, auth.Name)
		if err != nil {
			return nil, err
		}
	}

	detail, err := s.mcpJobDetail(ctx, ticket)
	if err != nil {
		return nil, err
	}
	return mcpRunJobResult{mcpJobDetail: detail, Created: created}, nil
}

func (s *Server) mcpGetJob(ctx context.Context, raw []byte) (any, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := decodeMCPArgs(raw, &args); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(args.ID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	ticket, err := s.store.Ticket(ctx, id)
	if errors.Is(err, tickets.ErrNotFound) {
		return nil, fmt.Errorf("job %q not found", id)
	}
	if err != nil {
		return nil, err
	}
	return s.mcpJobDetail(ctx, ticket)
}

func (s *Server) mcpCancelJob(ctx context.Context, auth tickets.TokenAuth, raw []byte) (any, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := decodeMCPArgs(raw, &args); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(args.ID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	ticket, err := s.store.Cancel(ctx, id, auth.Name)
	if errors.Is(err, tickets.ErrNotFound) {
		return nil, fmt.Errorf("job %q not found", id)
	}
	if errors.Is(err, tickets.ErrTicketEnded) {
		return nil, fmt.Errorf("job %q has already ended", id)
	}
	if err != nil {
		return nil, err
	}
	return s.mcpJobDetail(ctx, ticket)
}

func (s *Server) mcpJobDetail(ctx context.Context, ticket tickets.Ticket) (mcpJobDetail, error) {
	runs, err := s.store.Runs(ctx, ticket.ID)
	if err != nil {
		return mcpJobDetail{}, err
	}
	latestMessage, err := s.store.LatestMessage(ctx, ticket.ID)
	if err != nil {
		return mcpJobDetail{}, err
	}
	state := jobState(ticket, runs)
	detail := mcpJobDetail{
		ID:            ticket.ID,
		Title:         ticket.Title,
		Repo:          ticket.Repo,
		Preset:        ticket.Agent,
		Source:        ticket.Origin,
		SourceURL:     ticket.SourceURL,
		State:         state,
		PRNumber:      ticket.PRNumber,
		PRState:       ticket.PRState,
		PRURL:         githubPRURL(ticket.Repo, ticket.PRNumber),
		LatestMessage: latestMessage,
		CreatedBy:     ticket.CreatedBy,
		CreatedAt:     ticket.CreatedAt,
		UpdatedAt:     ticket.UpdatedAt,
	}
	for _, run := range runs {
		if run.Tokens != nil {
			detail.Tokens += *run.Tokens
		}
		if run.Cost != nil {
			total := *run.Cost
			if detail.CostUSD != nil {
				total += *detail.CostUSD
			}
			detail.CostUSD = &total
		}
	}

	var latestRun *tickets.Run
	var startedSteps []string
	if len(runs) > 0 {
		latestRun = &runs[len(runs)-1]
		detail.LatestRunID = latestRun.ID
		if latestRun.Report != nil {
			detail.Summary = latestRun.Report.Summary
		}
		progress, err := s.store.RunProgress(ctx, latestRun.ID)
		if err != nil {
			return mcpJobDetail{}, err
		}
		detail.LatestAction = progress.Action
		if state == JobRunning {
			detail.CurrentStep = progress.Step
		}
		if len(latestRun.Stages) == 0 {
			startedSteps, err = s.store.RunStartedSteps(ctx, latestRun.ID)
			if err != nil {
				return mcpJobDetail{}, err
			}
			if state == JobRunning && detail.CurrentStep == "" && len(startedSteps) > 0 {
				detail.CurrentStep = startedSteps[len(startedSteps)-1]
			}
		}
	}
	detail.Steps = buildMCPSteps(s.presetSteps(ticket.Agent), latestRun, startedSteps)
	return detail, nil
}

// buildMCPSteps merges the preset's planned steps with the latest run's
// recorded stages (once finished) or started step events (while running).
func buildMCPSteps(preset []stepView, latestRun *tickets.Run, started []string) []mcpJobStep {
	if latestRun == nil {
		steps := make([]mcpJobStep, 0, len(preset))
		for _, step := range preset {
			steps = append(steps, mcpJobStep{Name: step.Name, State: "pending"})
		}
		return steps
	}

	going := latestRun.Status == tickets.RunRunning || latestRun.Status == tickets.RunQueued
	ran := make([]mcpJobStep, 0)
	if len(latestRun.Stages) > 0 {
		for _, stage := range latestRun.Stages {
			ran = append(ran, mcpJobStep{
				Name:           stage.Name,
				State:          stage.State,
				DurationMillis: stage.DurationMillis,
				Error:          stage.Error,
			})
		}
	} else {
		for index, name := range started {
			stepState := "unknown"
			if going {
				if index == len(started)-1 {
					stepState = "running"
				} else {
					stepState = "succeeded"
				}
			}
			ran = append(ran, mcpJobStep{Name: name, State: stepState})
		}
	}

	planned := make(map[string]bool, len(preset))
	for _, step := range preset {
		planned[step.Name] = true
	}
	seen := make(map[string]mcpJobStep, len(ran))
	allInPreset := true
	for _, step := range ran {
		seen[step.Name] = step
		if !planned[step.Name] {
			allInPreset = false
		}
	}

	if len(preset) == 0 || (len(ran) > 0 && !allInPreset) {
		return ran
	}
	unreached := "skipped"
	if going {
		unreached = "pending"
	}
	steps := make([]mcpJobStep, 0, len(preset))
	for _, step := range preset {
		if existing, ok := seen[step.Name]; ok {
			steps = append(steps, existing)
		} else {
			steps = append(steps, mcpJobStep{Name: step.Name, State: unreached})
		}
	}
	return steps
}

func (s *Server) mcpGetJobOutput(ctx context.Context, raw []byte) (any, error) {
	var args struct {
		ID       string `json:"id"`
		Step     string `json:"step"`
		After    int64  `json:"after"`
		RunID    string `json:"run_id"`
		MaxBytes int    `json:"max_bytes"`
	}
	if err := decodeMCPArgs(raw, &args); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(args.ID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	if args.After < 0 {
		return nil, errors.New("after must be a non-negative integer")
	}
	maxBytes := args.MaxBytes
	if maxBytes == 0 {
		maxBytes = mcpDefaultOutputBytes
	}
	if maxBytes < 1 || maxBytes > mcpMaxOutputBytes {
		return nil, fmt.Errorf("max_bytes must be between 1 and %d", mcpMaxOutputBytes)
	}

	ticket, err := s.store.Ticket(ctx, id)
	if errors.Is(err, tickets.ErrNotFound) {
		return nil, fmt.Errorf("job %q not found", id)
	}
	if err != nil {
		return nil, err
	}
	runs, err := s.store.Runs(ctx, id)
	if err != nil {
		return nil, err
	}
	state := jobState(ticket, runs)
	if len(runs) == 0 {
		return mcpJobOutput{JobID: ticket.ID, State: state, Cursor: args.After}, nil
	}

	var run *tickets.Run
	if runID := strings.TrimSpace(args.RunID); runID != "" {
		for index := range runs {
			if runs[index].ID == runID {
				run = &runs[index]
				break
			}
		}
		if run == nil {
			return nil, fmt.Errorf("run %q not found for job %q", runID, ticket.ID)
		}
	} else {
		run = &runs[len(runs)-1]
	}

	stepFilter := strings.TrimSpace(args.Step)
	currentStep, err := s.store.RunStepAt(ctx, run.ID, args.After)
	if err != nil {
		return nil, err
	}

	cursor := args.After
	var combined strings.Builder
	var steps []mcpStepOutput
	var activeStep *mcpStepOutput
	hasMore := false
	truncated := false

	appendText := func(stepName, text string) {
		combined.WriteString(text)
		if stepName == "" {
			return
		}
		if activeStep == nil || activeStep.Name != stepName {
			steps = append(steps, mcpStepOutput{Name: stepName})
			activeStep = &steps[len(steps)-1]
		}
		activeStep.Output += text
	}

	for pageNum := 0; pageNum < mcpMaxOutputPages; pageNum++ {
		page, err := s.store.RunEvents(ctx, run.ID, cursor)
		if err != nil {
			return nil, err
		}
		if len(page.Events) == 0 {
			break
		}
		budgetReached := false
		for index, rawEvent := range page.Events {
			var event protocol.RunEvent
			if json.Unmarshal(rawEvent, &event) != nil {
				continue
			}
			if name, ok := tickets.EventStepName(event); ok {
				currentStep = name
				cursor = event.Sequence
				continue
			}
			var chunk string
			switch event.Type {
			case "process.output":
				if event.Encoding == "base64" {
					decoded, err := base64.StdEncoding.DecodeString(event.Data)
					if err == nil {
						chunk = string(decoded)
					}
				}
			case "process.output_truncated":
				if event.Message != "" {
					chunk = "\n[" + event.Message + "]\n"
				}
			}
			if chunk == "" || (stepFilter != "" && currentStep != stepFilter) {
				cursor = event.Sequence
				continue
			}
			if combined.Len()+len(chunk) > maxBytes {
				if combined.Len() > 0 {
					hasMore = true
					budgetReached = true
					break
				}
				chunk = truncateUTF8(chunk, maxBytes)
				truncated = true
				appendText(currentStep, chunk)
				cursor = event.Sequence
				if index < len(page.Events)-1 || len(page.Events) == protocol.MaxRunEventBatch {
					hasMore = true
				}
				budgetReached = true
				break
			}
			appendText(currentStep, chunk)
			cursor = event.Sequence
		}
		if budgetReached {
			break
		}
		if len(page.Events) < protocol.MaxRunEventBatch {
			break
		}
		if pageNum == mcpMaxOutputPages-1 {
			hasMore = true
		}
	}

	return mcpJobOutput{
		JobID:     ticket.ID,
		RunID:     run.ID,
		State:     state,
		RunStatus: run.Status,
		Cursor:    cursor,
		HasMore:   hasMore,
		Truncated: truncated,
		Output:    combined.String(),
		Steps:     steps,
	}, nil
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
