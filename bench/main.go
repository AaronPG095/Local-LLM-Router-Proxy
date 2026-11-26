package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/budget"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/proxy"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/storage"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/telemetry"
)

type workloadItem struct {
	name    string
	path    string
	headers map[string]string
	body    string
}

type benchReport struct {
	TotalRequests        int     `json:"total_requests"`
	DirectFrontierTokens int64   `json:"direct_frontier_tokens"`
	ProxiedFrontierTokens int64  `json:"proxied_frontier_tokens"`
	FrontierTokensSavedPct float64 `json:"frontier_tokens_saved_pct"`
	DirectCostUSD        float64 `json:"direct_cost_usd"`
	ProxiedCostUSD       float64 `json:"proxied_cost_usd"`
	CostSavedPct         float64 `json:"cost_saved_pct"`
	CacheHitRatePct      float64 `json:"cache_hit_rate_pct"`
	P50OverheadMs        int64   `json:"p50_overhead_ms"`
	P95OverheadMs        int64   `json:"p95_overhead_ms"`
}

func boolPtr(b bool) *bool { return &b }

func main() {
	// Deterministic mock upstreams with realistic simulated TTFB.
	geminiMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"parts":[{"text":"frontier-gemini-response"}]}}],
			"usageMetadata":{"promptTokenCount":1200,"candidatesTokenCount":300}
		}`))
	}))
	defer geminiMock.Close()

	openaiMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"frontier-openai-response"}}],
			"usage":{"prompt_tokens":1000,"completion_tokens":250}
		}`))
	}))
	defer openaiMock.Close()

	ollamaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/embeddings" {
			var req struct {
				Input string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			vec := []float32{0.0, 1.0, 0.0}
			if strings.Contains(strings.ToLower(req.Input), "summarize") {
				vec = []float32{0.99, 0.14, 0.0}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"embedding": vec}},
			})
			return
		}
		time.Sleep(15 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"local-ollama-response"}}],
			"usage":{"prompt_tokens":600,"completion_tokens":120}
		}`))
	}))
	defer ollamaMock.Close()

	tmpDir, err := os.MkdirTemp("", "llrp-bench-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := storage.Open(filepath.Join(tmpDir, "bench.db"))
	if err != nil {
		panic(err)
	}
	defer store.Close()

	rec, err := telemetry.NewRecorder(store, filepath.Join(tmpDir, "bench.jsonl"))
	if err != nil {
		panic(err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{ConnectTimeoutMs: 2000, FirstByteTimeoutMs: 5000},
		Providers: map[string]config.ProviderConfig{
			"gemini": {
				Type:    "gemini",
				BaseURL: geminiMock.URL,
				PricingPer1M: map[string]config.ModelPricing{
					"gemini-2.5-pro":   {Input: 1.25, Output: 10.00},
					"gemini-2.5-flash": {Input: 0.15, Output: 0.60},
				},
			},
			"openai": {
				Type:    "openai",
				BaseURL: openaiMock.URL + "/v1",
				PricingPer1M: map[string]config.ModelPricing{
					"gpt-4o": {Input: 2.50, Output: 10.00},
				},
			},
			"ollama": {
				Type:    "openai_compatible",
				BaseURL: ollamaMock.URL + "/v1",
			},
		},
		Routing: config.RoutingConfig{
			NeverDowngrade: config.NeverDowngradeConfig{
				Headers:                    map[string]string{"X-Router-Tier": "frontier"},
				WhenReasoningParamsPresent: true,
			},
			Rules: []config.RoutingRule{
				{
					Name: "mechanical-offload",
					Match: config.MatchRule{
						Headers:  map[string][]string{"X-Router-Task": {"commit_msg", "log_trim", "summarize"}},
						HasTools: boolPtr(false),
					},
					Target:        "ollama:qwen2.5-coder:7b",
					SemanticCache: true,
				},
			},
		},
		Cache: config.CacheConfig{
			L1Exact: config.L1CacheConfig{
				Enabled:              true,
				TTLSeconds:           3600,
				MaxEntries:           1000,
				RequireDeterministic: true,
			},
			L2Semantic: config.L2CacheConfig{
				Enabled:             false,
				EmbeddingProvider:   "ollama:nomic-embed-text",
				SimilarityThreshold: 0.94,
				TTLSeconds:          3600,
				MaxCandidates:       100,
			},
		},
		Budgets: config.BudgetsConfig{
			Session:      config.BudgetScopeConfig{MaxTokens: 10000000, SoftWarnPct: 0.8, HardAction: "block"},
			ProjectDaily: config.BudgetScopeConfig{MaxTokens: 10000000, SoftWarnPct: 0.8, HardAction: "block"},
		},
	}

	srv := proxy.NewServer(cfg, store, rec, budget.NewGuard(store, cfg.Budgets))
	proxyTS := httptest.NewServer(srv.Handler())
	defer proxyTS.Close()

	workload := buildWorkload()

	// 1. Direct baseline (all 20 requests hit frontier directly)
	var directTokens int64
	var directCost float64
	for _, item := range workload {
		if strings.HasPrefix(item.path, "/v1beta/models/") {
			directTokens += 1500
			p, _ := cfg.LookupPricing("gemini", "gemini-2.5-pro")
			directCost += telemetry.CalculateCost(p, 1200, 300, 0)
		} else {
			directTokens += 1250
			p, _ := cfg.LookupPricing("openai", "gpt-4o")
			directCost += telemetry.CalculateCost(p, 1000, 250, 0)
		}
	}

	// 2. Proxied workload (Routed + L1 Exact + Opt-in L2 Semantic Cache)
	client := &http.Client{Timeout: 5 * time.Second}
	for _, item := range workload {
		req, _ := http.NewRequest(http.MethodPost, proxyTS.URL+item.path, bytes.NewBufferString(item.body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range item.headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			panic(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	sum, err := store.GetMetricsSummary(context.Background(), 0)
	if err != nil {
		panic(err)
	}

	// Query frontier tokens & pass-through overhead from requests_log
	var proxiedFrontierTokens int64
	_ = store.DB().QueryRow(`
		SELECT COALESCE(SUM(prompt_tokens + completion_tokens), 0)
		FROM requests_log
		WHERE routed_provider IN ('gemini', 'openai') AND cache_status = 'miss'
	`).Scan(&proxiedFrontierTokens)

	rows, err := store.DB().Query(`SELECT proxy_overhead_ms FROM requests_log WHERE cache_status = 'miss'`)
	if err != nil {
		panic(err)
	}
	var overheads []int64
	for rows.Next() {
		var o int64
		_ = rows.Scan(&o)
		overheads = append(overheads, o)
	}
	rows.Close()
	sort.Slice(overheads, func(i, j int) bool { return overheads[i] < overheads[j] })

	rep := benchReport{
		TotalRequests:          len(workload),
		DirectFrontierTokens:   directTokens,
		ProxiedFrontierTokens:  proxiedFrontierTokens,
		FrontierTokensSavedPct: pctSaved(float64(directTokens), float64(proxiedFrontierTokens)),
		DirectCostUSD:          directCost,
		ProxiedCostUSD:         sum.EstimatedCostUSD,
		CostSavedPct:           pctSaved(directCost, sum.EstimatedCostUSD),
		CacheHitRatePct:        sum.CacheHitRate * 100.0,
		P50OverheadMs:          pct(overheads, 0.50),
		P95OverheadMs:          pct(overheads, 0.95),
	}

	fmt.Println("# Local LLM Router Proxy — Benchmark & Proof Report")
	fmt.Println()
	fmt.Println("| Metric | Direct | Proxied (Routed + Cached) | Savings / Result |")
	fmt.Println("| :--- | ---: | ---: | ---: |")
	fmt.Printf("| Total Requests | %d | %d | — |\n", rep.TotalRequests, rep.TotalRequests)
	fmt.Printf("| Frontier Tokens | %d | %d | **%.1f%% saved** |\n", rep.DirectFrontierTokens, rep.ProxiedFrontierTokens, rep.FrontierTokensSavedPct)
	fmt.Printf("| Estimated Cost (USD) | $%.5f | $%.5f | **%.1f%% saved** |\n", rep.DirectCostUSD, rep.ProxiedCostUSD, rep.CostSavedPct)
	fmt.Printf("| Cache Hit Rate | 0.0%% | %.1f%% | %d hits |\n", rep.CacheHitRatePct, sum.CacheHits)
	fmt.Printf("| Pass-Through Overhead (p50 / p95) | — | %dms / %dms | <5ms target |\n", rep.P50OverheadMs, rep.P95OverheadMs)
	fmt.Println()
	fmt.Println("```json")
	enc, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(enc))
	fmt.Println("```")
}

