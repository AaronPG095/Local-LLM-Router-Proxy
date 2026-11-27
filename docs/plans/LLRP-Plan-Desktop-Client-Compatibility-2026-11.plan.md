---
title: Plan - Feature: Desktop Client Compatibility
ticket: local://LLRP:LLRP-Plan-Desktop-Client-Compatibility-2026-11
status: in-progress
priority: 
state: 
due: 
appetite: M
version: 
impact: 
blueprint: docs/architecture/LLRP-Blueprint-Desktop-Client-Integration.md
dependsOn: LLRP-Plan-Shared-Desktop-Credentials-2026-11
devTicketKey: LLRP:LLRP-Plan-Desktop-Client-Compatibility-2026-11
agent: Codex
branch: feat/desktop-client-compatibility
worktree: Local-LLM-Router-Proxy
startedAt: 2026-11-27
reviewedBy: Codex
reviewVerdict: pass
reviewSession: /root/desktop_client_review
baseBranch: main
reviewFingerprint: 1c0539d68557cc57c2cdcbe3cca3fcf1c93a42415bca73d442c6faa9950a510e
---
# Desktop Client Compatibility

## Summary

Document and verify the desktop router entry points for Codex, Antigravity, Claude Code, Gemini CLI, and OpenAI-compatible clients. Add a shared test matrix for each supported generation protocol.

## Context

PR 10 merged native Windows credentials. The current guide uses an obsolete Gemini CLI variable and does not separate native streaming APIs from MCP offload.

## Open questions

None.

## Acceptance criteria

- [x] AC1: The guide distinguishes MCP offload from native generation and gives verified configuration syntax and endpoint paths for each named client. verified: Official configuration references checked and documented with verified generation paths and known auxiliary endpoint limitations
- [x] AC2: Mocked client requests demonstrate authenticated HTTP streaming over all four protocol surfaces and a local-only request reaches only Ollama. verified: Authenticated streaming contracts passed on Responses, Messages, Gemini Native and Chat Completions; local privacy reached only Ollama
- [x] AC3: Run the repository test/build gate and verify the Codex and Antigravity registered MCP commands can initialize and call router metrics without a cloud prompt. verified: go test -v ./... and both builds passed; Codex and Antigravity configured commands passed initialize and router_metrics

## Steps

- [x] Replace generic environment suggestions with a client and protocol setup matrix, including supported auth modes and endpoint limitations. (AC1) verified: Guide documents MCP and generation separately; confirmed Codex, Claude Code, Gemini CLI and Antigravity configuration in official docs
- [x] Add the mocked generation and local-only compatibility matrix. (AC2) verified: TestDesktopClientStreamingContracts passed all four routes and local-only test reached Ollama only
- [x] Run full Go verification and exercise the installed Codex and Antigravity MCP registrations. (AC3) verified: Full Go suite and both Windows builds passed; actual Codex and Antigravity registered commands returned router_metrics

## Risks

Client authentication modes and API environment names can vary by version. Cite official configuration references and keep compatibility claims scoped to the supported route. A local-only client flow could accidentally use a remote provider, so verify that with a loopback mock and a privacy header.

## Out of scope

Cloud subscription interception, new provider protocols or discovery endpoints, paid provider smoke calls, and secret sync.

## Notes

Approved plan:

> Document and test explicit setup for Codex CLI using OpenAI Responses, Claude Code using Anthropic Messages, Gemini CLI using Gemini Native, and OpenAI-compatible clients using Chat Completions. Include Antigravity’s MCP offload path.

## Progress log

<!-- One line per session/review, added with `ticket.mjs log` or `ticket.mjs review` -->
- 2026-11-27 13:45 UTC, Codex (review: pass): Independent pack tests and scope passed; four streaming routes and local privacy were covered, and Codex overrides parsed in a no-generation check. [reviewer: /root/desktop_client_review]
- 2026-11-27 13:45 UTC, Codex (handoff): Done: Client guide, protocol tests, independent review, and both desktop MCP connections verified; Next: Publish PR and merge after Windows and Linux CI; Open: none; Tree: 1 dirty file(s)
