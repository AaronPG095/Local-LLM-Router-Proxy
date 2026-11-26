package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/budget"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/cache"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/router"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/storage"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/telemetry"
)

// Server is the HTTP reverse proxy for LLM providers.
type Server struct {
	cfg      *config.Config
	store    *storage.Store
	recorder *telemetry.Recorder
	guard    *budget.Guard
	engine   *router.Engine
	cache    *cache.Engine
	client   *http.Client
	mux      *http.ServeMux
}

// NewServer constructs a proxy Server with configured timeouts, policy router, cache, and routes.
func NewServer(cfg *config.Config, store *storage.Store, recorder *telemetry.Recorder, guard *budget.Guard) *Server {
	dialTimeout := time.Duration(cfg.Server.ConnectTimeoutMs) * time.Millisecond
	firstByteTimeout := time.Duration(cfg.Server.FirstByteTimeoutMs) * time.Millisecond

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: firstByteTimeout,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
	}

	eng, err := router.NewEngine(cfg)
	if err != nil {
		eng, _ = router.NewEngine(&config.Config{Providers: cfg.Providers, Routing: config.RoutingConfig{DefaultTarget: cfg.Routing.DefaultTarget, NeverDowngrade: cfg.Routing.NeverDowngrade}})
	}

	var embedder cache.Embedder
	if spec := strings.TrimSpace(cfg.Cache.L2Semantic.EmbeddingProvider); spec != "" {
		if t, ok := router.ParseTarget(spec); ok {
			if p, exists := cfg.Providers[t.Provider]; exists {
				embedder = cache.NewOllamaEmbedder(p.BaseURL, t.Model)
			}
		}
	}
	cacheEng := cache.NewEngine(store, cfg.Cache, embedder)

	s := &Server{
		cfg:      cfg,
		store:    store,
		recorder: recorder,
		guard:    guard,
		engine:   eng,
		cache:    cacheEng,
		client:   &http.Client{Transport: transport},
		mux:      http.NewServeMux(),
	}

	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/metrics/summary", s.handleMetricsSummary)
	s.mux.HandleFunc("/cache", s.handleCache)
	s.mux.HandleFunc("/v1beta/models/", s.handleGemini)
	s.mux.HandleFunc("/v1/chat/completions", s.handleOpenAIChat)
	s.mux.HandleFunc("/v1/responses", s.handleOpenAIResponses)
	s.mux.HandleFunc("/v1/messages", s.handleAnthropicMessages)

	return s
}

// Handler returns the HTTP handler for the proxy server.
func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleMetricsSummary(w http.ResponseWriter, r *http.Request) {
	sum, err := s.store.GetMetricsSummary(r.Context(), 0)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query metrics summary")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sum)
}

func (s *Server) handleCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	deleted, err := s.cache.Purge(r.Context(), ns)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to purge cache")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"deleted": deleted})
}