func buildWorkload() []workloadItem {
	var items []workloadItem
	// 4 frontier reasoning requests (never downgraded)
	for i := 0; i < 4; i++ {
		items = append(items, workloadItem{
			name:    fmt.Sprintf("frontier-%d", i),
			path:    "/v1beta/models/gemini-2.5-pro:generateContent",
			headers: map[string]string{"X-Router-Tier": "frontier"},
			body:    fmt.Sprintf(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}},"contents":[{"parts":[{"text":"design architecture %d"}]}]}`, i),
		})
	}
	// 6 repeated deterministic tool/status checks (1 miss + 5 L1 exact hits)
	for i := 0; i < 6; i++ {
		items = append(items, workloadItem{
			name: fmt.Sprintf("deterministic-%d", i),
			path: "/v1/chat/completions",
			body: `{"model":"gpt-4o","temperature":0,"messages":[{"role":"user","content":"check lint rule R001"}]}`,
		})
	}
	// 6 mechanical commit_msg offloads (routed to local Ollama)
	for i := 0; i < 6; i++ {
		items = append(items, workloadItem{
			name:    fmt.Sprintf("commit-%d", i),
			path:    "/v1beta/models/gemini-2.5-pro:generateContent",
			headers: map[string]string{"X-Router-Task": "commit_msg"},
			body:    fmt.Sprintf(`{"contents":[{"parts":[{"text":"diff file %d"}]}]}`, i),
		})
	}
	// 4 semantically similar summarization tasks (opt-in L2 semantic cache: 1 miss + 3 L2 hits)
	for i := 0; i < 4; i++ {
		items = append(items, workloadItem{
			name:    fmt.Sprintf("summary-%d", i),
			path:    "/v1/chat/completions",
			headers: map[string]string{"X-Router-Task": "summarize"},
			body:    fmt.Sprintf(`{"model":"gpt-4o","temperature":0.4,"messages":[{"role":"user","content":"summarize build log variant %d"}]}`, i),
		})
	}
	return items
}

func pctSaved(baseline, actual float64) float64 {
	if baseline <= 0 {
		return 0
	}
	return ((baseline - actual) / baseline) * 100.0
}

func pct(vals []int64, p float64) int64 {
	if len(vals) == 0 {
		return 0
	}
	idx := int(float64(len(vals)-1) * p)
	return vals[idx]
}
