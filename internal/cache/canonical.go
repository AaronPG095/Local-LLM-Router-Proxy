package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ShouldBypass returns true if request headers explicitly request bypassing cache reads.
func ShouldBypass(h http.Header) bool {
	if strings.EqualFold(strings.TrimSpace(h.Get("X-Router-Cache")), "bypass") {
		return true
	}
	cc := strings.ToLower(h.Get("Cache-Control"))
	return strings.Contains(cc, "no-cache") || strings.Contains(cc, "no-store")
}

// IsDeterministic returns true when L1 exact caching is permitted for the request.
func IsDeterministic(body []byte, h http.Header, requireDeterministic bool) bool {
	if !requireDeterministic {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(h.Get("X-Router-Cache")), "force") {
		return true
	}

	var payload struct {
		Temperature      *float64 `json:"temperature"`
		GenerationConfig *struct {
			Temperature *float64 `json:"temperature"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if payload.Temperature != nil && *payload.Temperature == 0 {
			return true
		}
		if payload.GenerationConfig != nil && payload.GenerationConfig.Temperature != nil && *payload.GenerationConfig.Temperature == 0 {
			return true
		}
	}
	return false
}

// CanonicalKey produces a deterministic SHA-256 hex key for the request tuple.
func CanonicalKey(apiFormat, targetModel string, isStream bool, body []byte) string {
	var parsed any
	canonicalBody := body
	if err := json.Unmarshal(body, &parsed); err == nil {
		if encoded, err := json.Marshal(parsed); err == nil {
			canonicalBody = encoded
		}
	}

	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%s|%t|", apiFormat, targetModel, isStream)
	_, _ = h.Write(canonicalBody)
	return hex.EncodeToString(h.Sum(nil))
}

// ExtractPromptText extracts the user/system prompt text for L2 semantic embedding.
func ExtractPromptText(apiFormat string, body []byte) string {
	switch apiFormat {
	case "gemini_native":
		var g struct {
			SystemInstruction *struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"systemInstruction"`
			Contents []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
		}
		if json.Unmarshal(body, &g) == nil {
			var parts []string
			if g.SystemInstruction != nil {
				for _, p := range g.SystemInstruction.Parts {
					if p.Text != "" {
						parts = append(parts, p.Text)
					}
				}
			}
			for _, c := range g.Contents {
				for _, p := range c.Parts {
					if p.Text != "" {
						parts = append(parts, p.Text)
					}
				}
			}
			return strings.TrimSpace(strings.Join(parts, "\n"))
		}

	case "openai_chat":
		var o struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &o) == nil {
			var parts []string
			for _, m := range o.Messages {
				if m.Content != "" {
					parts = append(parts, m.Content)
				}
			}
			return strings.TrimSpace(strings.Join(parts, "\n"))
		}

	case "openai_responses":
		var r struct {
			Instructions string          `json:"instructions"`
			Input        json.RawMessage `json:"input"`
		}
		if json.Unmarshal(body, &r) == nil {
			var parts []string
			if r.Instructions != "" {
				parts = append(parts, r.Instructions)
			}
			var inputStr string
			if json.Unmarshal(r.Input, &inputStr) == nil && inputStr != "" {
				parts = append(parts, inputStr)
			} else {
				var items []struct {
					Content string `json:"content"`
				}
				if json.Unmarshal(r.Input, &items) == nil {
					for _, it := range items {
						if it.Content != "" {
							parts = append(parts, it.Content)
						}
					}
				}
			}
			return strings.TrimSpace(strings.Join(parts, "\n"))
		}

	case "anthropic_messages":
		var a struct {
			System   json.RawMessage `json:"system"`
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &a) == nil {
			var parts []string
			var sysStr string
			if json.Unmarshal(a.System, &sysStr) == nil && sysStr != "" {
				parts = append(parts, sysStr)
			}
			for _, m := range a.Messages {
				var s string
				if json.Unmarshal(m.Content, &s) == nil && s != "" {
					parts = append(parts, s)
				}
			}
			return strings.TrimSpace(strings.Join(parts, "\n"))
		}
	}
	return ""
}
