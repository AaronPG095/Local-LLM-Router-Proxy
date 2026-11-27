---
title: Plan - Feature: Tool-Trim-And-Prompt-Cache-Booster
ticket: local://LLRP:LLRP-Plan-Tool-Trim-And-Prompt-Cache-Booster-2026-11
status: in-progress
priority: 
state: 
due: 
appetite: 
version: 
impact: 
devTicketKey: LLRP:LLRP-Plan-Tool-Trim-And-Prompt-Cache-Booster-2026-11
agent: Gemini
branch: feat/tool-trim-and-cache-booster
worktree: Local-LLM-Router-Proxy
startedAt: 2026-11-27
---
# Tool-Trim-And-Prompt-Cache-Booster

## Summary

Add content-aware old-tool-output trimming for long OpenAI Chat Completions and Anthropic Messages agent sessions, plus an Anthropic `cache_control: {"type": "ephemeral"}` prefix booster to cut input token costs without touching recent turns or tool schemas.

## Context

- `internal/proxy/openai.go` and `internal/proxy/anthropic.go` forward conversation histories untouched, even when older tool outputs (build logs, file reads) from 6+ turns ago bloat context.
- Anthropic `/v1/messages` discounts cached prefixes by up to 90% when `cache_control: {"type": "ephemeral"}` breakpoints are set on `system` and the final `tools` entry.

## Open questions

None

## Acceptance criteria

- [x] AC1: `CompressionConfig` (`internal/config/config.go`) loads and applies defaults (`keep_recent_messages: 6`, `min_tool_result_chars: 4000`, `head_chars: 800`, `tail_chars: 800`). verified: CompressionConfig defaults verified in internal/config/config.go
- [x] AC2: `internal/proxy/compress.go` trims old large tool outputs while leaving recent turns/system/small outputs untouched, and injects Anthropic `cache_control` without duplicating existing markers, verified by `internal/proxy/compress_test.go`. verified: go test -v ./internal/proxy/... passed (compress_test.go)
- [x] AC3: `openai.go` and `anthropic.go` apply compression/boosting (respecting `X-Router-Compress: off`), `go test -v ./...` passes, and `bin/llm-router.exe` + `bin/llm-router-mcp.exe` rebuild cleanly on `main`. verified: go test -v ./... passed across all packages

## Steps

- [x] Step 1: Add `CompressionConfig` to `internal/config/config.go` and `router.config.example.yaml` (AC1)
- [x] Step 2: Implement `internal/proxy/compress.go`, `compress_test.go`, and wire into `openai.go` + `anthropic.go` (AC2, AC3)
- [x] Step 3: Run `go test -v ./...`, complete `adw pack --test`, independent review, `adw check`, merge PR #9, and rebuild binaries (AC3)

## Risks

- **Breaking prompt prefix caching by mutating recent messages:** Mitigated by keeping `system`/`developer` and the last `keep_recent_messages` (default 6) 100% untouched, and allowing `X-Router-Compress: off` bypass.

## Out of scope

- Destructive LLM-based summarization of conversation history inside the proxy hot path.

## Notes

Approved Plan:
> PR #9 (Content-Aware Old-Tool-Output Trimming & Anthropic cache_control Prefix Booster): CompressionConfig in internal/config, compress.go + compress_test.go, wired into openai.go and anthropic.go (respecting X-Router-Compress: off), documented in router.config.example.yaml.

## Progress log

<!-- One line per session/review, added with `ticket.mjs log` or `ticket.mjs review` -->
