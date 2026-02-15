# Architecture Overview

## What This Service Does

`agentd` is an event-driven daemon that receives GitHub webhooks and triggers AI-assisted workflows:

- Issue refinement for labeled issues (default: `ai:refine`)
- PR specialist review for labeled pull requests (default: `ai:review`)

Content creation (issue comments, PR reviews) is delegated to configured AI backends via MCP tools.

## Runtime Flow

1. Load config + `.env`, initialize logger, runners, workflow engine, webhook server.
2. Receive GitHub webhook events on `/webhooks/github`.
3. Verify `X-Hub-Signature-256` and dedupe by `X-GitHub-Delivery`.
4. Route `issues` / `pull_request` label events to workflow engine.
5. Compute deterministic fingerprint from webhook payload.
6. Build workflow prompt and invoke selected backend runner.

## Key Components

- `internal/webhook`: HTTP server, signature verification, delivery dedupe.
- `internal/workflow`: label routing + backend invocation from event payloads.
- `internal/ai`: prompt templates + command/noop runner.

## Important Constraints

- Maintain output contract for Claude command mode:
  - exactly one JSON object on stdout when output is provided,
  - `summary` + `artifacts[]` fields.
- Keep fingerprint determinism stable unless versioning is intentionally changed.
