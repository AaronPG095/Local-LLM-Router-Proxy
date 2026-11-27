package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the root configuration for the LLM router proxy.
type Config struct {
	Server    ServerConfig              `yaml:"server"`
	Providers map[string]ProviderConfig `yaml:"providers"`
	Routing   RoutingConfig             `yaml:"routing"`
	Cache     CacheConfig               `yaml:"cache"`
	Budgets   BudgetsConfig             `yaml:"budgets"`
	Telemetry TelemetryConfig           `yaml:"telemetry"`
}

type ServerConfig struct {
	Host               string `yaml:"host"`
	Port               int    `yaml:"port"`
	ConnectTimeoutMs   int    `yaml:"connect_timeout_ms"`
	FirstByteTimeoutMs int    `yaml:"first_byte_timeout_ms"`
}

type ProviderConfig struct {
	Type         string                  `yaml:"type"`
	BaseURL      string                  `yaml:"base_url"`
	APIKeyEnv    string                  `yaml:"api_key_env"`
	MaxRPM       int                     `yaml:"max_rpm"`
	MaxRPD       int                     `yaml:"max_rpd"`
	PricingPer1M map[string]ModelPricing `yaml:"pricing_per_1m"`
}

type ModelPricing struct {
	Input       float64 `yaml:"input"`
	Output      float64 `yaml:"output"`
	CachedInput float64 `yaml:"cached_input"`
}

type RoutingConfig struct {
	DefaultTarget  string               `yaml:"default_target"`
	NeverDowngrade NeverDowngradeConfig `yaml:"never_downgrade"`
	Rules          []RoutingRule        `yaml:"rules"`
}

type NeverDowngradeConfig struct {
	Headers                  map[string]string `yaml:"headers"`
	ModelAliases             []string          `yaml:"model_aliases"`
	WhenReasoningParamsPresent bool              `yaml:"when_reasoning_params_present"`
}

type RoutingRule struct {
	Name          string     `yaml:"name"`
	Match         MatchRule  `yaml:"match"`
	Target        string     `yaml:"target"`
	FallbackChain []string   `yaml:"fallback_chain"`
	SemanticCache bool       `yaml:"semantic_cache"`
}

type MatchRule struct {
	Headers            map[string][]string `yaml:"headers"`
	ModelAlias         string              `yaml:"model_alias"`
	MaxEstimatedTokens int                 `yaml:"max_estimated_tokens"`
	SystemPromptRegex  string              `yaml:"system_prompt_regex"`
	HasTools           *bool               `yaml:"has_tools"`
}

type CacheConfig struct {
	L1Exact    L1CacheConfig `yaml:"l1_exact"`
	L2Semantic L2CacheConfig `yaml:"l2_semantic"`
}

type L1CacheConfig struct {
	Enabled              bool `yaml:"enabled"`
	TTLSeconds           int  `yaml:"ttl_seconds"`
	MaxEntries           int  `yaml:"max_entries"`
	RequireDeterministic bool `yaml:"require_deterministic"`
}

type L2CacheConfig struct {
	Enabled             bool    `yaml:"enabled"`
	EmbeddingProvider   string  `yaml:"embedding_provider"`
	SimilarityThreshold float64 `yaml:"similarity_threshold"`
	TTLSeconds          int     `yaml:"ttl_seconds"`
	MaxCandidates       int     `yaml:"max_candidates"`
}

type BudgetsConfig struct {
	Session      BudgetScopeConfig `yaml:"session"`
	ProjectDaily BudgetScopeConfig `yaml:"project_daily"`
}

type BudgetScopeConfig struct {
	MaxTokens       int64   `yaml:"max_tokens"`
	MaxCostUSD      float64 `yaml:"max_cost_usd"`
	SoftWarnPct     float64 `yaml:"soft_warn_pct"`
	HardAction      string  `yaml:"hard_action"`
	DowngradeTarget string  `yaml:"downgrade_target"`
}

type TelemetryConfig struct {
	SQLitePath   string `yaml:"sqlite_path"`
	JSONLPath    string `yaml:"jsonl_path"`
	OTLPEndpoint string `yaml:"otlp_endpoint"`
}

// Load reads a YAML configuration file, applies defaults, and validates fields.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml config %q: %w", path, err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Host == "" {
		c.Server.Host = "127.0.0.1"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8787
	}
	if c.Server.ConnectTimeoutMs <= 0 {
		c.Server.ConnectTimeoutMs = 5000
	}
	if c.Server.FirstByteTimeoutMs <= 0 {
		c.Server.FirstByteTimeoutMs = 30000
	}
	if c.Telemetry.SQLitePath == "" {
		c.Telemetry.SQLitePath = "./data/router.db"
	}
	if c.Telemetry.JSONLPath == "" {
		c.Telemetry.JSONLPath = "./data/telemetry.jsonl"
	}
	applyBudgetDefaults(&c.Budgets.Session)
	applyBudgetDefaults(&c.Budgets.ProjectDaily)
}

func applyBudgetDefaults(b *BudgetScopeConfig) {
	if b.SoftWarnPct <= 0 {
		b.SoftWarnPct = 0.80
	}
	if b.HardAction == "" {
		b.HardAction = "block"
	}
}

// Validate checks required configuration fields and invariants.
func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server.port %d: must be between 1 and 65535", c.Server.Port)
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("providers must not be empty")
	}
	for name, p := range c.Providers {
		if strings.TrimSpace(p.BaseURL) == "" {
			return fmt.Errorf("provider %q: base_url is required", name)
		}
		if strings.TrimSpace(p.Type) == "" {
			return fmt.Errorf("provider %q: type is required", name)
		}
	}
	if err := validateBudgetScope("budgets.session", c.Budgets.Session); err != nil {
		return err
	}
	if err := validateBudgetScope("budgets.project_daily", c.Budgets.ProjectDaily); err != nil {
		return err
	}
	return nil
}

func validateBudgetScope(name string, b BudgetScopeConfig) error {
	switch b.HardAction {
	case "block", "downgrade":
	default:
		return fmt.Errorf("%s.hard_action %q: must be 'block' or 'downgrade'", name, b.HardAction)
	}
	if b.SoftWarnPct < 0 || b.SoftWarnPct > 1 {
		return fmt.Errorf("%s.soft_warn_pct %.2f: must be between 0 and 1", name, b.SoftWarnPct)
	}
	if b.HardAction == "downgrade" && strings.TrimSpace(b.DowngradeTarget) == "" {
		return fmt.Errorf("%s.downgrade_target is required when hard_action is 'downgrade'", name)
	}
	return nil
}

// ResolveAPIKey reads the configured environment variable for a provider's API key.
func (c *Config) ResolveAPIKey(providerName string) string {
	p, ok := c.Providers[providerName]
	if !ok || p.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(p.APIKeyEnv)
}

// LookupPricing returns the model pricing if configured for the provider and model.
func (c *Config) LookupPricing(providerName, model string) (ModelPricing, bool) {
	p, ok := c.Providers[providerName]
	if !ok || p.PricingPer1M == nil {
		return ModelPricing{}, false
	}
	if pricing, exists := p.PricingPer1M[model]; exists {
		return pricing, true
	}
	return ModelPricing{}, false
}
