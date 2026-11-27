package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
)

func shouldCompress(r *http.Request, cfg config.CompressionConfig) bool {
	if !cfg.Enabled {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Router-Compress")), "off")
}

func trimText(s string, minChars, headChars, tailChars int) (string, int, bool) {
	if minChars < 0 || headChars < 0 || tailChars < 0 {
		return s, 0, false
	}
	runes := []rune(s)
	if len(runes) <= minChars || headChars+tailChars >= len(runes) {
		return s, 0, false
	}
	trimmedCount := len(runes) - headChars - tailChars
	out := fmt.Sprintf("%s\n...[trimmed %d chars by llm-router]...\n%s", string(runes[:headChars]), trimmedCount, string(runes[len(runes)-tailChars:]))
	return out, trimmedCount, true
}

// trimOldOpenAIToolOutputs collapses large tool-role messages older than KeepRecentMessages.
// System/developer messages and recent turns are never modified.
func trimOldOpenAIToolOutputs(raw []byte, cfg config.CompressionConfig) ([]byte, int) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw, 0
	}
	msgs, ok := payload["messages"].([]any)
	if !ok || len(msgs) <= cfg.KeepRecentMessages {
		return raw, 0
	}

	cutoff := len(msgs) - cfg.KeepRecentMessages
	totalSaved := 0
	changed := false

	for i := 0; i < cutoff; i++ {
		m, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role != "tool" {
			continue
		}
		content, ok := m["content"].(string)
		if !ok {
			continue
		}
		if trimmed, saved, did := trimText(content, cfg.MinToolResultChars, cfg.HeadChars, cfg.TailChars); did {
			m["content"] = trimmed
			totalSaved += saved
			changed = true
		}
	}

	if !changed {
		return raw, 0
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return raw, 0
	}
	return encoded, totalSaved
}

// trimOldAnthropicToolResults collapses large tool_result blocks older than KeepRecentMessages.
func trimOldAnthropicToolResults(raw []byte, cfg config.CompressionConfig) ([]byte, int) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw, 0
	}
	msgs, ok := payload["messages"].([]any)
	if !ok || len(msgs) <= cfg.KeepRecentMessages {
		return raw, 0
	}

	cutoff := len(msgs) - cfg.KeepRecentMessages
	totalSaved := 0
	changed := false

	for i := 0; i < cutoff; i++ {
		m, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, blkAny := range blocks {
			blk, ok := blkAny.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := blk["type"].(string); t != "tool_result" {
				continue
			}
			if contentStr, ok := blk["content"].(string); ok {
				if trimmed, saved, did := trimText(contentStr, cfg.MinToolResultChars, cfg.HeadChars, cfg.TailChars); did {
					blk["content"] = trimmed
					totalSaved += saved
					changed = true
				}
			}
		}
	}

	if !changed {
		return raw, 0
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return raw, 0
	}
	return encoded, totalSaved
}

// injectAnthropicCacheControl adds {"type":"ephemeral"} cache_control breakpoints to system and the final tool.
func injectAnthropicCacheControl(raw []byte, enabled bool) []byte {
	if !enabled {
		return raw
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw
	}

	changed := false
	ephemeral := map[string]any{"type": "ephemeral"}

	if sys, exists := payload["system"]; exists {
		switch v := sys.(type) {
		case string:
			if v != "" {
				payload["system"] = []any{
					map[string]any{
						"type":          "text",
						"text":          v,
						"cache_control": ephemeral,
					},
				}
				changed = true
			}
		case []any:
			if len(v) > 0 {
				if last, ok := v[len(v)-1].(map[string]any); ok {
					if _, hasCC := last["cache_control"]; !hasCC {
						last["cache_control"] = ephemeral
						changed = true
					}
				}
			}
		}
	}

	if tools, ok := payload["tools"].([]any); ok && len(tools) > 0 {
		if lastTool, ok := tools[len(tools)-1].(map[string]any); ok {
			if _, hasCC := lastTool["cache_control"]; !hasCC {
				lastTool["cache_control"] = ephemeral
				changed = true
			}
		}
	}

	if !changed {
		return raw
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return encoded
}
