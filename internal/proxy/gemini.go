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

func extractGeminiMeta(body []byte) (systemPrompt string, hasTools bool, hasReasoning bool) {
	var payload struct {
		SystemInstruction *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		Tools            json.RawMessage `json:"tools"`
		GenerationConfig struct {
			ThinkingConfig json.RawMessage `json:"thinkingConfig"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if payload.SystemInstruction != nil {
			var sb strings.Builder
			for _, p := range payload.SystemInstruction.Parts {
				sb.WriteString(p.Text)
			}
			systemPrompt = sb.String()
		}
		if len(payload.Tools) > 0 && string(payload.Tools) != "null" && string(payload.Tools) != "[]" {
			hasTools = true
		}
		if len(payload.GenerationConfig.ThinkingConfig) > 0 && string(payload.GenerationConfig.ThinkingConfig) != "null" {
			hasReasoning = true
		}
	}
	return systemPrompt, hasTools, hasReasoning
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

	isStream := action == "streamGenerateContent"
	sysPrompt, hasTools, hasReasoning := extractGeminiMeta(reqBody)
	routeDec := s.engine.Evaluate(router.RequestContext{
		APIFormat:          "gemini_native",
		RequestedModel:     model,
		Headers:            r.Header,
		SystemPrompt:       sysPrompt,
		EstimatedTokens:    len(reqBody) / 4,
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

	lookupReq := s.buildCacheLookupReq(r, projectID, "gemini_native", routeDec, isStream, hasTools, reqBody)
	lookupRes, _ := s.cache.Lookup(r.Context(), lookupReq)
	if lookupRes.Hit {
		s.serveCacheHit(r.Context(), w, start, projectID, sessionID, "gemini_native", "gemini", model, routeDec, lookupRes)
		return
	}

	candidates := s.buildCandidates(routeDec, budgetDec)
	preUpstreamMs := time.Since(start).Milliseconds()

	resp, winner, fallbackAttempts, err := executeWithFallbackTracked(r.Context(), candidates, s.tracker, s.cfg.Providers, func(t router.RouteTarget) (*http.Request, error) {
		pCfg, exists := s.cfg.Providers[t.Provider]
		if !exists {
			return nil, fmt.Errorf("provider %q not configured", t.Provider)
		}

		// Cross-protocol offload: Gemini Native -> OpenAI-compatible (e.g. Ollama)
		if pCfg.Type == "openai" || pCfg.Type == "openai_compatible" {
			oaiBody, convErr := geminiToOpenAIRequest(reqBody, t.Model, isStream)
			if convErr != nil {
				return nil, convErr
			}
			targetURL := strings.TrimRight(pCfg.BaseURL, "/") + "/chat/completions"
			upReq, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(oaiBody))
			if reqErr != nil {
				return nil, reqErr
			}
			upReq.Header.Set("Content-Type", "application/json")
			if key := s.cfg.ResolveAPIKey(t.Provider); key != "" {
				upReq.Header.Set("Authorization", "Bearer "+key)
			}
			return upReq, nil
		}

		targetURL := strings.TrimRight(pCfg.BaseURL, "/") + "/v1beta/models/" + t.Model + ":" + action
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}
		upReq, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(reqBody))
		if reqErr != nil {
			return nil, reqErr
		}
		copyRequestHeaders(r.Header, upReq.Header)
		if upReq.Header.Get("x-goog-api-key") == "" {
			if key := s.cfg.ResolveAPIKey(t.Provider); key != "" {
				upReq.Header.Set("x-goog-api-key", key)
			}
		}
		return upReq, nil
	}, s.client)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	winCfg := s.cfg.Providers[winner.Provider]
	translated := winCfg.Type == "openai" || winCfg.Type == "openai_compatible"

	copyResponseHeaders(resp.Header, w.Header())
	if translated {
		if isStream && resp.StatusCode == http.StatusOK {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
	}
	w.Header().Set("X-Router-Provider", winner.Provider)
	w.Header().Set("X-Router-Model", winner.Model)
	if routeDec.RuleName != "" {
		w.Header().Set("X-Router-Rule", routeDec.RuleName)
	}
	w.WriteHeader(resp.StatusCode)

	var usage geminiUsageMetadata
	var firstByte time.Time
	var capturedBody []byte

	if translated {
		if isStream && resp.StatusCode == http.StatusOK && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			flusher, _ := w.(http.Flusher)
			var capBuf bytes.Buffer
			firstByte, _ = forwardSSETransform(resp.Body, func(payload []byte) {
				gemChunk, u := openAISSEChunkToGemini(payload)
				if u != nil {
					usage = *u
				}
				if len(gemChunk) > 0 {
					if firstByte.IsZero() {
						firstByte = time.Now()
					}
					line := fmt.Sprintf("data: %s\n\n", gemChunk)
					_, _ = io.WriteString(w, line)
					if capBuf.Len()+len(line) <= defaultMaxCaptureBytes {
						capBuf.WriteString(line)
					}
					if flusher != nil {
						flusher.Flush()
					}
				}
			})
			capturedBody = capBuf.Bytes()
		} else {
			respBytes, readErr := io.ReadAll(resp.Body)
			firstByte = time.Now()
			if readErr == nil && resp.StatusCode == http.StatusOK {
				gemBytes, u, convErr := openAIToGeminiResponse(respBytes)
				if convErr == nil {
					usage = u
					capturedBody = gemBytes
					_, _ = w.Write(gemBytes)
				} else {
					_, _ = w.Write(respBytes)
				}
			} else if readErr == nil {
				_, _ = w.Write(respBytes)
			}
		}
	} else {
		if isStream && resp.StatusCode == http.StatusOK && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			firstByte, capturedBody, _ = forwardSSEWithCapture(w, resp.Body, defaultMaxCaptureBytes, func(payload []byte) {
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
				if resp.StatusCode == http.StatusOK {
					capturedBody = respBytes
				}
				_, _ = w.Write(respBytes)
			}
		}
	}

	totalMs := time.Since(start).Milliseconds()
	ttfbMs := totalMs
	if !firstByte.IsZero() {
		ttfbMs = firstByte.Sub(start).Milliseconds()
	}

	completionTokens := usage.CandidatesTokenCount + usage.ThoughtsTokenCount
	if resp.StatusCode == http.StatusOK && len(capturedBody) > 0 {
		s.storeCacheResponse(r.Context(), lookupReq, lookupRes, w.Header().Get("Content-Type"), capturedBody, usage.PromptTokenCount, completionTokens)
	}
	pricing, _ := s.cfg.LookupPricing(winner.Provider, winner.Model)
	cost := telemetry.CalculateCost(pricing, usage.PromptTokenCount, completionTokens, usage.CachedContentTokenCount)
	baselineCost, savedCost, savedTokens := s.computeSavings("gemini", model, winner.Provider, winner.Model, usage.PromptTokenCount, completionTokens, usage.CachedContentTokenCount, cost)

	_ = s.guard.Record(r.Context(), projectID, sessionID, usage.PromptTokenCount+completionTokens, cost)
	_ = s.recorder.Record(r.Context(), storage.RequestLog{
		TsUTC:            start.UTC().UnixMilli(),
		ProjectID:        projectID,
		SessionID:        sessionID,
		APIFormat:        "gemini_native",
		RequestedModel:   model,
		RoutedProvider:   winner.Provider,
		RoutedModel:      winner.Model,
		RouteRule:        routeDec.RuleName,
		FallbackAttempts: fallbackAttempts,
		IsStream:         isStream,
		StatusCode:       resp.StatusCode,
		CacheStatus:      "miss",
		PromptTokens:     usage.PromptTokenCount,
		CompletionTokens: completionTokens,
		CachedTokens:     usage.CachedContentTokenCount,
		EstimatedCostUSD: cost,
		BaselineCostUSD:  baselineCost,
		SavedCostUSD:     savedCost,
		SavedTokens:      savedTokens,
		TTFBMs:           ttfbMs,
		TotalMs:          totalMs,
		ProxyOverheadMs:  preUpstreamMs,
	})
}
