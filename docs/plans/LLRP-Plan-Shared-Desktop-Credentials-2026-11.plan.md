---
title: Plan - Feature: Shared Desktop Credentials
ticket: local://LLRP:LLRP-Plan-Shared-Desktop-Credentials-2026-11
status: shipped
priority: 
state: 
due: 
appetite: L
version: 
impact: 
blueprint: docs/architecture/LLRP-Blueprint-Desktop-Client-Integration.md
devTicketKey: LLRP:LLRP-Plan-Shared-Desktop-Credentials-2026-11
agent: Codex
branch: feat/shared-desktop-credentials
worktree: Local-LLM-Router-Proxy
startedAt: 2026-11-27
reviewedBy: Codex
reviewVerdict: warn
reviewSession: /root/desktop_credentials_review
baseBranch: main
reviewFingerprint: 4a13040d149bceb1c90acb74c1f6110403f306e55488a46245a9f6680a6a9d32
pr: https://github.com/AaronPG095/Local-LLM-Router-Proxy/pull/10
---
# Shared Desktop Credentials

## Summary

Make the existing Windows DPAPI store a supported desktop credential source. Load it once in the router so manual startup and MCP auto-start use the same keys without a PowerShell wrapper per MCP client.

## Context

The Windows launcher already stores six provider keys with DPAPI, but it is outside the repository and clients currently decrypt them before startup.

## Open questions

None.

## Acceptance criteria

- [x] AC1: A Windows router started directly or via MCP can use the existing Windows account's encrypted provider keys. Explicit environment keys take precedence and an absent store preserves the environment-only path on all platforms. verified: Cold MCP startup and running-router reuse supplied the saved credential to a mock upstream; environment override and absent store covered by Go tests
- [x] AC2: The supported Windows setup stores keys encrypted and can replace one key while preserving the other entries. Invalid or unreadable configured keys fail with diagnostics that omit credential material. verified: PowerShell test encrypted and rotated one entry, preserved a second entry, and Go tests confirmed redacted invalid-store errors
- [x] AC3: Automated tests cover precedence, invalid stores, and Windows encryption compatibility. Both binaries build, and a Windows cold-start and running-router reuse check records observed timing and outcomes. verified: go test -v ./... and both Windows builds passed; isolated smoke observed 325 ms cold startup and 22 ms reuse

## Steps

- [x] Test and implement router-owned credential loading, including the existing JSON and DPAPI format and environment precedence. (AC1, AC2) verified: Windows config tests passed for environment precedence, redacted failures and existing PowerShell DPAPI format
- [x] Add the supported Windows setup and document manual startup, MCP startup, and rotation. (AC1, AC2) verified: Windows fixture saved two keys and replaced one while retaining the other; workflow guide documents startup and rotation
- [x] Run the full Go suite, build both binaries, and exercise isolated Windows cold-start and existing-router reuse with dummy credentials. (AC3) verified: go test -v ./... passed; both Windows binaries built; isolated cold MCP 325 ms and warm reuse 22 ms with mock auth

## Risks

DPAPI blobs are bound to the Windows user and machine context. A round-trip against a PowerShell-created credential fixture catches encoding errors. An explicit environment override allows existing users to supply keys without depending on local DPAPI. Cold-start checks use an isolated config, loopback port and temporary telemetry.

## Out of scope

Portable vault migration, GUI client installation, additional protocol features, changes to routing policy, and live paid-provider calls.

## Notes

Approved plan:

> Make the encrypted Windows setup a supported repository feature. Manual startup, Codex MCP, and Antigravity MCP should all load the same credential store. Keep compatibility with the six keys already saved. Verify cold start, reuse of an already-running router, and rotation of one key without re-entering the others.

> Keep the existing Windows-encrypted store and make the router itself load the keys once at startup. Include a cold-start benchmark.

The Windows-specific reader, portable fallback, dedicated Windows rotation test, and CI matrix exceed the medium file limit without expanding the approved functionality.

## Progress log

<!-- One line per session/review, added with `ticket.mjs log` or `ticket.mjs review` -->
- 2026-11-27 13:34 UTC, Codex (review: warn): Independent tests and plan passed; custom configs can direct a resolved credential to an alternate upstream, so the setup guide now documents trusted configs and dummy credentials for mocks. [reviewer: /root/desktop_credentials_review]
- 2026-11-27 13:34 UTC, Codex (handoff): Done: Windows credential loading, rotation, mock cold-start timing, and independent review completed; Next: Publish PR and wait for Windows and Linux CI before merge; Open: none; Tree: 2 dirty file(s)
