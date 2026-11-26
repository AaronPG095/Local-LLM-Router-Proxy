package cache

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/storage"
)

func TestCanonicalKeyDeterministicAndBypass(t *testing.T) {
	b1 := []byte(`{"b":2,"a":1,"temperature":0}`)
	b2 := []byte(`{"temperature":0, "a":1, "b":2}`)
	k1 := CanonicalKey("openai_chat", "gpt-4o", false, b1)
	k2 := CanonicalKey("openai_chat", "gpt-4o", false, b2)
	if k1 != k2 {
		t.Fatalf("expected identical canonical keys, got %s vs %s", k1, k2)
	}

	if !IsDeterministic(b1, http.Header{}, true) {
		t.Fatalf("expected temperature=0 to be deterministic")
	}
	if IsDeterministic([]byte(`{"temperature":0.7}`), http.Header{}, true) {
		t.Fatalf("expected temperature=0.7 to be non-deterministic")
	}
	hForce := http.Header{}
	hForce.Set("X-Router-Cache", "force")
	if !IsDeterministic([]byte(`{"temperature":0.7}`), hForce, true) {
		t.Fatalf("expected X-Router-Cache: force to override deterministic gate")
	}

	hBypass := http.Header{}
	hBypass.Set("X-Router-Cache", "bypass")
	if !ShouldBypass(hBypass) {
		t.Fatalf("expected X-Router-Cache: bypass to trigger ShouldBypass")
	}
}

func TestL1ExactAndL2OptInSemanticCache(t *testing.T) {
	embServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	defer embServer.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "router.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	eng := NewEngine(store, config.CacheConfig{
		L1Exact: config.L1CacheConfig{
			Enabled:              true,
			TTLSeconds:           3600,
			MaxEntries:           100,
			RequireDeterministic: true,
		},
		L2Semantic: config.L2CacheConfig{
			Enabled:             false, // strictly opt-in
			SimilarityThreshold: 0.94,
			TTLSeconds:          3600,
			MaxCandidates:       50,
		},
	}, NewOllamaEmbedder(embServer.URL, "nomic-embed-text"))

	ctx := context.Background()

	// 1. Store an L1 + L2 opt-in entry
	lr1 := LookupRequest{
		APIFormat:          "openai_chat",
		TargetModel:        "qwen2.5-coder:7b",
		RouteNamespace:     "proj:commit:qwen",
		RouteSemanticCache: true,
		Headers:            http.Header{},
		Body:               []byte(`{"temperature":0,"messages":[{"role":"user","content":"summarize diff A"}]}`),
		PromptText:         "summarize diff A",
	}
	if err := eng.Store(ctx, StoreRequest{
		Lookup:           lr1,
		HeadersJSON:      `{"Content-Type":"application/json"}`,
		ResponseBody:     []byte(`{"choices":[{"message":{"content":"summary A"}}]}`),
		PromptTokens:     50,
		CompletionTokens: 10,
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// 2. Exact L1 hit
	res1, err := eng.Lookup(ctx, lr1)
	if err != nil || !res1.Hit || res1.Status != "l1_hit" {
		t.Fatalf("expected l1_hit, got %+v, err=%v", res1, err)
	}

	// 3. Semantic L2 hit with different wording (non-deterministic temp=0.5, opted in)
	lr2 := LookupRequest{
		APIFormat:          "openai_chat",
		TargetModel:        "qwen2.5-coder:7b",
		RouteNamespace:     "proj:commit:qwen",
		RouteSemanticCache: true,
		Headers:            http.Header{},
		Body:               []byte(`{"temperature":0.5,"messages":[{"role":"user","content":"summarize diff B"}]}`),
		PromptText:         "summarize diff B",
	}
	res2, err := eng.Lookup(ctx, lr2)
	if err != nil || !res2.Hit || res2.Status != "l2_hit" || res2.Similarity < 0.94 {
		t.Fatalf("expected l2_hit, got %+v, err=%v", res2, err)
	}

	// 4. L2 blocked when HasTools=true
	lrTools := lr2
	lrTools.HasTools = true
	resTools, _ := eng.Lookup(ctx, lrTools)
	if resTools.Hit {
		t.Fatalf("expected L2 miss when HasTools=true, got %+v", resTools)
	}

	// 5. L2 blocked when not opted in
	lrNoOpt := lr2
	lrNoOpt.RouteSemanticCache = false
	resNoOpt, _ := eng.Lookup(ctx, lrNoOpt)
	if resNoOpt.Hit {
		t.Fatalf("expected L2 miss when not opted in, got %+v", resNoOpt)
	}
}
