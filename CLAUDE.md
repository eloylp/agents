# CLAUDE.md

## Project Overview

**agents** is a self-hosted Go daemon that dispatches AI CLIs (Claude, Codex) to work on GitHub repos. Agents are configured declaratively in YAML and bound to repos via labels, GitHub event subscriptions (event-driven), and/or cron schedules (autonomous). All GitHub writes happen through the AI backend's MCP tools — the daemon itself is read-only against GitHub. The daemon also ships a built-in Anthropic↔OpenAI translation proxy so the `claude` CLI can be routed through any OpenAI-compatible backend (local `llama.cpp`, hosted Qwen, vLLM, etc.) — see [`docs/local-models.md`](docs/local-models.md). Agents can additionally invoke each other at runtime via the reactive inter-agent dispatcher (see Architecture Notes).

## Directory Structure

```
cmd/agents/main.go          # Daemon entry point + --run-agent mode
internal/
  config/                   # YAML parsing, prompt/skill file resolution, validation
  ai/                       # Prompt composition + command-based CLI runner (per-backend env)
  anthropic_proxy/          # Built-in Anthropic↔OpenAI Chat Completions translation proxy
  observe/                  # Observability store: events, traces, dispatch graph, SSE hubs
  autonomous/               # Cron scheduler + filesystem-backed agent memory
  workflow/                 # Event routing engine, single event queue, processor, dispatcher
  webhook/                  # HTTP server, HMAC verification, delivery dedupe
  setup/                    # Interactive first-time setup command
  logging/                  # zerolog setup
prompts/                    # Optional prompt files referenced by agent prompt_file:
skills/                     # Optional skill files referenced by skill prompt_file:
docs/                       # Long-form docs (docs/local-models.md, etc.)
```

## Config Model

The config file has four top-level domains:

- `daemon` — log, http, processor (incl. `dispatch` safety limits), memory_dir, ai_backends, optional `proxy` block
- `skills` — map of reusable guidance blocks, keyed by name (inline or `prompt_file:`)
- `agents` — list of named capabilities (backend + skills + prompt, optional `allow_prs` / `allow_dispatch` / `can_dispatch` / `description`)
- `repos` — list of repos and their `use[]` bindings (which agents run, and with what triggers)

An agent is a pure capability definition — it doesn't run until a repo binds it. A binding sets exactly one trigger: `labels: [...]`, `events: [...]`, or `cron: "..."`. The same agent can have multiple bindings on the same repo with different triggers.

No framework prompt templates. Each agent owns its full prompt; skill guidance is concatenated in Go code before the agent's prompt at render time. A runtime "Available experts" roster is also injected — see Architecture Notes.

## Build & Run

```bash
go test ./... -race
go build -o agents ./cmd/agents
go run ./cmd/agents -config config.yaml

# On-demand single agent pass (synchronous, drains any dispatch chain, exits)
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
- `./skills` → `/etc/agents/skills` (read-only; referenced by `skills.<name>.prompt_file`)
- Claude/Codex/gh config dirs from host
- `agents-memory` named volume → `/var/lib/agents/memory`

## Environment Variables

- `GITHUB_WEBHOOK_SECRET` — HMAC shared secret (`daemon.http.webhook_secret_env`)
- `AGENTS_API_KEY` — Bearer token for `POST /agents/run` (`daemon.http.api_key_env`)
- `LOG_SALT` — optional prompt-log redaction salt (`daemon.ai_backends.<name>.redaction_salt_env`)

## Architecture Notes

- Event-driven for label-based workflows; cron scheduler for autonomous agents. Both paths resolve to the same agent definitions.
- HTTP endpoints:
  - `GET /status` — JSON with uptime, event queue depth, agent schedules, dispatch counters.
  - `POST /webhooks/github` — HMAC-verified webhook receiver.
  - `POST /agents/run` — on-demand agent trigger (requires Bearer token).
  - `POST /v1/messages` — Anthropic↔OpenAI translation proxy (disabled by default; enabled via `daemon.proxy.enabled: true`).
  - `GET /v1/models` — companion stub for `/v1/messages`; returns the configured upstream model. Only mounted when the proxy is enabled.
  - `POST /api/run` — unauthenticated on-demand trigger (same handler as `/agents/run`; relies on Traefik basic auth for the `/api/*` prefix). Enqueues a synthetic `agents.run` event.
  - `GET /api/agents` — fleet snapshot with per-agent status, skills, dispatch wiring, bindings.
  - `GET /api/events[/stream]` — recent events + SSE firehose.
  - `GET /api/traces[/stream]` — recent agent run traces with timing, summary, status + SSE.
  - `GET /api/graph` — agent interaction graph (dispatch edges + counts).
  - `GET /api/dispatches` — dispatch dedup store snapshot + counters.
  - `GET /api/memory/{agent}/{repo}` — raw agent memory markdown.
  - `GET /api/config` — effective parsed config (secrets redacted).
- Supported webhook events: `issues.*` (labeled, opened, edited, reopened, closed), `pull_request.*` (labeled, opened, synchronize, ready_for_review, closed), `issue_comment.created`, `pull_request_review.submitted`, `pull_request_review_comment.created`, `push` (branches only). Label-triggered routing uses `payload.label.name`. Non-label `events:` subscriptions match the event kind exactly. Draft PRs skip `pull_request.labeled`.
- Internal event kinds (not from webhooks): `agents.run` (on-demand trigger from `/api/run` or `--run-agent`), `agent.dispatch` (inter-agent dispatch), `autonomous` (cron scheduler).
- Duplicate webhook suppression via `X-GitHub-Delivery` TTL cache.
- Workflow execution is stateless in-process. Only autonomous agents persist memory (per-agent, per-repo markdown file under `memory_dir`).
- Agent memory is read before each scheduled run and is the agent's responsibility to update.
- Backend resolution: agents declare `backend: claude | codex | auto`. `auto` picks the first configured backend in preference order (claude > codex).
- Per-backend env overrides (`daemon.ai_backends.<name>.env`) let two backends run the same CLI with different endpoints — e.g. a default `claude` backend on hosted Anthropic plus a `claude_local` backend that routes the CLI via `ANTHROPIC_BASE_URL` through the built-in proxy to a local model. See [`docs/local-models.md`](docs/local-models.md).
- **Reactive inter-agent dispatch**: agents can return a `dispatch: [{agent, number, reason}]` array in their JSON response to invoke other agents. Enqueued as synthetic `agent.dispatch` events. Target must opt in via `allow_dispatch: true`; originator must whitelist targets in `can_dispatch: [...]`. Safety limits (`daemon.processor.dispatch.{max_depth, max_fanout, dedup_window_seconds}`) prevent cascade storms and duplicate invocations. The originating agent's prompt receives an `## Available experts` roster listing dispatchable targets.

## Security Notes

- Webhook authenticity is enforced with HMAC SHA-256 signature verification.
- `/agents/run` is gated by Bearer token; endpoint returns 403 when no token is configured (disabled by default).
- Prompts are never logged in plaintext; only the hash and length are recorded.
- The daemon delegates all GitHub writes to the configured AI backend via MCP tools.
