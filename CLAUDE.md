# CLAUDE.md

## Project Overview

**agents** is a self-hosted Go daemon that dispatches AI CLIs (Claude, Codex) to work on GitHub repos. Agents are configured declaratively in YAML and bound to repos via labels (event-driven) and/or cron schedules (autonomous). All GitHub writes happen through the AI backend's MCP tools — the daemon itself is read-only against GitHub.

## Directory Structure

```
cmd/agents/main.go          # Daemon entry point + --run-agent mode
internal/
  config/                   # YAML parsing, prompt file resolution, validation
  ai/                       # Prompt composition + CLI runner
  autonomous/               # Cron scheduler + filesystem-backed agent memory
  workflow/                 # Event routing engine, queues, processor
  webhook/                  # HTTP server, HMAC verification, delivery dedupe
  setup/                    # Interactive first-time setup command
  logging/                  # zerolog setup
prompts/                    # Optional prompt files referenced by prompt_file:
```

## Config Model

The config file has three top-level domains:

- `daemon` — log, http, processor, memory_dir, ai_backends (how the service runs)
- `skills` — map of reusable guidance blocks, keyed by name
- `agents` — list of named capabilities (backend + skills + prompt)
- `repos` — list of repos and their `use[]` bindings (which agents run, and with what triggers)

An agent is a pure capability definition — it doesn't run until a repo binds it. A binding needs at least one trigger: `labels: [...]` or `cron: "..."`. The same agent can have multiple bindings on the same repo with different triggers.

No framework prompt templates. Each agent owns its full prompt; skill guidance is concatenated in Go code before the agent's prompt at render time.

## Build & Run

```bash
go test ./... -race
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

Multi-stage build on `node:22-alpine` so the image includes Claude Code, Codex, and `gh` CLIs alongside the daemon. Runs as non-root `agents` user. Compose mounts:
- `./config.yaml` → `/etc/agents/config.yaml` (read-only)
- `./prompts` → `/etc/agents/prompts` (read-only)
- Claude/Codex/gh config dirs from host
- `agents-memory` named volume → `/var/lib/agents/memory`

## Environment Variables

- `GITHUB_WEBHOOK_SECRET` — HMAC shared secret (`daemon.http.webhook_secret_env`)
- `AGENTS_API_KEY` — Bearer token for `POST /agents/run` (`daemon.http.api_key_env`)
- `LOG_SALT` — optional prompt-log redaction salt (`daemon.ai_backends.<name>.redaction_salt_env`)

## Architecture Notes

- Event-driven for label-based workflows; cron scheduler for autonomous agents. Both paths resolve to the same agent definitions.
- HTTP endpoints:
  - `GET /status` — JSON with uptime, queue depths, agent schedules
  - `POST /webhooks/github` — HMAC-verified webhook receiver
  - `POST /agents/run` — on-demand agent trigger (requires Bearer token)
  - `POST /v1/messages` — Anthropic↔OpenAI translation proxy (disabled by default; enabled via `daemon.proxy.enabled: true`)
  - `GET /v1/models` — companion stub for `/v1/messages`; returns the configured upstream model. Only mounted when the proxy is enabled.
- Relevant webhook events: `issues.labeled`, `pull_request.labeled`. Trigger label comes from `payload.label.name`.
- Duplicate webhook suppression via `X-GitHub-Delivery` TTL cache.
- Workflow execution is stateless in-process. Only autonomous agents persist memory (per-agent, per-repo markdown file under `memory_dir`).
- Agent memory is read before each scheduled run and is the agent's responsibility to update.
- Backend resolution: agents declare `backend: claude | codex | auto`. `auto` picks the first configured backend in preference order (claude > codex).
- Per-backend env overrides (`daemon.ai_backends.<name>.env`) let two backends run the same CLI with different endpoints — e.g. a default `claude` backend on hosted Anthropic plus a `claude_local` backend that routes the CLI via `ANTHROPIC_BASE_URL` through the built-in proxy to a local model. See [`docs/local-models.md`](docs/local-models.md).

## Security Notes

- Webhook authenticity is enforced with HMAC SHA-256 signature verification.
- `/agents/run` is gated by Bearer token; endpoint returns 403 when no token is configured (disabled by default).
- Prompts are never logged in plaintext; only the hash and length are recorded.
- The daemon delegates all GitHub writes to the configured AI backend via MCP tools.
