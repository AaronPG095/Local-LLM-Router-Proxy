package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Server implements a newline-delimited JSON-RPC 2.0 stdio MCP server
// backed by the local LLM Router Proxy HTTP endpoints.
type Server struct {
	routerURL    string
	client       *http.Client
	startMu      sync.Mutex
	startProxyFn func() error
}

// NewServer constructs an MCP Server targeting routerURL (default http://127.0.0.1:8787).
func NewServer(routerURL string) *Server {
	u := strings.TrimRight(strings.TrimSpace(routerURL), "/")
	if u == "" {
		u = "http://127.0.0.1:8787"
	}
	s := &Server{
		routerURL: u,
		client:    &http.Client{Timeout: 65 * time.Second},
	}
	s.startProxyFn = s.defaultStartProxy
	return s
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads JSON-RPC requests line-by-line from r and writes responses to w.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			var req rpcRequest
			if jErr := json.Unmarshal(trimmed, &req); jErr != nil {
				_ = writeJSONLine(w, rpcResponse{
					JSONRPC: "2.0",
					ID:      json.RawMessage("null"),
					Error:   &rpcError{Code: -32700, Message: "parse error"},
				})
			} else if resp := s.handleRequest(context.Background(), req); resp != nil {
				if wErr := writeJSONLine(w, resp); wErr != nil {
					return wErr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

func (s *Server) handleRequest(ctx context.Context, req rpcRequest) *rpcResponse {
	// Notifications have no ID or null ID and must not receive a response.
	if len(req.ID) == 0 || string(req.ID) == "null" || req.Method == "notifications/initialized" {
		return nil
	}

	switch req.Method {
	case "initialize":
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "llm-router-mcp",
					"version": "0.1.0",
				},
			},
		}
	case "ping":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": toolDefinitions(),
			},
		}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return &rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32602, Message: "invalid params"},
			}
		}
		res := s.callTool(ctx, p.Name, p.Arguments)
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: res}
	default:
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
}

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "offload_llm",
			"description": "Offload mechanical (commit_msg, log_trim, classify, summarize) or bounded code/synthesis tasks (pr_summary, explain, draft_tests, boilerplate) to the local LLM Router Proxy (Ollama + L1/L2 cache) to save frontier tokens.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task": map[string]any{
						"type":        "string",
						"enum":        []string{"commit_msg", "log_trim", "classify", "summarize", "pr_summary", "explain", "draft_tests", "boilerplate"},
						"description": "Task category used for X-Router-Task policy routing.",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "User prompt or text payload to process.",
					},
					"system": map[string]any{
						"type":        "string",
						"description": "Optional system instruction.",
					},
					"deterministic": map[string]any{
						"type":        "boolean",
						"description": "Set temperature=0 for L1 exact caching (default true).",
					},
					"semantic_cache": map[string]any{
						"type":        "boolean",
						"description": "Opt in to L2 semantic caching via X-Router-Semantic-Cache: allow (default false).",
					},
				},
				"required": []string{"task", "prompt"},
			},
		},
		{
			"name":        "router_metrics",
			"description": "Fetch live token, cost savings, cache hit rate, and latency metrics from the Local LLM Router Proxy (/metrics/summary).",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "router_cache_purge",
			"description": "Purge cached responses from the Local LLM Router Proxy (DELETE /cache).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"namespace": map[string]any{
						"type":        "string",
						"description": "Optional route_namespace to purge; omit to purge all entries.",
					},
				},
			},
		},
	}
}

func (s *Server) callTool(ctx context.Context, name string, rawArgs json.RawMessage) map[string]any {
	switch name {
	case "offload_llm":
		return s.callOffloadLLM(ctx, rawArgs)
	case "router_metrics":
		return s.callRouterMetrics(ctx)
	case "router_cache_purge":
		return s.callRouterCachePurge(ctx, rawArgs)
	default:
		return mcpError("unknown tool: " + name)
	}
}

func (s *Server) callOffloadLLM(ctx context.Context, rawArgs json.RawMessage) map[string]any {
	_ = s.EnsureProxyRunning(ctx)
	var args struct {
		Task          string `json:"task"`
		Prompt        string `json:"prompt"`
		System        string `json:"system"`
		Deterministic *bool  `json:"deterministic"`
		SemanticCache bool   `json:"semantic_cache"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil || args.Task == "" || args.Prompt == "" {
		return mcpError("offload_llm requires non-empty 'task' and 'prompt'")
	}

	var msgs []map[string]string
	if args.System != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": args.System})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": args.Prompt})

	payload := map[string]any{
		"model":    "router/local",
		"messages": msgs,
		"stream":   false,
	}
	if args.Deterministic == nil || *args.Deterministic {
		payload["temperature"] = 0
	}
	bodyBytes, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.routerURL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return mcpError("build request failed: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Router-Task", args.Task)
	if args.SemanticCache {
		req.Header.Set("X-Router-Semantic-Cache", "allow")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return mcpError(fmt.Sprintf("router proxy unreachable at %s (%v). Start it with scripts/start-proxy.ps1", s.routerURL, err))
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return mcpError("read proxy response failed: " + err.Error())
	}
	if resp.StatusCode != http.StatusOK {
		return mcpError(fmt.Sprintf("proxy returned HTTP %d: %s", resp.StatusCode, string(respBytes)))
	}

	var oaiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBytes, &oaiResp); err != nil || len(oaiResp.Choices) == 0 {
		return mcpError("unexpected proxy response JSON: " + string(respBytes))
	}

	provider := resp.Header.Get("X-Router-Provider")
	model := resp.Header.Get("X-Router-Model")
	cacheStatus := resp.Header.Get("X-Router-Cache")
	if cacheStatus == "" {
		cacheStatus = "miss"
	}
	text := fmt.Sprintf("[provider=%s, model=%s, cache=%s]\n%s", provider, model, cacheStatus, oaiResp.Choices[0].Message.Content)
	return mcpText(text)
}

func (s *Server) callRouterMetrics(ctx context.Context) map[string]any {
	_ = s.EnsureProxyRunning(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.routerURL+"/metrics/summary", nil)
	if err != nil {
		return mcpError(err.Error())
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return mcpError(fmt.Sprintf("router proxy unreachable at %s (%v). Start it with scripts/start-proxy.ps1", s.routerURL, err))
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return mcpError(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(raw)))
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, raw, "", "  ") == nil {
		return mcpText(pretty.String())
	}
	return mcpText(string(raw))
}

func (s *Server) callRouterCachePurge(ctx context.Context, rawArgs json.RawMessage) map[string]any {
	_ = s.EnsureProxyRunning(ctx)
	var args struct {
		Namespace string `json:"namespace"`
	}
	if len(rawArgs) > 0 {
		_ = json.Unmarshal(rawArgs, &args)
	}
	target := s.routerURL + "/cache"
	if args.Namespace != "" {
		target += "?namespace=" + url.QueryEscape(args.Namespace)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return mcpError(err.Error())
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return mcpError(fmt.Sprintf("router proxy unreachable at %s (%v). Start it with scripts/start-proxy.ps1", s.routerURL, err))
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return mcpError(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(raw)))
	}
	return mcpText(strings.TrimSpace(string(raw)))
}

func mcpText(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
	}
}

func mcpError(msg string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{
			{"type": "text", "text": msg},
		},
	}
}
