package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
)

func TestTrimOldOpenAIToolOutputs(t *testing.T) {
	cfg := config.CompressionConfig{
		Enabled:            true,
		KeepRecentMessages: 2,
		MinToolResultChars: 100,
		HeadChars:          20,
		TailChars:          20,
	}

	largeOld := strings.Repeat("A", 150)
	largeRecent := strings.Repeat("B", 150)
	req := map[string]any{
		"model": "gpt-4o",
		"messages": []map[string]any{
			{"role": "system", "content": strings.Repeat("S", 150)},
			{"role": "tool", "content": largeOld},
			{"role": "tool", "content": "small"},
			{"role": "user", "content": "next"},
			{"role": "tool", "content": largeRecent},
		},
	}
	raw, _ := json.Marshal(req)

	out, saved := trimOldOpenAIToolOutputs(raw, cfg)
	if saved != 110 {
		t.Fatalf("expected 110 saved chars, got %d", saved)
	}

	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.Messages[1].Content, "trimmed 110 chars") {
		t.Fatalf("expected old tool output to be trimmed, got %q", parsed.Messages[1].Content)
	}
	if parsed.Messages[2].Content != "small" {
		t.Fatalf("expected small tool output untouched")
	}
	if parsed.Messages[4].Content != largeRecent {
		t.Fatalf("expected recent tool output untouched")
	}
}

func TestTrimOldAnthropicToolResultsAndInjectCacheControl(t *testing.T) {
	cfg := config.CompressionConfig{
		Enabled:               true,
		KeepRecentMessages:    1,
		MinToolResultChars:    100,
		HeadChars:             15,
		TailChars:             15,
		AnthropicCacheControl: true,
	}

	largeOld := strings.Repeat("X", 130)
	req := map[string]any{
		"model":  "claude-sonnet-4-20250514",
		"system": "You are a helpful coding assistant.",
		"tools": []map[string]any{
			{"name": "read_file", "description": "read"},
			{"name": "write_file", "description": "write"},
		},
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "tool_result", "tool_use_id": "1", "content": largeOld},
				},
			},
			{"role": "user", "content": "latest question"},
		},
	}
	raw, _ := json.Marshal(req)

	trimmed, saved := trimOldAnthropicToolResults(raw, cfg)
	if saved != 100 {
		t.Fatalf("expected 100 saved chars, got %d", saved)
	}
	boosted := injectAnthropicCacheControl(trimmed, cfg.AnthropicCacheControl)

	var parsed struct {
		System []map[string]any `json:"system"`
		Tools  []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(boosted, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.System) != 1 || parsed.System[0]["cache_control"] == nil {
		t.Fatalf("expected system cache_control injected, got %+v", parsed.System)
	}
	if parsed.Tools[0]["cache_control"] != nil || parsed.Tools[1]["cache_control"] == nil {
		t.Fatalf("expected cache_control only on final tool, got %+v", parsed.Tools)
	}
}
