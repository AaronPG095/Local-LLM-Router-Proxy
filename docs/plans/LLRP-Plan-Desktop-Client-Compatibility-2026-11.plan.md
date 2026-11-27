---
title: Plan - Feature: Desktop Client Compatibility
ticket: local://LLRP:LLRP-Plan-Desktop-Client-Compatibility-2026-11
status: planned
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
---
# Desktop Client Compatibility

## Summary

Document and verify the desktop router entry points for Codex, Antigravity, Claude Code, Gemini CLI, and OpenAI-compatible clients. Add a shared test matrix for each supported generation protocol.

## Context

PR 10 merged native Windows credentials. The current guide uses an obsolete Gemini CLI variable and does not separate native streaming APIs from MCP offload.

## Open questions

None.

## Acceptance criteria

- [ ] AC1: The guide distinguishes MCP offload from native generation and gives verified configuration syntax and endpoint paths for each named client.
- [ ] AC2: Mocked client requests demonstrate authenticated HTTP streaming over all four protocol surfaces and a local-only request reaches only Ollama.
- [ ] AC3: Run the repository test/build gate and verify the Codex and Antigravity registered MCP commands can initialize and call router metrics without a cloud prompt.

## Steps

- [ ] Replace generic environment suggestions with a client and protocol setup matrix, including supported auth modes and endpoint limitations. (AC1)
- [ ] Add the mocked generation and local-only compatibility matrix. (AC2)
- [ ] Run full Go verification and exercise the installed Codex and Antigravity MCP registrations. (AC3)

## Risks

Client authentication modes and API environment names can vary by version. Cite official configuration references and keep compatibility claims scoped to the supported route. A local-only client flow could accidentally use a remote provider, so verify that with a loopback mock and a privacy header.

## Out of scope

Cloud subscription interception, new provider protocols or discovery endpoints, paid provider smoke calls, and secret sync.

## Notes

Approved plan:

> Document and test explicit setup for Codex CLI using OpenAI Responses, Claude Code using Anthropic Messages, Gemini CLI using Gemini Native, and OpenAI-compatible clients using Chat Completions. Include Antigravity’s MCP offload path.

## Progress log

<!-- One line per session/review, added with `ticket.mjs log` or `ticket.mjs review` -->
