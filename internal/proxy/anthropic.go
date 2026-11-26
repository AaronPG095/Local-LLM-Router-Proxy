package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/router"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/storage"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/telemetry"
)

type anthropicUsage struct {
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
}

func extractAnthropicMeta(raw []byte) (model string, isStream bool, systemPrompt string, hasTools bool, hasReasoning bool) {
	var payload struct {
		Model    string          `json:"model"`
		Stream   bool            `json:"stream"`
		System   json.RawMessage `json:"system"`
		Tools    json.RawMessage `json:"tools"`
		Thinking json.RawMessage `json:"thinking"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil {
		model = payload.Model
		isStream = payload.Stream
		if len(payload.System) > 0 {
			var s string
			if json.Unmarshal(payload.System, &s) == nil {
				systemPrompt = s
			} else {
				systemPrompt = string(payload.System)
			}
		}
		if len(payload.Tools) > 0 && string(payload.Tools) != "null" && string(payload.Tools) != "[]" {
			hasTools = true
		}
		if len(payload.Thinking) > 0 && string(payload.Thinking) != "null" {
			hasReasoning = true
		}
	}
	return model, isStream, systemPrompt, hasTools, hasReasoning
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	requestedModel, isStream, sysPrompt, hasTools, hasReasoning := extractAnthropicMeta(rawBody)
	routeDec := s.engine.Evaluate(router.RequestContext{
		APIFormat:          "anthropic_messages",
		RequestedModel:     requestedModel,
		Headers:            r.Header,
		SystemPrompt:       sysPrompt,
		EstimatedTokens:    len(rawBody) / 4,
		HasTools:           hasTools,
		HasReasoningParams: hasReasoning,
	})

	projectID, sessionID := extractScopes(r)
	budgetDec, err := s.guard.Check(r.Context(), projectID, sessionID, routeDec.NeverDowngrade)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "budget check failed")
		return
	}
	if !budgetDec.Allowed {
		w.Header().Set("X-Router-Budget-Blocked", "true")
		writeJSONError(w, http.StatusTooManyRequests, budgetDec.BlockReason)
		return
	}
	if budgetDec.SoftWarn {
		w.Header().Set("X-Router-Budget-Warning", budgetDec.WarningMsg)
	}

	lookupReq := s.buildCacheLookupReq(r, projectID, "anthropic_messages", routeDec, isStream, hasTools, rawBody)
	lookupRes, _ := s.cache.Lookup(r.Context(), lookupReq)
	if lookupRes.Hit {
		s.serveCacheHit(r.Context(), w, start, projectID, sessionID, "anthropic_messages", "anthropic", requestedModel, routeDec, lookupRes)
		return
	}

	candidates := s.buildCandidates(routeDec, budgetDec)
	preUpstreamMs := time.Since(start).Milliseconds()

	resp, winner, fallbackAttempts, err := executeWithFallback(r.Context(), candidates, func(t router.RouteTarget) (*http.Request, error) {
		pCfg, exists := s.cfg.Providers[t.Provider]
		if !exists {
			return nil, fmt.Errorf("provider %q not configured", t.Provider)
		}
		bodyToSend := rawBody
		if t.Model != requestedModel && t.Model != "" {
			bodyToSend = rewriteOpenAIModel(rawBody, t.Model)
		}
		targetURL := strings.TrimRight(pCfg.BaseURL, "/") + "/messages"
		upReq, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(bodyToSend))
		if reqErr != nil {
			return nil, reqErr
		}
		copyRequestHeaders(r.Header, upReq.Header)
		if upReq.Header.Get("x-api-key") == "" {
			if key := s.cfg.ResolveAPIKey(t.Provider); key != "" {
				upReq.Header.Set("x-api-key", key)
			}
		}
		if upReq.Header.Get("anthropic-version") == "" {
			upReq.Header.Set("anthropic-version", "2023-06-01")
		}
		return upReq, nil
	}, s.client)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(resp.Header, w.Header())
	w.Header().Set("X-Router-Provider", winner.Provider)
	w.Header().Set("X-Router-Model", winner.Model)
	if routeDec.RuleName != "" {
		w.Header().Set("X-Router-Rule", routeDec.RuleName)
	}
	w.WriteHeader(resp.StatusCode)

	var usage anthropicUsage
	var firstByte time.Time
	var capturedBody []byte

	if isStream && resp.StatusCode == http.StatusOK && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		firstByte, capturedBody, _ = forwardSSEWithCapture(w, resp.Body, defaultMaxCaptureBytes, func(payload []byte) {
			var ev struct {
				Type    string `json:"type"`
				Message *struct {
					Usage *anthropicUsage `json:"usage"`
				} `json:"message"`
				Usage *anthropicUsage `json:"usage"`
			}
			if json.Unmarshal(payload, &ev) == nil {
				if ev.Type == "message_start" && ev.Message != nil && ev.Message.Usage != nil {
					usage.InputTokens = ev.Message.Usage.InputTokens
					usage.CacheReadInputTokens = ev.Message.Usage.CacheReadInputTokens
				}
				if ev.Type == "message_delta" && ev.Usage != nil {
					usage.OutputTokens = ev.Usage.OutputTokens
				}
			}
		})
	} else {
		respBytes, readErr := io.ReadAll(resp.Body)
		firstByte = time.Now()
		if readErr == nil {
			var env struct {
				Usage *anthropicUsage `json:"usage"`
			}
			if json.Unmarshal(respBytes, &env) == nil && env.Usage != nil {
				usage = *env.Usage
			}
			if resp.StatusCode == http.StatusOK {
				capturedBody = respBytes
			}
			_, _ = w.Write(respBytes)
		}
	}

	totalMs := time.Since(start).Milliseconds()
	ttfbMs := totalMs
	if !firstByte.IsZero() {
		ttfbMs = firstByte.Sub(start).Milliseconds()
	}

	if resp.StatusCode == http.StatusOK && len(capturedBody) > 0 {
		s.storeCacheResponse(r.Context(), lookupReq, lookupRes, w.Header().Get("Content-Type"), capturedBody, usage.InputTokens, usage.OutputTokens)
	}

	pricing, _ := s.cfg.LookupPricing(winner.Provider, winner.Model)
	cost := telemetry.CalculateCost(pricing, usage.InputTokens, usage.OutputTokens, usage.CacheReadInputTokens)
	baselineCost, savedCost, savedTokens := s.computeSavings("anthropic", requestedModel, winner.Provider, winner.Model, usage.InputTokens, usage.OutputTokens, usage.CacheReadInputTokens, cost)

	_ = s.guard.Record(r.Context(), projectID, sessionID, usage.InputTokens+usage.OutputTokens, cost)
	_ = s.recorder.Record(r.Context(), storage.RequestLog{
		TsUTC:            start.UTC().UnixMilli(),
		ProjectID:        projectID,
		SessionID:        sessionID,
		APIFormat:        "anthropic_messages",
		RequestedModel:   requestedModel,
		RoutedProvider:   winner.Provider,
		RoutedModel:      winner.Model,
		RouteRule:        routeDec.RuleName,
		FallbackAttempts: fallbackAttempts,
		IsStream:         isStream,
		StatusCode:       resp.StatusCode,
		CacheStatus:      "miss",
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		CachedTokens:     usage.CacheReadInputTokens,
		EstimatedCostUSD: cost,
		BaselineCostUSD:  baselineCost,
		SavedCostUSD:     savedCost,
		SavedTokens:      savedTokens,
		TTFBMs:           ttfbMs,
		TotalMs:          totalMs,
		ProxyOverheadMs:  preUpstreamMs,
	})
}
