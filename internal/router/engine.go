package router

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
)

// RequestContext captures the attributes used to evaluate routing policies.
type RequestContext struct {
	APIFormat          string
	RequestedModel     string
	Headers            http.Header
	SystemPrompt       string
	EstimatedTokens    int
	HasTools           bool
	HasReasoningParams bool
}

// RouteTarget identifies a provider and model pair.
type RouteTarget struct {
	Provider string
	Model    string
}

// Decision is the output of policy evaluation.
type Decision struct {
	Primary              RouteTarget
	Fallbacks            []RouteTarget
	RuleName             string
	NeverDowngrade       bool
	SemanticCacheAllowed bool
}

type compiledRule struct {
	rule        config.RoutingRule
	systemRegex *regexp.Regexp
	primary     RouteTarget
	fallbacks   []RouteTarget
}

// Engine evaluates deterministic routing rules against incoming requests.
type Engine struct {
	cfg   *config.Config
	rules []compiledRule
}

// NewEngine compiles routing rules and returns a policy Engine.
func NewEngine(cfg *config.Config) (*Engine, error) {
	e := &Engine{cfg: cfg}
	for _, r := range cfg.Routing.Rules {
		var re *regexp.Regexp
		if r.Match.SystemPromptRegex != "" {
			var err error
			re, err = regexp.Compile(r.Match.SystemPromptRegex)
			if err != nil {
				return nil, fmt.Errorf("compile regex for rule %q: %w", r.Name, err)
			}
		}
		primary, _ := ParseTarget(r.Target)
		var fallbacks []RouteTarget
		for _, fb := range r.FallbackChain {
			if t, ok := ParseTarget(fb); ok {
				fallbacks = append(fallbacks, t)
			}
		}
		e.rules = append(e.rules, compiledRule{
			rule:        r,
			systemRegex: re,
			primary:     primary,
			fallbacks:   fallbacks,
		})
	}
	return e, nil
}

// ParseTarget splits a "provider:model" spec on the first ':'.
func ParseTarget(spec string) (RouteTarget, bool) {
	idx := strings.IndexByte(spec, ':')
	if idx <= 0 || idx >= len(spec)-1 {
		return RouteTarget{}, false
	}
	return RouteTarget{
		Provider: strings.TrimSpace(spec[:idx]),
		Model:    strings.TrimSpace(spec[idx+1:]),
	}, true
}

// Evaluate selects the primary target, fallback chain, and never_downgrade flag.
func (e *Engine) Evaluate(rc RequestContext) Decision {
	neverDowngrade := e.isNeverDowngrade(rc)
	localOnly := strings.EqualFold(strings.TrimSpace(rc.Headers.Get("X-Router-Privacy")), "local")

	var dec Decision
	matched := false
	for _, cr := range e.rules {
		if !matchesRule(cr, rc) {
			continue
		}
		// Never silently offload frontier/reasoning requests to local/zero-cost models
		// via heuristic/task rules. Explicit model_alias rules still apply.
		if neverDowngrade && cr.rule.Match.ModelAlias == "" {
			if p, ok := e.cfg.Providers[cr.primary.Provider]; ok && p.Type == "openai_compatible" {
				continue
			}
		}
		dec = Decision{
			Primary:              cr.primary,
			Fallbacks:            append([]RouteTarget(nil), cr.fallbacks...),
			RuleName:             cr.rule.Name,
			NeverDowngrade:       neverDowngrade,
			SemanticCacheAllowed: cr.rule.SemanticCache,
		}
		matched = true
		break
	}

	if !matched {
		dec = Decision{
			Primary:        e.defaultPassThroughTarget(rc),
			NeverDowngrade: neverDowngrade,
		}
	}

	if localOnly {
		dec = e.enforceLocalOnly(dec)
	}
	return dec
}

