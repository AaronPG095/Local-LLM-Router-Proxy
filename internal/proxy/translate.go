package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
)

type openAIChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// geminiToOpenAIRequest translates a Gemini Native generateContent payload to OpenAI Chat Completions.
func geminiToOpenAIRequest(raw []byte, targetModel string, isStream bool) ([]byte, error) {
	var gReq struct {
		SystemInstruction *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig *struct {
			Temperature     *float64 `json:"temperature"`
			MaxOutputTokens *int     `json:"maxOutputTokens"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(raw, &gReq); err != nil {
		return nil, fmt.Errorf("parse gemini request: %w", err)
	}

	var msgs []openAIChatMsg
	if gReq.SystemInstruction != nil {
		var sb strings.Builder
		for _, p := range gReq.SystemInstruction.Parts {
			sb.WriteString(p.Text)
		}
		if sb.Len() > 0 {
			msgs = append(msgs, openAIChatMsg{Role: "system", Content: sb.String()})
		}
	}

	for _, c := range gReq.Contents {
		role := c.Role
		if role == "" {
			role = "user"
		} else if role == "model" {
			role = "assistant"
		}
		var sb strings.Builder
		for _, p := range c.Parts {
			sb.WriteString(p.Text)
		}
		msgs = append(msgs, openAIChatMsg{Role: role, Content: sb.String()})
	}

	out := map[string]any{
		"model":    targetModel,
		"messages": msgs,
		"stream":   isStream,
	}
	if isStream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if gReq.GenerationConfig != nil {
		if gReq.GenerationConfig.Temperature != nil {
			out["temperature"] = *gReq.GenerationConfig.Temperature
		}
		if gReq.GenerationConfig.MaxOutputTokens != nil {
			out["max_tokens"] = *gReq.GenerationConfig.MaxOutputTokens
		}
	}
	return json.Marshal(out)
}

// openAIToGeminiResponse converts a non-streaming OpenAI Chat Completions JSON response to Gemini Native JSON.
func openAIToGeminiResponse(raw []byte) ([]byte, geminiUsageMetadata, error) {
	var oResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *openAIUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &oResp); err != nil {
		return nil, geminiUsageMetadata{}, fmt.Errorf("parse openai response: %w", err)
	}

	var usage geminiUsageMetadata
	if oResp.Usage != nil {
		usage.PromptTokenCount = oResp.Usage.PromptTokens
		usage.CandidatesTokenCount = oResp.Usage.CompletionTokens
		usage.CachedContentTokenCount = oResp.Usage.PromptTokensDetails.CachedTokens
	}

	text := ""
	finishReason := "STOP"
	if len(oResp.Choices) > 0 {
		text = oResp.Choices[0].Message.Content
		if oResp.Choices[0].FinishReason == "length" {
			finishReason = "MAX_TOKENS"
		}
	}

	gResp := map[string]any{
		"candidates": []map[string]any{
			{
				"content": map[string]any{
					"role":  "model",
					"parts": []map[string]any{{"text": text}},
				},
				"finishReason": finishReason,
			},
		},
		"usageMetadata": usage,
	}
	encoded, err := json.Marshal(gResp)
	return encoded, usage, err
}

// openAISSEChunkToGemini converts an OpenAI SSE data payload into a Gemini Native SSE data payload.
// Returns nil payload if the chunk contains neither delta text nor usage metadata.
func openAISSEChunkToGemini(payload []byte) ([]byte, *geminiUsageMetadata) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *openAIUsage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return nil, nil
	}

	var usage *geminiUsageMetadata
	if chunk.Usage != nil {
		usage = &geminiUsageMetadata{
			PromptTokenCount:        chunk.Usage.PromptTokens,
			CandidatesTokenCount:    chunk.Usage.CompletionTokens,
			CachedContentTokenCount: chunk.Usage.PromptTokensDetails.CachedTokens,
		}
	}

	text := ""
	finishReason := ""
	if len(chunk.Choices) > 0 {
		text = chunk.Choices[0].Delta.Content
		if chunk.Choices[0].FinishReason == "stop" {
			finishReason = "STOP"
		} else if chunk.Choices[0].FinishReason == "length" {
			finishReason = "MAX_TOKENS"
		}
	}

	if text == "" && finishReason == "" && usage == nil {
		return nil, nil
	}

	out := map[string]any{}
	if text != "" || finishReason != "" {
		cand := map[string]any{
			"content": map[string]any{
				"role":  "model",
				"parts": []map[string]any{{"text": text}},
			},
		}
		if finishReason != "" {
			cand["finishReason"] = finishReason
		}
		out["candidates"] = []map[string]any{cand}
	}
	if usage != nil {
		out["usageMetadata"] = usage
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, usage
	}
	return encoded, usage
}

type responsesUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	TotalTokens        int64 `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// responsesToOpenAIChatRequest maps OpenAI Responses API (/v1/responses) to Chat Completions for Ollama offload.
func responsesToOpenAIChatRequest(raw []byte, targetModel string, isStream bool) ([]byte, error) {
	var rReq struct {
		Instructions    string          `json:"instructions"`
		Input           json.RawMessage `json:"input"`
		Temperature     *float64        `json:"temperature"`
		MaxOutputTokens *int            `json:"max_output_tokens"`
	}
	if err := json.Unmarshal(raw, &rReq); err != nil {
		return nil, fmt.Errorf("parse responses request: %w", err)
	}

	var msgs []openAIChatMsg
	if rReq.Instructions != "" {
		msgs = append(msgs, openAIChatMsg{Role: "system", Content: rReq.Instructions})
	}

	var inputStr string
	if err := json.Unmarshal(rReq.Input, &inputStr); err == nil {
		msgs = append(msgs, openAIChatMsg{Role: "user", Content: inputStr})
	} else {
		var items []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(rReq.Input, &items); err == nil {
			for _, it := range items {
				role := it.Role
				if role == "" {
					role = "user"
				}
				msgs = append(msgs, openAIChatMsg{Role: role, Content: it.Content})
			}
		}
	}

	out := map[string]any{
		"model":    targetModel,
		"messages": msgs,
		"stream":   isStream,
	}
	if isStream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if rReq.Temperature != nil {
		out["temperature"] = *rReq.Temperature
	}
	if rReq.MaxOutputTokens != nil {
		out["max_tokens"] = *rReq.MaxOutputTokens
	}
	return json.Marshal(out)
}

// openAIToResponsesResponse converts a non-streaming OpenAI Chat Completion JSON into an OpenAI Responses JSON object.
func openAIToResponsesResponse(raw []byte, model string) ([]byte, responsesUsage, error) {
	var oResp struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *openAIUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &oResp); err != nil {
		return nil, responsesUsage{}, fmt.Errorf("parse openai response: %w", err)
	}

	var usage responsesUsage
	if oResp.Usage != nil {
		usage.InputTokens = oResp.Usage.PromptTokens
		usage.OutputTokens = oResp.Usage.CompletionTokens
		usage.TotalTokens = oResp.Usage.PromptTokens + oResp.Usage.CompletionTokens
		usage.InputTokensDetails.CachedTokens = oResp.Usage.PromptTokensDetails.CachedTokens
	}

	text := ""
	if len(oResp.Choices) > 0 {
		text = oResp.Choices[0].Message.Content
	}

	respObj := map[string]any{
		"id":     "resp_" + oResp.ID,
		"object": "response",
		"status": "completed",
		"model":  model,
		"output": []map[string]any{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": text},
				},
			},
		},
		"usage": usage,
	}
	encoded, err := json.Marshal(respObj)
	return encoded, usage, err
}
