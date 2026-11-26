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

func extractResponsesMeta(raw []byte) (model string, isStream bool, systemPrompt string, hasTools bool, hasReasoning bool) {
	var payload struct {
		Model        string          `json:"model"`
		Stream       bool            `json:"stream"`
		Instructions string          `json:"instructions"`
		Tools        json.RawMessage `json:"tools"`
		Reasoning    json.RawMessage `json:"reasoning"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil {
		model = payload.Model
		isStream = payload.Stream
		systemPrompt = payload.Instructions
		if len(payload.Tools) > 0 && string(payload.Tools) != "null" && string(payload.Tools) != "[]" {
			hasTools = true
		}
		if len(payload.Reasoning) > 0 && string(payload.Reasoning) != "null" {
			hasReasoning = true
		}
	}
	return model, isStream, systemPrompt, hasTools, hasReasoning
}

func (s *Server) handleOpenAIResponses(w http.ResponseWriter, r *http.Request) {
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

	requestedModel, isStream, sysPrompt, hasTools, hasReasoning := extractResponsesMeta(rawBody)
	routeDec := s.engine.Evaluate(router.RequestContext{
		APIFormat:          "openai_responses",
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

	lookupReq := s.buildCacheLookupReq(r, projectID, "openai_responses", routeDec, isStream, hasTools, rawBody)
	lookupRes, _ := s.cache.Lookup(r.Context(), lookupReq)
	if lookupRes.Hit {
		s.serveCacheHit(r.Context(), w, start, projectID, sessionID, "openai_responses", "openai", requestedModel, routeDec, lookupRes)
		return
	}

	candidates := s.buildCandidates(routeDec, budgetDec)
	preUpstreamMs := time.Since(start).Milliseconds()

	resp, winner, fallbackAttempts, err := executeWithFallback(r.Context(), candidates, func(t router.RouteTarget) (*http.Request, error) {
		pCfg, exists := s.cfg.Providers[t.Provider]
		if !exists {
			return nil, fmt.Errorf("provider %q not configured", t.Provider)
		}

		// Local Ollama offload: translate /v1/responses -> /v1/chat/completions
		if pCfg.Type == "openai_compatible" {
			chatBody, convErr := responsesToOpenAIChatRequest(rawBody, t.Model, isStream)
			if convErr != nil {
				return nil, convErr
			}
			targetURL := strings.TrimRight(pCfg.BaseURL, "/") + "/chat/completions"
			upReq, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(chatBody))
			if reqErr != nil {
				return nil, reqErr
			}
			upReq.Header.Set("Content-Type", "application/json")
			if key := s.cfg.ResolveAPIKey(t.Provider); key != "" {
				upReq.Header.Set("Authorization", "Bearer "+key)
			}
			return upReq, nil
		}

		bodyToSend := rawBody
		if t.Model != requestedModel && t.Model != "" {
			bodyToSend = rewriteOpenAIModel(rawBody, t.Model)
		}
		targetURL := strings.TrimRight(pCfg.BaseURL, "/") + "/responses"
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

	winCfg := s.cfg.Providers[winner.Provider]
	translated := winCfg.Type == "openai_compatible"

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

	var usage responsesUsage
	var firstByte time.Time
	var capturedBody []byte

	if translated {
		if isStream && resp.StatusCode == http.StatusOK && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			flusher, _ := w.(http.Flusher)
			var capBuf bytes.Buffer
			firstByte, _ = forwardSSETransform(resp.Body, func(payload []byte) {
				var chunk struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
					Usage *openAIUsage `json:"usage"`
				}
				if json.Unmarshal(payload, &chunk) != nil {
					return
				}
				if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
					ev, _ := json.Marshal(map[string]any{
						"type":  "response.output_text.delta",
						"delta": chunk.Choices[0].Delta.Content,
					})
					line := fmt.Sprintf("event: response.output_text.delta\ndata: %s\n\n", ev)
					_, _ = io.WriteString(w, line)
					if capBuf.Len()+len(line) <= defaultMaxCaptureBytes {
						capBuf.WriteString(line)
					}
					if flusher != nil {
						flusher.Flush()
					}
				}
				if chunk.Usage != nil {
					usage.InputTokens = chunk.Usage.PromptTokens
					usage.OutputTokens = chunk.Usage.CompletionTokens
					usage.TotalTokens = chunk.Usage.PromptTokens + chunk.Usage.CompletionTokens
					usage.InputTokensDetails.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
					ev, _ := json.Marshal(map[string]any{
						"type": "response.completed",
						"response": map[string]any{
							"status": "completed",
							"usage":  usage,
						},
					})
					line := fmt.Sprintf("event: response.completed\ndata: %s\n\n", ev)
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
				rBytes, u, convErr := openAIToResponsesResponse(respBytes, winner.Model)
				if convErr == nil {
					usage = u
					capturedBody = rBytes
					_, _ = w.Write(rBytes)
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
				var ev struct {
					Type     string `json:"type"`
					Response *struct {
						Usage *responsesUsage `json:"usage"`
					} `json:"response"`
					Usage *responsesUsage `json:"usage"`
				}
				if json.Unmarshal(payload, &ev) == nil {
					if ev.Response != nil && ev.Response.Usage != nil {
						usage = *ev.Response.Usage
					} else if ev.Usage != nil {
						usage = *ev.Usage
					}
				}
			})
		} else {
			respBytes, readErr := io.ReadAll(resp.Body)
			firstByte = time.Now()
			if readErr == nil {
				var env struct {
					Usage *responsesUsage `json:"usage"`
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
	cost := telemetry.CalculateCost(pricing, usage.InputTokens, usage.OutputTokens, usage.InputTokensDetails.CachedTokens)
	baselineCost, savedCost, savedTokens := s.computeSavings("openai", requestedModel, winner.Provider, winner.Model, usage.InputTokens, usage.OutputTokens, usage.InputTokensDetails.CachedTokens, cost)

	_ = s.guard.Record(r.Context(), projectID, sessionID, usage.InputTokens+usage.OutputTokens, cost)
	_ = s.recorder.Record(r.Context(), storage.RequestLog{
		TsUTC:            start.UTC().UnixMilli(),
		ProjectID:        projectID,
		SessionID:        sessionID,
		APIFormat:        "openai_responses",
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
		CachedTokens:     usage.InputTokensDetails.CachedTokens,
		EstimatedCostUSD: cost,
		BaselineCostUSD:  baselineCost,
		SavedCostUSD:     savedCost,
		SavedTokens:      savedTokens,
		TTFBMs:           ttfbMs,
		TotalMs:          totalMs,
		ProxyOverheadMs:  preUpstreamMs,
	})
}
