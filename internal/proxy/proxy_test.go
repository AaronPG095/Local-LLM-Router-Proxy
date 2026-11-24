package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/budget"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/storage"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/telemetry"
)

func newTestServer(t *testing.T, geminiURL, openaiURL string, sessionMaxTokens int64) (*Server, *storage.Store, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "router.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	jsonlPath := filepath.Join(dir, "telemetry.jsonl")
	rec, err := telemetry.NewRecorder(store, jsonlPath)
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:               "127.0.0.1",
			Port:               8787,
			ConnectTimeoutMs:   2000,
			FirstByteTimeoutMs: 5000,
		},
		Providers: map[string]config.ProviderConfig{
			"gemini": {
				Type:    "gemini",
				BaseURL: geminiURL,
				PricingPer1M: map[string]config.ModelPricing{
					"gemini-2.5-flash": {Input: 0.15, Output: 0.60},
				},
			},
			"openai": {
				Type:    "openai",
				BaseURL: openaiURL,
				PricingPer1M: map[string]config.ModelPricing{
					"gpt-4o": {Input: 2.50, Output: 10.00},
				},
			},
		},
		Routing: config.RoutingConfig{
			NeverDowngrade: config.NeverDowngradeConfig{
				Headers:                    map[string]string{"X-Router-Tier": "frontier"},
				WhenReasoningParamsPresent: true,
			},
		},
		Budgets: config.BudgetsConfig{
			Session: config.BudgetScopeConfig{
				MaxTokens:   sessionMaxTokens,
				SoftWarnPct: 0.80,
				HardAction:  "block",
			},
			ProjectDaily: config.BudgetScopeConfig{
				MaxTokens:   100000,
				SoftWarnPct: 0.80,
				HardAction:  "block",
			},
		},
	}

	guard := budget.NewGuard(store, cfg.Budgets)
	return NewServer(cfg, store, rec, guard), store, jsonlPath
}

