# CLAUDE.md

## Project Overview

**agents** is a Go daemon that receives GitHub webhook events for issue and PR label updates, then invokes Claude/Codex CLIs (via MCP tools) to post issue refinement comments and PR specialist reviews.

## Directory Structure

```
cmd/agents/main.go          # Daemon entry point
internal/
  config/config.go          # YAML config parsing, env var resolution, defaults
  ai/*                      # Prompt generation + command runner contract
  workflow/*                # Label parsing and event-driven orchestration
  webhook/*                 # HTTP server, signature verification, delivery dedupe
  logging/logging.go        # zerolog structured logger setup
```

## Build & Run

```bash
go test ./...
go build -o agents ./cmd/agents
go run ./cmd/agents -config config.yaml

# On-demand single agent pass (synchronous, exits after completion)
./agents -config config.yaml --run-agent <agent-name> --repo owner/repo
```

## Docker

```bash
docker compose build
docker compose up -d
```

The image uses a multi-stage build (golang alpine builder + scratch runtime) to produce a minimal container with only the static binary and CA certificates. The compose file mounts `config.yaml` read-only into `/etc/agents/` and loads secrets from `.env`.

## Configuration

Main runtime secret:

- `GITHUB_WEBHOOK_SECRET` - shared secret for `X-Hub-Signature-256` verification (`http.webhook_secret_env`)

Optional prompt-log redaction salt:

- `LOG_SALT` (`ai_backends.<name>.redaction_salt_env`)

## Architecture Notes

- Event-driven for label-based workflows; cron scheduler for autonomous agents.
- HTTP endpoints:
  - `GET /status` — returns JSON with uptime, queue depths, and agent schedules
  - `POST /webhooks/github` — HMAC-verified webhook receiver
  - `POST /agents/run` — on-demand agent trigger (requires `http.api_key_env`)
- Relevant webhook events:
  - `issues` and `pull_request`
  - `action` in `labeled`
  - trigger label from `payload.label.name`
- Duplicate webhook delivery suppression by `X-GitHub-Delivery` with TTL cache.
- Workflow execution is stateless in-process (no persistent workflow-tracking database).
- Autonomous agents run on cron schedules; each agent defines its own `skills` and sequential `tasks`. Agent memory is persisted under `memory_dir`.

## Security Notes

- Webhook authenticity is enforced with HMAC SHA-256 signature verification.
- Prompts are not logged in plaintext; prompt hashes are logged instead.
- The daemon delegates GitHub writes to the configured AI backend via MCP tools.
