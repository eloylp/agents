# AGENTS.md

This file defines repo-specific guidance for future coding agents working in this project.

## Project Summary

- Language: Go (`go 1.22`).
- Binary entrypoint: `cmd/agentd/main.go`.
- Purpose: receive GitHub webhook events and trigger AI CLI workflows (Claude/Codex) for issue refinement (`ai:refine`) and PR review (`ai:review`).

## Quick Commands

- Run tests: `go test ./...`
- Build: `go build ./cmd/agentd`
- Run daemon: `go run ./cmd/agentd -config config.yaml`

## Code Map

- `cmd/agentd/main.go`
  - Wires config, logger, AI backend runners, workflow engine, and webhook server.
- `internal/config/config.go`
  - Config schema, defaults, env resolution, and AI backend validation.
- `internal/webhook/*`
  - HTTP server, webhook signature verification, and short-lived delivery dedupe.
- `internal/workflow/engine.go`
  - Label parsing/routing from webhook events and runner invocation.
- `internal/workflow/labels.go`
  - Parses supported `ai:*` labels into workflow/backend/agent targets.
- `internal/ai/prompt.go`
  - Prompt templates and stdout JSON artifact contract for issue and PR workflows.
- `internal/ai/cmdrunner.go`
  - Shared `noop`/`command` runner implementation for configured AI backends.
- `internal/claude/*`, `internal/codex/*`
  - Thin compatibility wrappers over `internal/ai` interfaces.

## Behavioral Guardrails

- Keep the daemon read-only against GitHub REST APIs. Write actions should continue to happen through AI CLI + GitHub MCP workflows.
- Keep prompt/runner contract consistent:
  - prompts require one JSON object on stdout,
  - `internal/ai/cmdrunner.go` expects parseable JSON when output is non-empty.
- Preserve current label semantics:
  - issue: `ai:refine` and `ai:refine:<backend>`,
  - PR: `ai:review`, `ai:review:<backend>:<agent>`, and `ai:review:<backend>:all`.

## Editing Checklist

- For config changes:
  - update defaults/validation in `internal/config/config.go`,
  - update `config.example.yaml`,
  - update README configuration docs.
- For workflow behavior changes:
  - update prompt text and engine logic together when contracts change,
  - update `internal/workflow/labels.go` + tests when label grammar changes.

## Testing Expectations

- Always run `go test ./...` after non-trivial changes.
- Add or update focused tests when changing:
  - prompt content/format (`internal/claude/prompt_test.go`),
  - label parsing (`internal/workflow/labels_test.go`),
  - parsing/contract behavior in runner (`internal/codex/runner_test.go`).

## Operational Notes

- `.env` is auto-loaded on startup (`godotenv.Load()`).
- Required runtime secret comes from config or env (`GITHUB_WEBHOOK_SECRET`).
- Avoid printing secrets or raw prompt bodies in logs; prompt hashing/redaction is already implemented.
