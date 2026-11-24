# ADR-004: Policy Routing, Pre-First-Byte Fallbacks, and Cross-Protocol Translation

## Status
Accepted

## Context
To reduce frontier token consumption without degrading complex coding or reasoning tasks, the proxy needs deterministic policy routing, automatic failover on upstream rate limits/overload (`429`/`529`), and seamless offloading of mechanical text tasks from Gemini Native or OpenAI Responses callers to local OpenAI-compatible Ollama models.

## Decision
1. **First-Match-Wins Rule Engine:** Routing rules in `routing.rules` are evaluated in declaration order. All non-empty match criteria (`headers`, `model_alias`, `max_estimated_tokens`, `system_prompt_regex`, `has_tools`) must match.
2. **`never_downgrade` Protection:** Requests flagged as frontier/reasoning (`X-Router-Tier: frontier`, frontier model aliases, or `thinkingConfig` / `reasoning` / `thinking` params) skip heuristic/task offload rules targeting `openai_compatible` (local/Ollama) providers. Explicit `model_alias` frontier rules and resilience fallback chains still apply.
3. **Pre-First-Byte Retryable Statuses:** Upstream failures (`429`, `529`, `502`, `503`, `504`, or network/header timeouts) trigger the ordered `fallback_chain` strictly before any bytes are written to the client response.
4. **Cross-Protocol Translation Scope:** When a text-only mechanical sub-task (`has_tools == false`) from Gemini Native or OpenAI Responses is routed to an `openai_compatible` provider (e.g., Ollama), the proxy translates the request into OpenAI Chat Completions (`/v1/chat/completions`) and maps the JSON or SSE response back into the caller's native wire format.

## Consequences
- Frontier/reasoning workloads are never silently degraded to small local models.
- Tool-calling agent loops stay on native protocol endpoints.
- Mechanical tasks (commit messages, log trimming, summarization) offload cleanly to local Ollama regardless of whether the caller speaks Gemini Native or OpenAI Responses.
