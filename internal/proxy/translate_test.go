package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGeminiToOpenAIAndBackTranslation(t *testing.T) {
	gemReq := `{
		"systemInstruction": {"parts": [{"text": "Write a concise git commit message."}]},
		"contents": [{"role": "user", "parts": [{"text": "diff --git a/main.go"}]}],
		"generationConfig": {"temperature": 0.2, "maxOutputTokens": 120}
	}`

	oaiReqBytes, err := geminiToOpenAIRequest([]byte(gemReq), "qwen2.5-coder:7b", false)
	if err != nil {
		t.Fatalf("geminiToOpenAIRequest: %v", err)
	}
	var oaiReq map[string]any
	if err := json.Unmarshal(oaiReqBytes, &oaiReq); err != nil {
		t.Fatal(err)
	}
	if oaiReq["model"] != "qwen2.5-coder:7b" {
		t.Errorf("expected model qwen2.5-coder:7b, got %v", oaiReq["model"])
	}
	msgs := oaiReq["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system+user), got %d", len(msgs))
	}

	oaiResp := `{"choices":[{"message":{"content":"feat: update main"},"finish_reason":"stop"}],"usage":{"prompt_tokens":42,"completion_tokens":8}}`
	gemRespBytes, usage, err := openAIToGeminiResponse([]byte(oaiResp))
	if err != nil {
		t.Fatalf("openAIToGeminiResponse: %v", err)
	}
	if usage.PromptTokenCount != 42 || usage.CandidatesTokenCount != 8 {
		t.Errorf("unexpected usage: %+v", usage)
	}
	if !strings.Contains(string(gemRespBytes), "feat: update main") {
		t.Errorf("expected translated text in Gemini response: %s", string(gemRespBytes))
	}

	sseChunk := `{"choices":[{"delta":{"content":"feat: "}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`
	gemChunk, gUsage := openAISSEChunkToGemini([]byte(sseChunk))
	if gUsage == nil || gUsage.PromptTokenCount != 10 {
		t.Errorf("expected usage from SSE chunk, got %+v", gUsage)
	}
	if !strings.Contains(string(gemChunk), "feat: ") {
		t.Errorf("expected delta text in Gemini SSE chunk: %s", string(gemChunk))
	}
}

func TestResponsesToOpenAIChatTranslation(t *testing.T) {
	req := `{"model":"gpt-4o","instructions":"Be brief.","input":"Summarize logs","stream":true}`
	out, err := responsesToOpenAIChatRequest([]byte(req), "qwen2.5-coder:7b", true)
	if err != nil {
		t.Fatalf("responsesToOpenAIChatRequest: %v", err)
	}
	if !strings.Contains(string(out), `"include_usage":true`) || !strings.Contains(string(out), `"Be brief."`) {
		t.Fatalf("unexpected translated responses request: %s", string(out))
	}
}
