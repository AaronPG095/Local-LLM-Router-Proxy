---
title: Plan - Feature: Free-Tier-Pool-And-Cooldown
ticket: local://LLRP:LLRP-Plan-Free-Tier-Pool-And-Cooldown-2026-11
status: planned
priority: 
state: 
due: 
appetite: 
version: 
impact: 
devTicketKey: LLRP:LLRP-Plan-Free-Tier-Pool-And-Cooldown-2026-11
agent: Gemini
---
# Free-Tier-Pool-And-Cooldown

## Summary

Add a native free-tier provider pool with per-provider `429`/`529` `Retry-After` cooldowns, `max_rpm`/`max_rpd` proactive quota rotation, `provider_health` reporting in `/metrics/summary`, and an explicit `X-Router-Privacy: local` gate so sensitive prompts never leave local Ollama.

## Context

- `internal/proxy/fallback.go` currently tries candidates sequentially without remembering `429`/`529` cooldowns across requests.
- `internal/config/config.go` defines `ProviderConfig` without `max_rpm` or `max_rpd`.
- Free-tier cloud APIs (Groq, Google AI Studio, OpenRouter `:free`, Mistral) may log prompts, so an explicit `X-Router-Privacy: local` gate is needed to keep sensitive code on local Ollama.

## Open questions

None

## Acceptance criteria

- [ ] AC1: `ProviderTracker` (`internal/proxy/cooldown.go`) handles `Retry-After` cooldowns, sliding 60s `max_rpm`, UTC-day `max_rpd`, and `StatusSnapshot`, verified by `internal/proxy/cooldown_test.go`.
- [ ] AC2: `executeWithFallback` skips cooling-down/capped providers and marks `429`/`529` cooldowns; `/metrics/summary` exposes `provider_health`.
- [ ] AC3: `X-Router-Privacy: local` strips non-local providers in `internal/router/engine.go` and `offload_llm` (`internal/mcp/server.go`), verified by `go test -v ./...`.

## Steps

- [ ] Step 1: Add `MaxRPM`/`MaxRPD` config, `ProviderTracker`, fallback integration, and `provider_health` in `/metrics/summary` (AC1, AC2)
- [ ] Step 2: Add `X-Router-Privacy: local` gate, `offload_llm` privacy arg, and free-tier templates in `router.config.example.yaml` (AC3)
- [ ] Step 3: Run `go test -v ./...`, complete `adw pack --test`, independent review, `adw check`, and merge PR #8 (AC1, AC2, AC3)

## Risks

- **All candidates cooling down simultaneously:** Mitigated by falling back to the last candidate in `executeWithFallback` instead of failing with 0 attempts.

## Out of scope

- Content-aware tool-output trimming and Anthropic `cache_control` booster (handled in PR #9).

## Notes

Approved Plan:
> PR #8 (Free-Tier Pool, Per-Provider 429 Cooldown & Privacy Gate): `MaxRPM`/`MaxRPD` on `ProviderConfig`, `ProviderTracker` with `Retry-After` cooldowns and `StatusSnapshot`, `executeWithFallback` integration, `provider_health` in `/metrics/summary`, `X-Router-Privacy: local` gate in `router.Engine` and `offload_llm`, and free-tier templates in `router.config.example.yaml`.

## Progress log

<!-- One line per session/review, added with `ticket.mjs log` or `ticket.mjs review` -->
