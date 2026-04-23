# CLAUDE.md

## Project Overview

**agents** is a self-hosted Go daemon that dispatches AI CLIs (Claude, Codex) to work on GitHub repos. Agents are configured declaratively in YAML and bound to repos via labels, GitHub event subscriptions (event-driven), and/or cron schedules (autonomous). All GitHub writes happen through the AI backend's MCP tools — the daemon itself is read-only against GitHub. The daemon also ships a built-in Anthropic↔OpenAI translation proxy so the `claude` CLI can be routed through any OpenAI-compatible backend (local `llama.cpp`, hosted Qwen, vLLM, etc.) — see [`docs/local-models.md`](docs/local-models.md). Agents can additionally invoke each other at runtime via the reactive inter-agent dispatcher (see Architecture Notes).

## Directory Structure

```
cmd/agents/main.go          # Daemon entry point + --run-agent / --db / --import modes
internal/
  config/                   # YAML parsing, prompt/skill file resolution, validation
  ai/                       # Prompt composition + command-based CLI runner (per-backend env)
  anthropic_proxy/          # Built-in Anthropic↔OpenAI Chat Completions translation proxy
  observe/                  # Observability store: events, traces, dispatch graph, SSE hubs
  autonomous/               # Cron scheduler + agent memory (SQLite-backed)
  store/                    # SQLite-backed config store: Open, Import, Load, CRUD
  workflow/                 # Event routing engine, single event queue, processor, dispatcher
  webhook/                  # HTTP server, HMAC verification, delivery dedupe, CRUD API handlers
  ui/                       # Embedded Next.js web dashboard (served at /ui/)
  setup/                    # Interactive first-time setup command
  logging/                  # zerolog setup
docs/                       # Long-form docs: configuration, events, dispatch, API, docker, local-models, security
```

## Config Model

The config file has four top-level domains:

- `daemon` — log, http, processor (incl. `dispatch` safety limits), ai_backends, optional `proxy` block
- `skills` — map of reusable guidance blocks, keyed by name (inline or file-based at import time; stored in SQLite)
- `agents` — list of named capabilities (backend + skills + prompt, optional `allow_prs` / `allow_dispatch` / `can_dispatch` / `description`)
- `repos` — list of repos and their `use[]` bindings (which agents run, and with what triggers)

An agent is a pure capability definition — it doesn't run until a repo binds it. A binding sets exactly one trigger: `labels: [...]`, `events: [...]`, or `cron: "..."`. The same agent can have multiple bindings on the same repo with different triggers.

No framework prompt templates. Each agent owns its full prompt; skill guidance is concatenated in Go code before the agent's prompt at render time. A runtime "Available experts" roster is also injected — see Architecture Notes.

## Build & Run

```bash
go test ./... -race
go build -o agents ./cmd/agents

# Import config into SQLite and start
./agents --db agents.db --import config.yaml   # one-time import
./agents --db agents.db                         # subsequent starts

# On-demand single agent pass (synchronous, drains any dispatch chain, exits)
./agents --db agents.db --run-agent <agent-name> --repo owner/repo
```

## Docker

```bash
docker compose build
docker compose up -d
```

Multi-stage build on `node:22-alpine` so the image includes Claude Code, Codex, and `gh` CLIs alongside the daemon. Runs as non-root `agents` user. Default CMD is `--db /var/lib/agents/agents.db`. Compose mounts:
- `./config.yaml` → `/etc/agents/config.yaml` (read-only; used for `--import` seeding)
- Claude/Codex/gh config dirs from host
- `agents-data` named volume → `/var/lib/agents` (SQLite database persistence)

## Environment Variables

- `GITHUB_WEBHOOK_SECRET` — HMAC shared secret (`daemon.http.webhook_secret_env`)
- `LOG_SALT` — optional prompt-log redaction salt (`daemon.ai_backends.<name>.redaction_salt_env`)

## Architecture Notes

