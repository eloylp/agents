# AGENTS.md

This file defines repo-specific guidance for future coding agents working in this project.

## Project Summary

- Language: Go (`go 1.22`).
- Binary entrypoint: `cmd/agentd/main.go`.
- Purpose: poll GitHub issues/PRs and trigger AI CLI workflows (Claude/Codex) for issue refinement (`ai:refine`) and PR review (`ai:review`).
- Persistence: PostgreSQL via `internal/store`.

## Quick Commands

- Run tests: `go test ./...`
- Build: `go build ./cmd/agentd`
- Run daemon: `go run ./cmd/agentd -config config.yaml`

## Code Map

- `cmd/agentd/main.go`
  - Wires config, logger, store, GitHub client, AI backend runners, workflow engine, and poller.
- `internal/config/config.go`
  - Config schema, defaults, env resolution, and AI backend validation.
- `internal/poller/poller.go`
  - Repo polling loop, backoff, jitter, and issue/PR dispatch.
- `internal/workflow/engine.go`
  - Label parsing/routing, dedupe fingerprints, locking, quotas, runner calls, and artifact persistence.
- `internal/workflow/labels.go`
  - Parses supported `ai:*` labels into workflow/backend/role targets.
- `internal/workflow/fingerprint.go`
  - Deterministic issue/PR fingerprint generation.
- `internal/ai/prompt.go`
  - Prompt templates and stdout JSON artifact contract for issue and PR workflows.
- `internal/ai/cmdrunner.go`
  - Shared `noop`/`command` runner implementation for configured AI backends.
- `internal/claude/*`, `internal/openai/*`
  - Thin compatibility wrappers over `internal/ai` interfaces.
- `internal/github/client.go`
  - GitHub REST reads (issues, PRs, comments, files), pagination, and rate-limit handling.
- `internal/store/store.go` and `internal/store/schema.sql`
  - DB schema, repo/work item/run/artifact records, and locking primitives.

## Behavioral Guardrails

- Keep the daemon read-only against GitHub REST APIs. Write actions should continue to happen through AI CLI + GitHub MCP workflows.
- Preserve idempotency guarantees:
  - fingerprints drive run dedupe (`workflow_runs` unique path),
  - artifacts are deduped by `(workflow_run_id, artifact_type, part_key)`.
- Preserve lock semantics (`locks` table) before workflow execution to prevent concurrent processing.
- Keep prompt/runner contract consistent:
  - prompts require one JSON object on stdout,
  - `internal/ai/cmdrunner.go` expects parseable JSON when output is non-empty.
- Preserve current label semantics:
  - issue: `ai:refine` and `ai:refine:<agent>`,
  - PR: `ai:review`, `ai:review:<agent>:<role>`, and `ai:review:<agent>:all`.

## Editing Checklist

- For config changes:
  - update defaults/validation in `internal/config/config.go`,
  - update `config.example.yaml`,
  - update README configuration docs.
- For schema changes:
  - edit `internal/store/schema.sql`,
  - update store queries if needed,
  - verify startup migration path (`EnsureSchema`) still works.
- For workflow behavior changes:
  - update prompt text and engine logic together when contracts change,
  - update `internal/workflow/labels.go` + tests when label grammar changes,
  - keep fingerprint inputs stable unless intentional (document version bumps).

## Testing Expectations

- Always run `go test ./...` after non-trivial changes.
- Add or update focused tests when changing:
  - prompt content/format (`internal/claude/prompt_test.go`),
  - fingerprint logic (`internal/workflow/fingerprint_test.go`),
  - label parsing (`internal/workflow/labels_test.go`),
  - parsing/contract behavior in runner (`internal/openai/runner_test.go`).

## Operational Notes

- `.env` is auto-loaded on startup (`godotenv.Load()`).
- Required runtime secrets come from config or env (`DATABASE_URL`, `GITHUB_TOKEN`).
- Avoid printing secrets or raw prompt bodies in logs; prompt hashing/redaction is already implemented.
