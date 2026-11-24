# ADR-002: Cache Invalidation & Strictly Opt-In Semantic Caching for Code Tasks

## Status
Accepted

## Context
Caching repeated LLM prompts can save substantial frontier quota, but coding-agent workflows are uniquely sensitive to stale or near-miss responses (e.g., two prompts differing only by a line number, symbol name, or stack trace).

## Decision
1. **L1 Exact Cache:** Enabled by default only for deterministic requests (`temperature == 0` or `X-Router-Cache: force`). Keyed by SHA-256 of the canonicalized `(api_format, model, messages, tools, temperature, system_prompt)` tuple. Streaming hits replay stored SSE frames.
2. **L2 Semantic Cache:** Disabled by default (`enabled: false` globally unless explicitly opted in per route via `semantic_cache: true` or per request via `X-Router-Semantic-Cache: allow`), and always bypassed when tool definitions are present.
3. **Namespace Isolation & TTL:** L2 cosine-similarity lookups are scoped by `route_namespace` (project + route + model) and bounded by `ttl_seconds` and `max_candidates`.

## Consequences
- Zero risk of semantic cache poisoning on code generation, refactoring, or tool-calling agent loops.
- Mechanical sub-tasks (commit messages, log summarization, classification) can safely opt in to L2 semantic caching for maximum quota savings.
