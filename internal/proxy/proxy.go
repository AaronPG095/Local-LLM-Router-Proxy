package proxy

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/budget"
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
	client   *http.Client
	mux      *http.ServeMux
}

// NewServer constructs a proxy Server with configured timeouts, policy router, and routes.
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
		// Fall back to rule-less pass-through engine if config regex fails.
		eng, _ = router.NewEngine(&config.Config{Providers: cfg.Providers, Routing: config.RoutingConfig{DefaultTarget: cfg.Routing.DefaultTarget, NeverDowngrade: cfg.Routing.NeverDowngrade}})
	}

	s := &Server{
		cfg:      cfg,
		store:    store,
		recorder: recorder,
		guard:    guard,
		engine:   eng,
		client:   &http.Client{Transport: transport},
		mux:      http.NewServeMux(),
	}

	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/metrics/summary", s.handleMetricsSummary)
	s.mux.HandleFunc("/v1beta/models/", s.handleGemini)
	s.mux.HandleFunc("/v1/chat/completions", s.handleOpenAIChat)

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