- Event-driven for label-based workflows; cron scheduler for autonomous agents. Both paths resolve to the same agent definitions.
- HTTP endpoints:
  - `GET /status` — JSON with uptime, event queue depth, agent schedules, dispatch counters, and orphaned-agent summary.
  - `POST /webhooks/github` — HMAC-verified webhook receiver.
  - `POST /run` — on-demand agent trigger (body: `{"agent":"<name>","repo":"owner/repo"}`).
  - `POST /v1/messages` — Anthropic↔OpenAI translation proxy (disabled by default; enabled via `daemon.proxy.enabled: true`).
  - `GET /v1/models` — companion stub for `/v1/messages`; returns the configured upstream model. Only mounted when the proxy is enabled.
  - `GET /agents` — fleet snapshot with per-agent status, skills, dispatch wiring, bindings.
  - `GET /agents/orphans/status` — DB-only orphan report (agents pinning models unavailable in backend model catalogs).
  - `GET /events[/stream]` — recent events + SSE firehose.
  - `GET /traces[/stream]` — recent agent run traces with timing, summary, status + SSE.
  - `GET /traces/{root_event_id}` — all spans for a single root event.
  - `GET /traces/{span_id}/steps` — tool-loop transcript (ordered tool calls + durations) for a completed agent span.
  - `GET /graph` — agent interaction graph (dispatch edges + counts).
  - `GET /dispatches` — dispatch dedup store snapshot + counters.
  - `GET /memory/{agent}/{repo}` — raw agent memory markdown.
  - `GET /memory/stream` — memory change notifications (SSE).
  - `GET /config` — effective parsed config (secrets redacted).
  - `GET /ui/` — embedded web dashboard (Next.js static assets).
  - CRUD endpoints (always mounted): `GET|POST /skills`, `GET|POST /backends`, `GET|POST /repos`, `POST /agents`, plus item routes (`GET|DELETE /agents/{name}`, `GET|DELETE /skills/{name}`, `GET|PATCH|DELETE /backends/{name}`, `GET|DELETE /repos/{owner}/{repo}`).
  - Backend diagnostics endpoints: `GET /backends/status`, `POST /backends/discover`, `POST /backends/local`.
  - `GET /export`, `POST /import` — export/import fleet config as YAML.
- Supported webhook events: `issues.*` (labeled, opened, edited, reopened, closed), `pull_request.*` (labeled, opened, synchronize, ready_for_review, closed), `issue_comment.created`, `pull_request_review.submitted`, `pull_request_review_comment.created`, `push` (branches only). Label-triggered routing uses `payload.label.name`. Non-label `events:` subscriptions match the event kind exactly. Draft PRs skip `pull_request.labeled`.
- Internal event kinds (not from webhooks): `agents.run` (on-demand trigger from `POST /run` or `--run-agent`), `agent.dispatch` (inter-agent dispatch), `autonomous` (cron scheduler).
- Duplicate webhook suppression via `X-GitHub-Delivery` TTL cache.
- Workflow execution is stateless in-process. Only autonomous agents persist memory (per-agent, per-repo).
- Memory is delivered to the agent as part of its prompt context, and the agent returns its full updated memory in the `memory` field of the JSON response. The daemon writes the value back to the store after the run. An empty string clears the memory. Event-driven runs (webhook events, label triggers) do not receive or persist memory.
- Memory backend: SQLite (stored in the `memory` table alongside config data).
- Backend resolution: agents must explicitly name a backend (no `auto` behavior). Built-ins are `claude` and `codex`; additional local backends are named entries with `local_model_url`.
- Startup auto-discovery runs only when the backends table is empty. Manual refresh is explicit via `POST /backends/discover`.
- Runtime guardrail: if an agent pins a model not present in its backend's DB model catalog, the run fails fast with an actionable error (and the agent appears in orphan reports).
- Local-model routing is configured via `local_model_url`; the daemon injects `ANTHROPIC_BASE_URL` for that backend at runtime. See [`docs/local-models.md`](docs/local-models.md).
- **Reactive inter-agent dispatch**: agents can return a `dispatch: [{agent, number, reason}]` array in their JSON response to invoke other agents. Enqueued as synthetic `agent.dispatch` events. Target must opt in via `allow_dispatch: true`; originator must whitelist targets in `can_dispatch: [...]`. Safety limits (`daemon.processor.dispatch.{max_depth, max_fanout, dedup_window_seconds}`) prevent cascade storms and duplicate invocations. The originating agent's prompt receives an `## Available experts` roster listing dispatchable targets.

## Contribution Model

This project is built by its own agent fleet. External contributions come as issues, not code PRs — the coder agent implements accepted issues, the pr-reviewer validates, and a maintainer merges. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the full flow and exceptions (doc typo fixes and security patches are accepted as direct PRs).

## Security Notes

- Webhook authenticity is enforced with HMAC SHA-256 signature verification.
- All endpoints are unauthenticated at the daemon level; access control is the reverse proxy's responsibility.
- Prompts are never logged in plaintext; only the hash and length are recorded.
- The daemon delegates all GitHub writes to the configured AI backend via MCP tools.
