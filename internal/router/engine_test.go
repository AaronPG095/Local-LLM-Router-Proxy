package router

import (
	"net/http"
	"testing"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
)

func boolPtr(b bool) *bool { return &b }

func testRouterConfig() *config.Config {
	return &config.Config{
		Providers: map[string]config.ProviderConfig{
			"gemini":    {Type: "gemini", BaseURL: "https://generativelanguage.googleapis.com"},
			"openai":    {Type: "openai", BaseURL: "https://api.openai.com/v1"},
			"anthropic": {Type: "anthropic", BaseURL: "https://api.anthropic.com/v1"},
			"ollama":    {Type: "openai_compatible", BaseURL: "http://127.0.0.1:11434/v1"},
		},
		Routing: config.RoutingConfig{
			DefaultTarget: "gemini:gemini-2.5-flash",
			NeverDowngrade: config.NeverDowngradeConfig{
				Headers:                    map[string]string{"X-Router-Tier": "frontier"},
				ModelAliases:               []string{"frontier"},
				WhenReasoningParamsPresent: true,
			},
			Rules: []config.RoutingRule{
				{
					Name: "commit-and-log-trim",
					Match: config.MatchRule{
						Headers:  map[string][]string{"X-Router-Task": {"commit_msg", "log_trim"}},
						HasTools: boolPtr(false),
					},
					Target:        "ollama:qwen2.5-coder:7b",
					FallbackChain: []string{"gemini:gemini-2.5-flash", "openai:gpt-4o-mini"},
					SemanticCache: true,
				},
				{
					Name: "short-mechanical",
					Match: config.MatchRule{
						MaxEstimatedTokens: 600,
						SystemPromptRegex:  "(?i)(summarize|classify)",
						HasTools:           boolPtr(false),
					},
					Target:        "ollama:llama3.2:3b",
					FallbackChain: []string{"openai:gpt-4o-mini"},
				},
				{
					Name: "frontier-with-resilience",
					Match: config.MatchRule{
						ModelAlias: "frontier",
					},
					Target:        "anthropic:claude-sonnet-4-20250514",
					FallbackChain: []string{"openai:gpt-4o"},
				},
			},
		},
	}
}

func TestEnginePolicyMatchingAndNeverDowngrade(t *testing.T) {
	eng, err := NewEngine(testRouterConfig())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// 1. Header match -> ollama + fallbacks + semantic cache
	h := http.Header{}
	h.Set("X-Router-Task", "commit_msg")
	d := eng.Evaluate(RequestContext{
		APIFormat:      "gemini_native",
		RequestedModel: "gemini-2.5-flash",
		Headers:        h,
		HasTools:       false,
	})
	if d.RuleName != "commit-and-log-trim" || d.Primary.Provider != "ollama" || d.Primary.Model != "qwen2.5-coder:7b" {
		t.Fatalf("unexpected header match decision: %+v", d)
	}
	if len(d.Fallbacks) != 2 || !d.SemanticCacheAllowed {
		t.Fatalf("expected 2 fallbacks and SemanticCacheAllowed=true, got %+v", d)
	}

	// 2. Tools present bypasses has_tools:false rule
	dTools := eng.Evaluate(RequestContext{
		APIFormat:      "gemini_native",
		RequestedModel: "gemini-2.5-flash",
		Headers:        h,
		HasTools:       true,
	})
	if dTools.RuleName != "" || dTools.Primary.Provider != "gemini" {
		t.Fatalf("expected pass-through when HasTools=true, got %+v", dTools)
	}

	// 3. MaxEstimatedTokens + SystemPromptRegex match
	dShort := eng.Evaluate(RequestContext{
		APIFormat:       "openai_chat",
		RequestedModel:  "gpt-4o",
		SystemPrompt:    "Please summarize this diff",
		EstimatedTokens: 250,
		HasTools:        false,
	})
	if dShort.RuleName != "short-mechanical" || dShort.Primary.Model != "llama3.2:3b" {
		t.Fatalf("expected short-mechanical rule, got %+v", dShort)
	}

	// 4. never_downgrade via X-Router-Tier or HasReasoningParams skips local offload
	hFrontier := http.Header{}
	hFrontier.Set("X-Router-Task", "commit_msg")
	hFrontier.Set("X-Router-Tier", "frontier")
	dND := eng.Evaluate(RequestContext{
		APIFormat:          "gemini_native",
		RequestedModel:     "gemini-2.5-pro",
		Headers:            hFrontier,
		HasReasoningParams: true,
	})
	if !dND.NeverDowngrade || dND.Primary.Provider != "gemini" {
		t.Fatalf("expected never_downgrade to skip ollama offload, got %+v", dND)
	}

	// 5. Explicit frontier alias rule still resolves primary + fallback chain
	dAlias := eng.Evaluate(RequestContext{
		APIFormat:      "anthropic_messages",
		RequestedModel: "frontier",
	})
	if dAlias.RuleName != "frontier-with-resilience" || dAlias.Primary.Provider != "anthropic" || len(dAlias.Fallbacks) != 1 {
		t.Fatalf("expected frontier-with-resilience rule, got %+v", dAlias)
	}
}

func TestEnginePrivacyLocalStripsCloudProviders(t *testing.T) {
	cfg := testRouterConfig()
	cfg.Providers["groq"] = config.ProviderConfig{Type: "openai_compatible", BaseURL: "https://api.groq.com/openai/v1"}
	cfg.Providers["ollama"] = config.ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "http://127.0.0.1:11434/v1",
		PricingPer1M: map[string]config.ModelPricing{
			"gemma3:4b": {Input: 0, Output: 0},
		},
	}
	cfg.Routing.Rules = append([]config.RoutingRule{
		{
			Name:          "free-cloud-then-local",
			Match:         config.MatchRule{Headers: map[string][]string{"X-Router-Task": {"summarize"}}},
			Target:        "groq:llama-3.3-70b-versatile",
			FallbackChain: []string{"gemini:gemini-2.5-flash", "ollama:gemma3:4b"},
		},
	}, cfg.Routing.Rules...)

	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}

	h := http.Header{}
	h.Set("X-Router-Task", "summarize")
	h.Set("X-Router-Privacy", "local")
	d := eng.Evaluate(RequestContext{
		APIFormat:      "openai_chat",
		RequestedModel: "gpt-4o",
		Headers:        h,
	})
	if d.Primary.Provider != "ollama" || d.Primary.Model != "gemma3:4b" || len(d.Fallbacks) != 0 {
		t.Fatalf("expected X-Router-Privacy=local to strip cloud providers, got %+v", d)
	}
}
