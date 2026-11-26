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

type openAIUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type openAIEnvelope struct {
	Usage *openAIUsage `json:"usage"`
}

func extractOpenAIMeta(raw []byte) (systemPrompt string, hasTools bool, hasReasoning bool) {
	var payload struct {
		ReasoningEffort string          `json:"reasoning_effort"`
		Tools           json.RawMessage `json:"tools"`
		Messages        []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil {
		if payload.ReasoningEffort != "" {
			hasReasoning = true
		}
		if len(payload.Tools) > 0 && string(payload.Tools) != "null" && string(payload.Tools) != "[]" {
			hasTools = true
		}
		var sb strings.Builder
		for _, m := range payload.Messages {
			if m.Role == "system" || m.Role == "developer" {
				sb.WriteString(m.Content)
			}
		}
		systemPrompt = sb.String()
	}
	return systemPrompt, hasTools, hasReasoning
}

// prepareOpenAIBody extracts model/stream flags and injects stream_options.include_usage=true
// when streaming so the proxy always receives final token usage.
func prepareOpenAIBody(raw []byte) (modified []byte, model string, isStream bool) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw, "", false
	}
	if v, ok := m["model"].(string); ok {
		model = v
	}
	if v, ok := m["stream"].(bool); ok {
		isStream = v
	}
	if isStream {
		if _, hasOpts := m["stream_options"]; !hasOpts {
			m["stream_options"] = map[string]any{"include_usage": true}
			if encoded, err := json.Marshal(m); err == nil {
				return encoded, model, isStream
			}
		}
	}
	return raw, model, isStream
}

func (s *Server) handleOpenAIChat(w http.ResponseWriter, r *http.Request) {
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

	reqBody, requestedModel, isStream := prepareOpenAIBody(rawBody)
	sysPrompt, hasTools, hasReasoning := extractOpenAIMeta(rawBody)

	routeDec := s.engine.Evaluate(router.RequestContext{
		APIFormat:          "openai_chat",
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

	lookupReq := s.buildCacheLookupReq(r, projectID, "openai_chat", routeDec, isStream, hasTools, rawBody)
	lookupRes, _ := s.cache.Lookup(r.Context(), lookupReq)
	if lookupRes.Hit {
		s.serveCacheHit(r.Context(), w, start, projectID, sessionID, "openai_chat", "openai", requestedModel, routeDec, lookupRes)
		return
	}

	candidates := s.buildCandidates(routeDec, budgetDec)
	preUpstreamMs := time.Since(start).Milliseconds()

	resp, winner, fallbackAttempts, err := executeWithFallback(r.Context(), candidates, func(t router.RouteTarget) (*http.Request, error) {
		pCfg, exists := s.cfg.Providers[t.Provider]
		if !exists {
			return nil, fmt.Errorf("provider %q not configured", t.Provider)
		}
		bodyToSend := reqBody
		if t.Model != requestedModel && t.Model != "" {
			bodyToSend = rewriteOpenAIModel(reqBody, t.Model)
		}
		targetURL := strings.TrimRight(pCfg.BaseURL, "/") + "/chat/completions"
		upReq, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(bodyToSend))
		if reqErr != nil {
			return nil, reqErr
		}
		copyRequestHeaders(r.Header, upReq.Header)
		if upReq.Header.Get("Authorization") == "" {
			if key := s.cfg.ResolveAPIKey(t.Provider); key != "" {
				upReq.Header.Set("Authorization", "Bearer "+key)
			}
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

	var usage openAIUsage
	var firstByte time.Time
	var capturedBody []byte

	if isStream && resp.StatusCode == http.StatusOK && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		firstByte, capturedBody, _ = forwardSSEWithCapture(w, resp.Body, defaultMaxCaptureBytes, func(payload []byte) {
			var env openAIEnvelope
			if json.Unmarshal(payload, &env) == nil && env.Usage != nil {
				usage = *env.Usage
			}
		})
	} else {
		respBytes, readErr := io.ReadAll(resp.Body)
		firstByte = time.Now()
		if readErr == nil {
			var env openAIEnvelope
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
		s.storeCacheResponse(r.Context(), lookupReq, lookupRes, w.Header().Get("Content-Type"), capturedBody, usage.PromptTokens, usage.CompletionTokens)
	}

	pricing, _ := s.cfg.LookupPricing(winner.Provider, winner.Model)
	cost := telemetry.CalculateCost(pricing, usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens)
	baselineCost, savedCost, savedTokens := s.computeSavings("openai", requestedModel, winner.Provider, winner.Model, usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, cost)

	_ = s.guard.Record(r.Context(), projectID, sessionID, usage.PromptTokens+usage.CompletionTokens, cost)
	_ = s.recorder.Record(r.Context(), storage.RequestLog{
		TsUTC:            start.UTC().UnixMilli(),
		ProjectID:        projectID,
		SessionID:        sessionID,
		APIFormat:        "openai_chat",
		RequestedModel:   requestedModel,
		RoutedProvider:   winner.Provider,
		RoutedModel:      winner.Model,
		RouteRule:        routeDec.RuleName,
		FallbackAttempts: fallbackAttempts,
		IsStream:         isStream,
		StatusCode:       resp.StatusCode,
		CacheStatus:      "miss",
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CachedTokens:     usage.PromptTokensDetails.CachedTokens,
		EstimatedCostUSD: cost,
		BaselineCostUSD:  baselineCost,
		SavedCostUSD:     savedCost,
		SavedTokens:      savedTokens,
		TTFBMs:           ttfbMs,
		TotalMs:          totalMs,
		ProxyOverheadMs:  preUpstreamMs,
	})
}

func rewriteOpenAIModel(raw []byte, newModel string) []byte {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	m["model"] = newModel
	encoded, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return encoded
}
