package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
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

func detectOpenAINeverDowngrade(r *http.Request, body []byte, cfg config.NeverDowngradeConfig) bool {
	if hasNeverDowngradeHeader(r, cfg) {
		return true
	}
	if !cfg.WhenReasoningParamsPresent {
		return false
	}
	var payload struct {
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.ReasoningEffort != "" {
		return true
	}
	return false
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

func (s *Server) resolveOpenAIProvider(requestedModel string) (string, config.ProviderConfig, string, bool) {
	// Direct prefix syntax: "ollama:qwen2.5-coder:7b" or "openai:gpt-4o"
	if idx := strings.IndexByte(requestedModel, ':'); idx > 0 {
		prefix := requestedModel[:idx]
		if p, ok := s.cfg.Providers[prefix]; ok {
			return prefix, p, requestedModel[idx+1:], true
		}
	}
	// Check if model pricing explicitly exists under ollama vs openai
	if p, ok := s.cfg.Providers["ollama"]; ok {
		if _, hasModel := p.PricingPer1M[requestedModel]; hasModel {
			return "ollama", p, requestedModel, true
		}
	}
	if p, ok := s.cfg.Providers["openai"]; ok {
		return "openai", p, requestedModel, true
	}
	if p, ok := s.cfg.Providers["ollama"]; ok {
		return "ollama", p, requestedModel, true
	}
	return "", config.ProviderConfig{}, requestedModel, false
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
	providerName, pCfg, targetModel, ok := s.resolveOpenAIProvider(requestedModel)
	if !ok {
		writeJSONError(w, http.StatusBadGateway, "no openai-compatible provider configured")
		return
	}

	projectID, sessionID := extractScopes(r)
	neverDowngrade := detectOpenAINeverDowngrade(r, rawBody, s.cfg.Routing.NeverDowngrade)

	decision, err := s.guard.Check(r.Context(), projectID, sessionID, neverDowngrade)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "budget check failed")
		return
	}
	if !decision.Allowed {
		w.Header().Set("X-Router-Budget-Blocked", "true")
		writeJSONError(w, http.StatusTooManyRequests, decision.BlockReason)
		return
	}
	if decision.SoftWarn {
		w.Header().Set("X-Router-Budget-Warning", decision.WarningMsg)
	}
	if decision.Action == "downgrade" && decision.DowngradeTarget != "" {
		if dp, dpCfg, dm, ok := s.resolveOpenAIProvider(decision.DowngradeTarget); ok {
			providerName, pCfg, targetModel = dp, dpCfg, dm
			reqBody = rewriteOpenAIModel(reqBody, targetModel)
		}
	} else if targetModel != requestedModel {
		reqBody = rewriteOpenAIModel(reqBody, targetModel)
	}

	targetURL := strings.TrimRight(pCfg.BaseURL, "/") + "/chat/completions"
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(reqBody))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "build upstream request failed")
		return
	}
	copyRequestHeaders(r.Header, upReq.Header)
	if upReq.Header.Get("Authorization") == "" {
		if key := s.cfg.ResolveAPIKey(providerName); key != "" {
			upReq.Header.Set("Authorization", "Bearer "+key)
		}
	}

	preUpstreamMs := time.Since(start).Milliseconds()
	resp, err := s.client.Do(upReq)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(resp.Header, w.Header())
	w.Header().Set("X-Router-Provider", providerName)
	w.Header().Set("X-Router-Model", targetModel)
	w.WriteHeader(resp.StatusCode)

	var usage openAIUsage
	var firstByte time.Time

	if isStream && resp.StatusCode == http.StatusOK && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		firstByte, err = forwardSSE(w, resp.Body, func(payload []byte) {
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
			_, _ = w.Write(respBytes)
		}
		err = readErr
	}

	totalMs := time.Since(start).Milliseconds()
	ttfbMs := totalMs
	if !firstByte.IsZero() {
		ttfbMs = firstByte.Sub(start).Milliseconds()
	}

	pricing, _ := s.cfg.LookupPricing(providerName, targetModel)
	cost := telemetry.CalculateCost(pricing, usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens)

	_ = s.guard.Record(r.Context(), projectID, sessionID, usage.PromptTokens+usage.CompletionTokens, cost)
	_ = s.recorder.Record(r.Context(), storage.RequestLog{
		TsUTC:            start.UTC().UnixMilli(),
		ProjectID:        projectID,
		SessionID:        sessionID,
		APIFormat:        "openai_chat",
		RequestedModel:   requestedModel,
		RoutedProvider:   providerName,
		RoutedModel:      targetModel,
		IsStream:         isStream,
		StatusCode:       resp.StatusCode,
		CacheStatus:      "miss",
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CachedTokens:     usage.PromptTokensDetails.CachedTokens,
		EstimatedCostUSD: cost,
		BaselineCostUSD:  cost,
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