func (s *Server) serveCacheHit(
	ctx context.Context,
	w http.ResponseWriter,
	start time.Time,
	projectID, sessionID, apiFormat, baselineProvider, requestedModel string,
	routeDec router.Decision,
	res cache.LookupResult,
) {
	entry := res.Entry
	var hdrs map[string]string
	_ = json.Unmarshal([]byte(entry.ResponseHeadersJSON), &hdrs)
	for k, v := range hdrs {
		w.Header().Set(k, v)
	}
	if w.Header().Get("Content-Type") == "" {
		if entry.IsStream {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
	}
	w.Header().Set("X-Router-Cache", res.Status)
	if res.Similarity > 0 {
		w.Header().Set("X-Router-Cache-Similarity", fmt.Sprintf("%.4f", res.Similarity))
	}
	w.Header().Set("X-Router-Provider", routeDec.Primary.Provider)
	w.Header().Set("X-Router-Model", entry.Model)
	if routeDec.RuleName != "" {
		w.Header().Set("X-Router-Rule", routeDec.RuleName)
	}
	w.WriteHeader(http.StatusOK)

	if entry.IsStream {
		replaySSE(w, entry.ResponseBody)
	} else {
		_, _ = w.Write(entry.ResponseBody)
	}

	totalMs := time.Since(start).Milliseconds()
	baselinePricing, _ := s.cfg.LookupPricing(baselineProvider, requestedModel)
	baselineCost := telemetry.CalculateCost(baselinePricing, entry.PromptTokens, entry.CompletionTokens, 0)

	_ = s.recorder.Record(ctx, storage.RequestLog{
		TsUTC:            start.UTC().UnixMilli(),
		ProjectID:        projectID,
		SessionID:        sessionID,
		APIFormat:        apiFormat,
		RequestedModel:   requestedModel,
		RoutedProvider:   routeDec.Primary.Provider,
		RoutedModel:      entry.Model,
		RouteRule:        routeDec.RuleName,
		IsStream:         entry.IsStream,
		StatusCode:       http.StatusOK,
		CacheStatus:      res.Status,
		CacheSimilarity:  res.Similarity,
		PromptTokens:     entry.PromptTokens,
		CompletionTokens: entry.CompletionTokens,
		EstimatedCostUSD: 0,
		BaselineCostUSD:  baselineCost,
		SavedCostUSD:     baselineCost,
		SavedTokens:      entry.PromptTokens + entry.CompletionTokens,
		TTFBMs:           totalMs,
		TotalMs:          totalMs,
		ProxyOverheadMs:  totalMs,
	})
}

func (s *Server) buildCacheLookupReq(
	r *http.Request,
	projectID, apiFormat string,
	routeDec router.Decision,
	isStream, hasTools bool,
	rawBody []byte,
) cache.LookupRequest {
	ns := fmt.Sprintf("%s:%s:%s", projectID, routeDec.RuleName, routeDec.Primary.Model)
	return cache.LookupRequest{
		APIFormat:          apiFormat,
		TargetModel:        routeDec.Primary.Model,
		RouteNamespace:     ns,
		IsStream:           isStream,
		HasTools:           hasTools,
		RouteSemanticCache: routeDec.SemanticCacheAllowed,
		Headers:            r.Header,
		Body:               rawBody,
		PromptText:         cache.ExtractPromptText(apiFormat, rawBody),
	}
}

func (s *Server) storeCacheResponse(
	ctx context.Context,
	lookupReq cache.LookupRequest,
	lookupRes cache.LookupResult,
	contentType string,
	body []byte,
	promptTokens, completionTokens int64,
) {
	if s.cache == nil || len(body) == 0 {
		return
	}
	hdrsJSON, _ := json.Marshal(map[string]string{"Content-Type": contentType})
	_ = s.cache.Store(ctx, cache.StoreRequest{
		Lookup:           lookupReq,
		LookupRes:        lookupRes,
		HeadersJSON:      string(hdrsJSON),
		ResponseBody:     body,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
	})
}

func (s *Server) buildCandidates(dec router.Decision, budgetDec budget.Decision) []router.RouteTarget {
	var candidates []router.RouteTarget
	if budgetDec.Action == "downgrade" && budgetDec.DowngradeTarget != "" {
		if dt, ok := router.ParseTarget(budgetDec.DowngradeTarget); ok {
			candidates = append(candidates, dt)
		}
	}
	candidates = append(candidates, dec.Primary)
	candidates = append(candidates, dec.Fallbacks...)
	return candidates
}

func (s *Server) computeSavings(baselineProvider, baselineModel, routedProvider, routedModel string, promptTokens, completionTokens, cachedTokens int64, actualCost float64) (baselineCost, savedCost float64, savedTokens int64) {
	baselinePricing, ok := s.cfg.LookupPricing(baselineProvider, baselineModel)
	if ok {
		baselineCost = telemetry.CalculateCost(baselinePricing, promptTokens, completionTokens, cachedTokens)
	} else {
		baselineCost = actualCost
	}
	if baselineCost > actualCost {
		savedCost = baselineCost - actualCost
	}
	if p, ok := s.cfg.Providers[routedProvider]; ok && p.Type == "openai_compatible" && actualCost == 0 {
		savedTokens = promptTokens + completionTokens
	}
	return baselineCost, savedCost, savedTokens
}

func extractScopes(r *http.Request) (projectID, sessionID string) {
	projectID = strings.TrimSpace(r.Header.Get("X-Router-Project"))
	if projectID == "" {
		projectID = "default"
	}
	sessionID = strings.TrimSpace(r.Header.Get("X-Router-Session"))
	if sessionID == "" {
		sessionID = "default"
	}
	return projectID, sessionID
}

func copyRequestHeaders(src, dst http.Header) {
	for k, vals := range src {
		if isHopByHop(k) || strings.HasPrefix(strings.ToLower(k), "x-router-") {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func copyResponseHeaders(src, dst http.Header) {
	for k, vals := range src {
		if isHopByHop(k) || strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func isHopByHop(h string) bool {
	switch strings.ToLower(h) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade", "host":
		return true
	}
	return false
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"code":    status,
		},
	})
}