func TestGeminiNonStreamingAndStreamingSSE(t *testing.T) {
	geminiMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ":generateContent") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"candidates":[{"content":{"parts":[{"text":"hello"}]}}],
				"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":25,"cachedContentTokenCount":10}
			}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			chunks := []string{
				`data: {"candidates":[{"content":{"parts":[{"text":"part1"}]}}]}`,
				`data: {"candidates":[{"content":{"parts":[{"text":"part2"}]}}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":20,"thoughtsTokenCount":5}}`,
			}
			for _, c := range chunks {
				_, _ = fmt.Fprintf(w, "%s\n\n", c)
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer geminiMock.Close()

	srv, store, jsonlPath := newTestServer(t, geminiMock.URL, "http://127.0.0.1:1", 10000)
	proxyTS := httptest.NewServer(srv.Handler())
	defer proxyTS.Close()

	// 1. Non-streaming Gemini
	resp, err := http.Post(proxyTS.URL+"/v1beta/models/gemini-2.5-flash:generateContent", "application/json", strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "hello") {
		t.Fatalf("unexpected non-streaming response: %d %s", resp.StatusCode, string(body))
	}

	// 2. Streaming Gemini SSE
	sResp, err := http.Post(proxyTS.URL+"/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", "application/json", strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	sBody, _ := io.ReadAll(sResp.Body)
	sResp.Body.Close()
	if sResp.StatusCode != 200 || !strings.Contains(string(sBody), "part1") || !strings.Contains(string(sBody), "part2") {
		t.Fatalf("unexpected streaming response: %d %s", sResp.StatusCode, string(sBody))
	}

	sum, err := store.GetMetricsSummary(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum.TotalRequests != 2 || sum.PromptTokens != 150 || sum.CompletionTokens != 50 {
		t.Fatalf("unexpected metrics summary: %+v", sum)
	}

	rawJSONL, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Split(strings.TrimSpace(string(rawJSONL)), "\n")) != 2 {
		t.Fatalf("expected 2 JSONL lines, got:\n%s", string(rawJSONL))
	}
}

func TestOpenAISSEStreamOptionsInjectionAndBudgetGuard(t *testing.T) {
	var sawIncludeUsage bool
	openaiMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if bytes.Contains(raw, []byte(`"include_usage":true`)) {
			sawIncludeUsage = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"1\",\"choices\":[],\"usage\":{\"prompt_tokens\":85,\"completion_tokens\":10}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer openaiMock.Close()

	// Session cap = 100 tokens, soft warn at 80
	srv, _, _ := newTestServer(t, "http://127.0.0.1:1", openaiMock.URL, 100)
	proxyTS := httptest.NewServer(srv.Handler())
	defer proxyTS.Close()

	// Request 1: 95 tokens recorded (85 + 10)
	req1, _ := http.NewRequest(http.MethodPost, proxyTS.URL+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Router-Session", "s-budget")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if !sawIncludeUsage {
		t.Fatalf("expected proxy to inject stream_options.include_usage=true")
	}

	// Request 2: at 95/100 tokens -> soft warn header should be present
	req2, _ := http.NewRequest(http.MethodPost, proxyTS.URL+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi2"}]}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Router-Session", "s-budget")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.Header.Get("X-Router-Budget-Warning") == "" {
		t.Fatalf("expected X-Router-Budget-Warning header at 95%% budget")
	}

	// Request 3: now at 190/100 tokens -> hard block 429
	req3, _ := http.NewRequest(http.MethodPost, proxyTS.URL+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi3"}]}`))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("X-Router-Session", "s-budget")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected HTTP 429 when budget exceeded, got %d", resp3.StatusCode)
	}

	// Verify /metrics/summary
	mResp, err := http.Get(proxyTS.URL + "/metrics/summary")
	if err != nil {
		t.Fatal(err)
	}
	defer mResp.Body.Close()
	var sum storage.MetricsSummary
	if err := json.NewDecoder(mResp.Body).Decode(&sum); err != nil {
		t.Fatal(err)
	}
	if sum.TotalRequests != 2 {
		t.Fatalf("expected 2 logged requests, got %d", sum.TotalRequests)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestFallbackOn429And529PreFirstByte(t *testing.T) {
	primaryMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer primaryMock.Close()

	fallbackMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"parts":[{"text":"recovered"}]}}],
			"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5}
		}`))
	}))
	defer fallbackMock.Close()

	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "router.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rec, _ := telemetry.NewRecorder(store, filepath.Join(dir, "telemetry.jsonl"))

	cfg := &config.Config{
		Server: config.ServerConfig{ConnectTimeoutMs: 2000, FirstByteTimeoutMs: 5000},
		Providers: map[string]config.ProviderConfig{
			"gemini":          {Type: "gemini", BaseURL: primaryMock.URL},
			"gemini-fallback": {Type: "gemini", BaseURL: fallbackMock.URL},
		},
		Routing: config.RoutingConfig{
			Rules: []config.RoutingRule{
				{
					Name:          "resilient-gemini",
					Match:         config.MatchRule{ModelAlias: "gemini-2.5-flash"},
					Target:        "gemini:gemini-2.5-flash",
					FallbackChain: []string{"gemini-fallback:gemini-2.5-flash"},
				},
			},
		},
		Budgets: config.BudgetsConfig{
			Session:      config.BudgetScopeConfig{MaxTokens: 100000, SoftWarnPct: 0.8, HardAction: "block"},
			ProjectDaily: config.BudgetScopeConfig{MaxTokens: 100000, SoftWarnPct: 0.8, HardAction: "block"},
		},
	}

	srv := NewServer(cfg, store, rec, budget.NewGuard(store, cfg.Budgets))
	proxyTS := httptest.NewServer(srv.Handler())
	defer proxyTS.Close()

	resp, err := http.Post(proxyTS.URL+"/v1beta/models/gemini-2.5-flash:generateContent", "application/json", strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 || !strings.Contains(string(body), "recovered") {
		t.Fatalf("expected fallback 200 recovered, got %d %s", resp.StatusCode, string(body))
	}
	if resp.Header.Get("X-Router-Provider") != "gemini-fallback" {
		t.Fatalf("expected X-Router-Provider=gemini-fallback, got %q", resp.Header.Get("X-Router-Provider"))
	}

	var attempts int
	if err := store.DB().QueryRow(`SELECT fallback_attempts FROM requests_log LIMIT 1`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("expected fallback_attempts=1, got %d", attempts)
	}
}

func TestGeminiOffloadToOllamaTranslation(t *testing.T) {
	ollamaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"feat: \"}}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":30,\"completion_tokens\":5}}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"feat: commit"},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":4}}`))
	}))
	defer ollamaMock.Close()

	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "router.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rec, _ := telemetry.NewRecorder(store, filepath.Join(dir, "telemetry.jsonl"))

	cfg := &config.Config{
		Server: config.ServerConfig{ConnectTimeoutMs: 2000, FirstByteTimeoutMs: 5000},
		Providers: map[string]config.ProviderConfig{
			"gemini": {
				Type:    "gemini",
				BaseURL: "http://127.0.0.1:1",
				PricingPer1M: map[string]config.ModelPricing{
					"gemini-2.5-flash": {Input: 0.15, Output: 0.60},
				},
			},
			"ollama": {
				Type:    "openai_compatible",
				BaseURL: ollamaMock.URL + "/v1",
			},
		},
		Routing: config.RoutingConfig{
			Rules: []config.RoutingRule{
				{
					Name: "commit-offload",
					Match: config.MatchRule{
						Headers:  map[string][]string{"X-Router-Task": {"commit_msg"}},
						HasTools: boolPtr(false),
					},
					Target: "ollama:qwen2.5-coder:7b",
				},
			},
		},
		Budgets: config.BudgetsConfig{
			Session:      config.BudgetScopeConfig{MaxTokens: 100000, SoftWarnPct: 0.8, HardAction: "block"},
			ProjectDaily: config.BudgetScopeConfig{MaxTokens: 100000, SoftWarnPct: 0.8, HardAction: "block"},
		},
	}

	srv := NewServer(cfg, store, rec, budget.NewGuard(store, cfg.Budgets))
	proxyTS := httptest.NewServer(srv.Handler())
	defer proxyTS.Close()

	// Non-streaming Gemini -> Ollama
	req1, _ := http.NewRequest(http.MethodPost, proxyTS.URL+"/v1beta/models/gemini-2.5-flash:generateContent", strings.NewReader(`{"contents":[{"parts":[{"text":"diff"}]}]}`))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Router-Task", "commit_msg")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != 200 || resp1.Header.Get("X-Router-Provider") != "ollama" || !strings.Contains(string(b1), "feat: commit") {
		t.Fatalf("unexpected translated non-stream response: %d %s", resp1.StatusCode, string(b1))
	}

	// Streaming Gemini SSE -> Ollama SSE -> Gemini SSE
	req2, _ := http.NewRequest(http.MethodPost, proxyTS.URL+"/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", strings.NewReader(`{"contents":[{"parts":[{"text":"diff"}]}]}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Router-Task", "commit_msg")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 200 || !strings.Contains(string(b2), "feat: ") || !strings.Contains(string(b2), "usageMetadata") {
		t.Fatalf("unexpected translated SSE response: %d %s", resp2.StatusCode, string(b2))
	}

	sum, err := store.GetMetricsSummary(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum.SavedTokens != 64 {
		t.Fatalf("expected SavedTokens=64 (29+35), got %d", sum.SavedTokens)
	}
}

func TestOpenAIResponsesPassThroughAndSSE(t *testing.T) {
	openaiMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":40,\"output_tokens\":12,\"input_tokens_details\":{\"cached_tokens\":10}}}}\n\n"))
	}))
	defer openaiMock.Close()

	srv, store, _ := newTestServer(t, "http://127.0.0.1:1", openaiMock.URL+"/v1", 10000)
	proxyTS := httptest.NewServer(srv.Handler())
	defer proxyTS.Close()

	resp, err := http.Post(proxyTS.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-4o","input":"hello","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 || !strings.Contains(string(body), "response.completed") {
		t.Fatalf("unexpected responses SSE output: %d %s", resp.StatusCode, string(body))
	}
	sum, err := store.GetMetricsSummary(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum.PromptTokens != 40 || sum.CompletionTokens != 12 || sum.CachedTokens != 10 {
		t.Fatalf("unexpected responses usage summary: %+v", sum)
	}
}

func TestAnthropicMessagesSSEAnd529Fallback(t *testing.T) {
	primary529 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(529)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error"}}`))
	}))
	defer primary529.Close()

	secondaryAnthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":60,\"cache_read_input_tokens\":15}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hello\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":25}}\n\n"))
	}))
	defer secondaryAnthropic.Close()

	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "router.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rec, _ := telemetry.NewRecorder(store, filepath.Join(dir, "telemetry.jsonl"))

	cfg := &config.Config{
		Server: config.ServerConfig{ConnectTimeoutMs: 2000, FirstByteTimeoutMs: 5000},
		Providers: map[string]config.ProviderConfig{
			"anthropic":           {Type: "anthropic", BaseURL: primary529.URL + "/v1"},
			"anthropic-secondary": {Type: "anthropic", BaseURL: secondaryAnthropic.URL + "/v1"},
		},
		Routing: config.RoutingConfig{
			Rules: []config.RoutingRule{
				{
					Name:          "anthropic-resilient",
					Match:         config.MatchRule{ModelAlias: "claude-sonnet-4-20250514"},
					Target:        "anthropic:claude-sonnet-4-20250514",
					FallbackChain: []string{"anthropic-secondary:claude-sonnet-4-20250514"},
				},
			},
		},
		Budgets: config.BudgetsConfig{
			Session:      config.BudgetScopeConfig{MaxTokens: 100000, SoftWarnPct: 0.8, HardAction: "block"},
			ProjectDaily: config.BudgetScopeConfig{MaxTokens: 100000, SoftWarnPct: 0.8, HardAction: "block"},
		},
	}

	srv := NewServer(cfg, store, rec, budget.NewGuard(store, cfg.Budgets))
	proxyTS := httptest.NewServer(srv.Handler())
	defer proxyTS.Close()

	resp, err := http.Post(proxyTS.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4-20250514","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 || !strings.Contains(string(body), "content_block_delta") {
		t.Fatalf("unexpected anthropic SSE output: %d %s", resp.StatusCode, string(body))
	}
	sum, err := store.GetMetricsSummary(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum.PromptTokens != 60 || sum.CompletionTokens != 25 || sum.CachedTokens != 15 {
		t.Fatalf("unexpected anthropic usage summary: %+v", sum)
	}
}