func (e *Engine) isLocalProvider(providerName string) bool {
	p, ok := e.cfg.Providers[providerName]
	if !ok || p.Type != "openai_compatible" {
		return false
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

func (e *Engine) enforceLocalOnly(dec Decision) Decision {
	var localCandidates []RouteTarget
	if e.isLocalProvider(dec.Primary.Provider) {
		localCandidates = append(localCandidates, dec.Primary)
	}
	for _, fb := range dec.Fallbacks {
		if e.isLocalProvider(fb.Provider) {
			localCandidates = append(localCandidates, fb)
		}
	}
	if len(localCandidates) > 0 {
		dec.Primary = localCandidates[0]
		dec.Fallbacks = localCandidates[1:]
		return dec
	}

	if dt, ok := ParseTarget(e.cfg.Budgets.Session.DowngradeTarget); ok && e.isLocalProvider(dt.Provider) {
		dec.Primary = dt
		dec.Fallbacks = nil
		return dec
	}
	for name, p := range e.cfg.Providers {
		if e.isLocalProvider(name) {
			for m := range p.PricingPer1M {
				dec.Primary = RouteTarget{Provider: name, Model: m}
				dec.Fallbacks = nil
				return dec
			}
		}
	}
	// Fail closed: never leak a privacy=local request to a non-local provider.
	dec.Primary = RouteTarget{}
	dec.Fallbacks = nil
	return dec
}

func (e *Engine) isNeverDowngrade(rc RequestContext) bool {
	nd := e.cfg.Routing.NeverDowngrade
	for k, expected := range nd.Headers {
		if strings.EqualFold(strings.TrimSpace(rc.Headers.Get(k)), expected) {
			return true
		}
	}
	for _, alias := range nd.ModelAliases {
		if strings.EqualFold(rc.RequestedModel, alias) {
			return true
		}
	}
	if nd.WhenReasoningParamsPresent && rc.HasReasoningParams {
		return true
	}
	return false
}

func matchesRule(cr compiledRule, rc RequestContext) bool {
	m := cr.rule.Match

	for hdr, allowed := range m.Headers {
		actual := strings.TrimSpace(rc.Headers.Get(hdr))
		if actual == "" {
			return false
		}
		matched := false
		for _, candidate := range allowed {
			if strings.EqualFold(actual, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	if m.ModelAlias != "" && !strings.EqualFold(rc.RequestedModel, m.ModelAlias) {
		return false
	}
	if m.MaxEstimatedTokens > 0 && rc.EstimatedTokens > m.MaxEstimatedTokens {
		return false
	}
	if cr.systemRegex != nil && !cr.systemRegex.MatchString(rc.SystemPrompt) {
		return false
	}
	if m.HasTools != nil && rc.HasTools != *m.HasTools {
		return false
	}
	return true
}

func (e *Engine) defaultPassThroughTarget(rc RequestContext) RouteTarget {
	if t, ok := ParseTarget(rc.RequestedModel); ok {
		if _, exists := e.cfg.Providers[t.Provider]; exists {
			return t
		}
	}

	switch rc.APIFormat {
	case "gemini_native":
		return RouteTarget{Provider: "gemini", Model: rc.RequestedModel}
	case "anthropic_messages":
		return RouteTarget{Provider: "anthropic", Model: rc.RequestedModel}
	default:
		if p, ok := e.cfg.Providers["ollama"]; ok {
			if _, hasModel := p.PricingPer1M[rc.RequestedModel]; hasModel {
				return RouteTarget{Provider: "ollama", Model: rc.RequestedModel}
			}
		}
		if _, ok := e.cfg.Providers["openai"]; ok {
			return RouteTarget{Provider: "openai", Model: rc.RequestedModel}
		}
		if _, ok := e.cfg.Providers["ollama"]; ok {
			return RouteTarget{Provider: "ollama", Model: rc.RequestedModel}
		}
		if t, ok := ParseTarget(e.cfg.Routing.DefaultTarget); ok {
			return t
		}
		return RouteTarget{Provider: "openai", Model: rc.RequestedModel}
	}
}
