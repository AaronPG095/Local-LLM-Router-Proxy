---
title: Plan - Feature: Shared Desktop Credentials
ticket: local://LLRP:LLRP-Plan-Shared-Desktop-Credentials-2026-11
status: planned
priority: 
state: 
due: 
appetite: M
version: 
impact: 
blueprint: docs/architecture/LLRP-Blueprint-Desktop-Client-Integration.md
devTicketKey: LLRP:LLRP-Plan-Shared-Desktop-Credentials-2026-11
agent: Codex
---
# Shared Desktop Credentials

## Summary

Make the existing Windows DPAPI store a supported desktop credential source. Load it once in the router so manual startup and MCP auto-start use the same keys without a PowerShell wrapper per MCP client.

## Context

The Windows launcher already stores six provider keys with DPAPI, but it is outside the repository and clients currently decrypt them before startup.

## Open questions

None.

## Acceptance criteria

- [ ] AC1: A Windows router started directly or via MCP can use the existing Windows account's encrypted provider keys. Explicit environment keys take precedence and an absent store preserves the environment-only path on all platforms.
- [ ] AC2: The supported Windows setup stores keys encrypted and can replace one key while preserving the other entries. Invalid or unreadable configured keys fail with diagnostics that omit credential material.
- [ ] AC3: Automated tests cover precedence, invalid stores, and Windows encryption compatibility. Both binaries build, and a Windows cold-start and running-router reuse check records observed timing and outcomes.

## Steps

- [ ] Test and implement router-owned credential loading, including the existing JSON and DPAPI format and environment precedence. (AC1, AC2)
- [ ] Add the supported Windows setup and document manual startup, MCP startup, and rotation. (AC1, AC2)
- [ ] Run the full Go suite, build both binaries, and exercise isolated Windows cold-start and existing-router reuse with dummy credentials. (AC3)

## Risks

DPAPI blobs are bound to the Windows user and machine context. A round-trip against a PowerShell-created credential fixture catches encoding errors. An explicit environment override allows existing users to supply keys without depending on local DPAPI. Cold-start checks use an isolated config, loopback port and temporary telemetry.

## Out of scope

Portable vault migration, GUI client installation, additional protocol features, changes to routing policy, and live paid-provider calls.

## Notes

Approved plan:

> Make the encrypted Windows setup a supported repository feature. Manual startup, Codex MCP, and Antigravity MCP should all load the same credential store. Keep compatibility with the six keys already saved. Verify cold start, reuse of an already-running router, and rotation of one key without re-entering the others.

> Keep the existing Windows-encrypted store and make the router itself load the keys once at startup. Include a cold-start benchmark.

## Progress log

<!-- One line per session/review, added with `ticket.mjs log` or `ticket.mjs review` -->
