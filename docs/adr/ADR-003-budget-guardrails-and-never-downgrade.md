# ADR-003: Budget Guardrails and the `never_downgrade` Invariant

## Status
Accepted

## Context
Hard token/cost caps protect limited frontier quotas, but silently downgrading a complex reasoning or architecture request to a small local model can cause subtle bugs or wasted agent loops.

## Decision
1. **Two-Tier Budget Thresholds:** Each scope (`session`, `project_daily`) tracks tokens and estimated USD cost in SQLite `budget_usage`.
   - At `soft_warn_pct` (default `0.80`), the proxy injects `X-Router-Budget-Warning` without blocking.
   - At `100%`, `hard_action` triggers either `"block"` (HTTP `429`) or `"downgrade"` (reroute to `downgrade_target`).
2. **`never_downgrade` Invariant:** Any request marked `X-Router-Tier: frontier`, using a frontier model alias, or carrying reasoning parameters (`generationConfig.thinkingConfig` in Gemini, `reasoning_effort` in OpenAI) MUST NEVER be silently downgraded. If a budget with `hard_action: "downgrade"` is exceeded for such a request, the proxy MUST fail closed with HTTP `429`.

## Consequences
- Routine mechanical requests degrade gracefully to local Ollama when budgets are exhausted.
- High-reasoning requests fail loudly rather than silently producing degraded output.
