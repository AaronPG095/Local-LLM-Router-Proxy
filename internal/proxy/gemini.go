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

type geminiUsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
}

type geminiUsageEnvelope struct {
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
}

func parseGeminiPath(urlPath string) (model string, action string, ok bool) {
	rest := strings.TrimPrefix(urlPath, "/v1beta/models/")
	if rest == urlPath || rest == "" {
		return "", "", false
	}
	idx := strings.IndexByte(rest, ':')
	if idx <= 0 || idx >= len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

func detectGeminiNeverDowngrade(r *http.Request, body []byte, cfg config.NeverDowngradeConfig) bool {
	if hasNeverDowngradeHeader(r, cfg) {
		return true
	}
	if !cfg.WhenReasoningParamsPresent {
		return false
	}
	var payload struct {
		GenerationConfig struct {
			ThinkingConfig json.RawMessage `json:"thinkingConfig"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if len(payload.GenerationConfig.ThinkingConfig) > 0 && string(payload.GenerationConfig.ThinkingConfig) != "null" {
			return true
		}
	}
	return false
}

func (s *Server) handleGemini(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	model, action, ok := parseGeminiPath(r.URL.Path)
	if !ok || (action != "generateContent" && action != "streamGenerateContent") {
		writeJSONError(w, http.StatusNotFound, "unsupported gemini endpoint")
		return
	}

	reqBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	providerName := "gemini"
	pCfg, exists := s.cfg.Providers[providerName]
	if !exists {
		writeJSONError(w, http.StatusBadGateway, "gemini provider not configured")
		return
	}

	projectID, sessionID := extractScopes(r)
	neverDowngrade := detectGeminiNeverDowngrade(r, reqBody, s.cfg.Routing.NeverDowngrade)
	isStream := action == "streamGenerateContent"

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

	targetURL := strings.TrimRight(pCfg.BaseURL, "/") + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(reqBody))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "build upstream request failed")
		return
	}
	copyRequestHeaders(r.Header, upReq.Header)
	if upReq.Header.Get("x-goog-api-key") == "" {
		if key := s.cfg.ResolveAPIKey(providerName); key != "" {
			upReq.Header.Set("x-goog-api-key", key)
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
	w.Header().Set("X-Router-Model", model)
	w.WriteHeader(resp.StatusCode)

	var usage geminiUsageMetadata
	var firstByte time.Time

	if isStream && resp.StatusCode == http.StatusOK && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		firstByte, err = forwardSSE(w, resp.Body, func(payload []byte) {
			var env geminiUsageEnvelope
			if json.Unmarshal(payload, &env) == nil && env.UsageMetadata != nil {
				usage = *env.UsageMetadata
			}
		})
	} else {
		respBytes, readErr := io.ReadAll(resp.Body)
		firstByte = time.Now()
		if readErr == nil {
			var env geminiUsageEnvelope
			if json.Unmarshal(respBytes, &env) == nil && env.UsageMetadata != nil {
				usage = *env.UsageMetadata
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

	completionTokens := usage.CandidatesTokenCount + usage.ThoughtsTokenCount
	pricing, _ := s.cfg.LookupPricing(providerName, model)
	cost := telemetry.CalculateCost(pricing, usage.PromptTokenCount, completionTokens, usage.CachedContentTokenCount)

	_ = s.guard.Record(r.Context(), projectID, sessionID, usage.PromptTokenCount+completionTokens, cost)
	_ = s.recorder.Record(r.Context(), storage.RequestLog{
		TsUTC:            start.UTC().UnixMilli(),
		ProjectID:        projectID,
		SessionID:        sessionID,
		APIFormat:        "gemini_native",
		RequestedModel:   model,
		RoutedProvider:   providerName,
		RoutedModel:      model,
		IsStream:         isStream,
		StatusCode:       resp.StatusCode,
		CacheStatus:      "miss",
		PromptTokens:     usage.PromptTokenCount,
		CompletionTokens: completionTokens,
		CachedTokens:     usage.CachedContentTokenCount,
		EstimatedCostUSD: cost,
		BaselineCostUSD:  cost,
		TTFBMs:           ttfbMs,
		TotalMs:          totalMs,
		ProxyOverheadMs:  preUpstreamMs,
	})
}
