package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPServerInitializeListAndToolCalls(t *testing.T) {
	proxyMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
			if r.Header.Get("X-Router-Task") != "commit_msg" {
				t.Errorf("expected X-Router-Task=commit_msg, got %q", r.Header.Get("X-Router-Task"))
			}
			w.Header().Set("X-Router-Provider", "ollama")
			w.Header().Set("X-Router-Model", "gemma3:4b")
			w.Header().Set("X-Router-Cache", "miss")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"feat: add mcp offload"}}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/metrics/summary":
			_, _ = w.Write([]byte(`{"total_requests":5,"saved_tokens":420}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/cache":
			_, _ = w.Write([]byte(`{"deleted":3}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer proxyMock.Close()

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"offload_llm","arguments":{"task":"commit_msg","prompt":"diff --git a/x"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"router_metrics","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"router_cache_purge","arguments":{"namespace":"p1"}}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	srv := NewServer(proxyMock.URL)
	if err := srv.Serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 JSON-RPC responses (notification skipped), got %d:\n%s", len(lines), out.String())
	}

	var listResp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil || len(listResp.Result.Tools) != 3 {
		t.Fatalf("expected 3 tools in tools/list, got %s", lines[1])
	}

	if !strings.Contains(lines[2], "feat: add mcp offload") || !strings.Contains(lines[2], "provider=ollama") {
		t.Fatalf("unexpected offload_llm response: %s", lines[2])
	}
	if !strings.Contains(lines[3], "420") {
		t.Fatalf("unexpected router_metrics response: %s", lines[3])
	}
	if !strings.Contains(lines[4], "deleted") || !strings.Contains(lines[4], "3") {
		t.Fatalf("unexpected router_cache_purge response: %s", lines[4])
	}
}
