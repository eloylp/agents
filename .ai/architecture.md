# Architecture Overview

## What This Service Does

`agentd` is a polling daemon that monitors configured GitHub repositories and triggers AI-assisted workflows:

- Issue refinement for labeled issues (default: `ai:refine`)
- PR specialist review for labeled pull requests (default: `ai:review`)

The daemon itself only performs read operations against GitHub REST APIs. Content creation (issue comments, PR reviews) is delegated to Claude via MCP tools.

## Runtime Flow

1. Load config + `.env`, initialize logger/store/clients.
2. Register configured repos in DB (`repos` table).
3. Poll each enabled repo on interval with jitter/backoff (`internal/poller`).
4. Fetch updated issues and PRs from GitHub (`internal/github`).
5. For each work item, enforce label gate and compute deterministic fingerprint (`internal/workflow/fingerprint.go`).
6. Create deduplicated workflow run record (`workflow_runs`).
7. Acquire DB lock (`locks`) to avoid concurrent processing.
8. Build workflow prompt and invoke Claude runner (`internal/claude`).
9. Persist returned artifacts (`posted_artifacts`) and mark run status.

## Key Components

- `internal/poller`: scheduling, interval backoff, repo polling loop.
- `internal/workflow`: business logic, idempotency, quotas, locking.
- `internal/github`: GitHub API reads for issues/PRs/comments/files.
- `internal/claude`: prompt templates + command/noop runner.
- `internal/store`: schema and SQL access for state persistence.

## Data Model (High Level)

- `repos`: tracked repository and polling cursors.
- `work_items`: tracked issue/PR entities.
- `workflow_runs`: execution history keyed by fingerprint.
- `posted_artifacts`: externally posted outputs, deduped per run part.
- `locks`: short-lived pessimistic lock per work item.

## Important Constraints

- Do not introduce GitHub write behavior directly in Go GitHub client.
- Maintain output contract for Claude command mode:
  - exactly one JSON object on stdout when output is provided,
  - `summary` + `artifacts[]` fields.
- Keep fingerprint determinism stable unless versioning is intentionally changed.
