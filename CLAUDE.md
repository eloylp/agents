# CLAUDE.md

## Project Overview

**agents** (`agentd`) is a Go daemon that receives GitHub webhook events for issue and PR label updates, then invokes Claude/Codex CLIs (via MCP tools) to post issue refinement comments and PR specialist reviews.

## Directory Structure

```
cmd/agentd/main.go          # Daemon entry point
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
go build -o agentd ./cmd/agentd
go run ./cmd/agentd -config config.yaml
```

## Configuration

Main runtime secret:

- `GITHUB_WEBHOOK_SECRET` - shared secret for `X-Hub-Signature-256` verification (`http.webhook_secret_env`)

Optional prompt-log redaction salt:

- `LOG_SALT` (`ai_backends.<name>.redaction_salt_env`)

## Architecture Notes

- Event-driven only (no polling loop).
- HTTP endpoints:
  - `GET /status`
  - `POST /webhooks/github`
- Relevant webhook events:
  - `issues` and `pull_request`
  - `action` in `labeled`
  - trigger label from `payload.label.name`
- Duplicate webhook delivery suppression by `X-GitHub-Delivery` with TTL cache.
- Workflow execution is stateless in-process (no persistent workflow-tracking database).

## Security Notes

- Webhook authenticity is enforced with HMAC SHA-256 signature verification.
- Prompts are not logged in plaintext; prompt hashes are logged instead.
- The daemon delegates GitHub writes to the configured AI backend via MCP tools.
