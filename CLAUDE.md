# CLAUDE.md

## Project Overview

**agents** is a self-hosted Go daemon that dispatches AI CLIs (Claude, Codex) to work on GitHub repos. Agents are configured declaratively in YAML and bound to repos via labels, GitHub event subscriptions (event-driven), and/or cron schedules (autonomous). All GitHub writes happen through the AI backend's MCP tools — the daemon itself is read-only against GitHub. The daemon also ships a built-in Anthropic↔OpenAI translation proxy so the `claude` CLI can be routed through any OpenAI-compatible backend (local `llama.cpp`, hosted Qwen, vLLM, etc.) — see [`docs/local-models.md`](docs/local-models.md). Agents can additionally invoke each other at runtime via the reactive inter-agent dispatcher (see Architecture Notes).

## Directory Structure

```
cmd/agents/main.go          # Daemon entry point + --db / --import flags
internal/
  fleet/                    # Domain entities: Agent, Repo, Skill, Backend, Binding (zero deps)
  config/                   # YAML parsing, prompt/skill file resolution, validation (uses fleet)
  ai/                       # Prompt composition + command-based CLI runner (per-backend env)
  anthropic_proxy/          # Built-in Anthropic↔OpenAI Chat Completions translation proxy
  observe/                  # Observability store: events, traces, dispatch graph, SSE hubs
  scheduler/                # Cron scheduler + agent memory (SQLite-backed)
  backends/                 # Backend discovery: CLI probing, GitHub MCP health checks, orphan detection
  store/                    # SQLite-backed config + event_queue store: Open, Import, Load, CRUD; *store.Store facade hides the *sql.DB
  workflow/                 # Event routing engine, durable event queue (persist-on-push + replay), processor, dispatcher
  daemon/                   # Daemon as a single composed unit: lifecycle, router, /status, /run, proxy + UI + MCP mounts
  daemon/observe/           # Observability HTTP handlers (events, traces, graph, dispatches, memory, SSE)
  daemon/config/            # /config snapshot, /export, /import HTTP handlers and methods
  daemon/fleet/             # Agents/skills/backends CRUD + GET /agents fleet view + orphans (incl. /agents/orphans/status)
  daemon/repos/             # Repos + per-binding HTTP CRUD handlers and methods
  daemon/queue/             # /queue listing + delete + retry handlers (durable event_queue surface)
  webhook/                  # GitHub webhook receiver only: HMAC verification, delivery dedupe, /webhooks/github event parsing
  mcp/                      # MCP server exposing fleet-management tools at /mcp
  ui/                       # Embedded Next.js web dashboard (served at /ui/)
  setup/                    # Interactive first-time setup command
  logging/                  # zerolog setup
docs/                       # Long-form docs: configuration, events, dispatch, API, docker, local-models, security
```

## Config Model

The config file has four top-level domains:

- `daemon` — log, http, processor (incl. `dispatch` safety limits), ai_backends, optional `proxy` block
- `skills` — map of reusable guidance blocks, keyed by name (inline or file-based at import time; stored in SQLite)
- `agents` — list of named capabilities (backend + skills + prompt, optional `allow_prs` / `allow_dispatch` / `allow_memory` / `can_dispatch` / `description`)
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
```

On-demand runs go through the running daemon: `POST /run` (HTTP) or the `trigger_agent` MCP tool. There is no separate CLI mode for ad-hoc execution — it would be a second runtime that doesn't share the daemon's run-lock or dispatch dedup, opening a memory-write race window.

## Docker

```bash
docker compose build
docker compose up -d
```

Multi-stage build on `node:22-alpine` so the image includes Claude Code and Codex alongside the daemon. Runs as non-root `agents` user. Default CMD is `--db /var/lib/agents/agents.db`. Compose mounts:
- `./config.yaml` → `/etc/agents/config.yaml` (read-only; used for `--import` seeding)
- Claude/Codex config dirs from host (GitHub access flows through the GitHub MCP server configured on those CLIs)
- `agents-data` named volume → `/var/lib/agents` (SQLite database persistence)

## Environment Variables

- `GITHUB_WEBHOOK_SECRET` — HMAC shared secret (`daemon.http.webhook_secret_env`)
- `LOG_SALT` — optional prompt-log redaction salt (`daemon.ai_backends.<name>.redaction_salt_env`)

## Architecture Notes

- Event-driven for label-based workflows; cron scheduler for autonomous agents. Both paths resolve to the same agent definitions.
- **Durable event queue.** Every `PushEvent` writes the event to the SQLite `event_queue` table before signalling workers via the in-memory channel — the DB is the source of truth, the channel is just a wake-up notification. At startup the daemon replays rows whose `completed_at` is still `NULL` so events buffered at shutdown (or runs interrupted mid-prompt) get a second chance instead of vanishing. An hourly cleanup loop prunes completed rows older than 7 days. `/queue` exposes the table for inspection, deletion, and retry.
- **Structured concurrency.** Every long-lived goroutine implements `Run(ctx) error`. The daemon arranges them in two errgroup tiers with separate contexts: producers (HTTP listener, cron scheduler) live on a context derived from the parent — they stop emitting webhooks and cron events as soon as the parent fires; consumers (worker pool, delivery dedup eviction, dispatch dedup eviction, queue cleanup, the one-shot replay step) live on a separate background context that outlives the producer tier so the queue can drain after producers stop. Phase boundaries are logged.
- HTTP endpoints:
  - `GET /status` — JSON with uptime, event queue depth, agent schedules, dispatch counters, and orphaned-agent summary.
  - `POST /webhooks/github` — HMAC-verified webhook receiver.
  - `POST /run` — on-demand agent trigger (body: `{"agent":"<name>","repo":"owner/repo"}`).
  - `POST /v1/messages` — Anthropic↔OpenAI translation proxy (disabled by default; enabled via `daemon.proxy.enabled: true`).
  - `GET /v1/models` — companion stub for `/v1/messages`; returns the configured upstream model. Only mounted when the proxy is enabled.
  - `GET /agents` — fleet snapshot with per-agent status, skills, dispatch wiring, bindings.
  - `GET /agents/orphans/status` — DB-only orphan report (agents pinning models unavailable in backend model catalogs).
  - `GET /events[/stream]` — recent events + SSE firehose.
  - `GET /queue` — paginated listing of the durable event queue (id, kind, repo, number, status, timestamps). `?status=enqueued|running|completed` filters by state; `?limit` / `?offset` paginate.
  - `DELETE /queue/{id}` — best-effort row removal. A worker that has already received the QueuedEvent from the in-memory channel may still run it; the row simply won't appear in subsequent listings.
  - `POST /queue/{id}/retry` — re-enqueue a row by copying its blob into a fresh event_queue row and pushing onto the channel. The original row stays as audit history. Returns 409 when the source is in `running` state.
  - `GET /traces[/stream]` — recent agent run traces with timing, summary, status + SSE.
  - `GET /traces/{root_event_id}` — all spans for a single root event.
  - `GET /traces/{span_id}/steps` — tool-loop transcript (ordered tool calls + durations) for a completed agent span.
  - `GET /graph` — agent interaction graph (dispatch edges + counts).
  - `GET /dispatches` — dispatch dedup store snapshot + counters.
  - `GET /memory/{agent}/{repo}` — raw agent memory markdown.
  - `GET /memory/stream` — memory change notifications (SSE).
  - `GET /config` — effective parsed config (secrets redacted).
  - `GET /ui/` — embedded web dashboard (Next.js static assets).
  - CRUD endpoints (always mounted): `GET|POST /skills`, `GET|POST /backends`, `GET|POST /repos`, `POST /agents`, plus item routes (`GET|PATCH|DELETE /agents/{name}`, `GET|PATCH|DELETE /skills/{name}`, `GET|PATCH|DELETE /backends/{name}`, `GET|PATCH|DELETE /repos/{owner}/{repo}`). PATCH is partial-update semantics — only fields present in the JSON body are applied, the rest are preserved; POST remains full-replace. Atomic per-binding routes: `POST /repos/{owner}/{repo}/bindings`, `GET|PATCH|DELETE /repos/{owner}/{repo}/bindings/{id}`. Exception: binding `PATCH` is a full-replace — all fields (agent, labels, events, cron, enabled) must be supplied.
  - Backend diagnostics endpoints: `GET /backends/status`, `POST /backends/discover`, `POST /backends/local`.
  - `GET /export`, `POST /import` — export/import fleet config as YAML.
  - `POST /mcp` — MCP (Model Context Protocol) Streamable HTTP endpoint. Registered MCP clients (Claude Code, Cursor, Cline) discover fleet-management tools automatically. Currently registered tools: `list_agents`, `get_agent`, `list_skills`, `get_skill`, `list_backends`, `get_backend`, `list_repos`, `get_repo`, `get_status`, `trigger_agent`, `list_events`, `list_traces`, `get_trace`, `get_trace_steps`, `get_graph`, `get_dispatches`, `get_memory`, `get_config`, `export_config`, `import_config`, `create_agent`, `update_agent`, `delete_agent`, `create_skill`, `update_skill`, `delete_skill`, `create_backend`, `update_backend`, `delete_backend`, `create_repo`, `update_repo`, `delete_repo`, `create_binding`, `get_binding`, `update_binding`, `delete_binding`, `list_queue_events`, `delete_queue_event`, `retry_queue_event`. `update_agent`, `update_skill`, `update_backend`, and `update_repo` follow partial-update semantics — only supplied fields are changed, bindings are preserved with their current IDs. Exception: `update_binding` is a full-replace (all binding fields required), matching the binding `PATCH` route. Queue tools mirror the `/queue` REST surface (list / delete / retry). The MCP surface now covers the full fleet inventory declared in #227.
- Supported webhook events: `issues.*` (labeled, opened, edited, reopened, closed), `pull_request.*` (labeled, opened, synchronize, ready_for_review, closed), `issue_comment.created`, `pull_request_review.submitted`, `pull_request_review_comment.created`, `push` (branches only). Label-triggered routing uses `payload.label.name`. Non-label `events:` subscriptions match the event kind exactly. Draft PRs skip `pull_request.labeled`.
- Internal event kinds (not from webhooks): `agents.run` (on-demand trigger from `POST /run` or MCP `trigger_agent`), `agent.dispatch` (inter-agent dispatch), `cron` (cron-scheduler tick).
- Duplicate webhook suppression via `X-GitHub-Delivery` TTL cache.
- Memory persistence is a per-agent property, not a per-trigger property. When `allow_memory: true` (the default), the daemon reads existing memory before every run and persists the response's `memory` field back to the store afterwards, uniformly across every trigger surface (cron tick, webhook events, inter-agent dispatch, `POST /run`, MCP `trigger_agent`). Setting `allow_memory: false` on an agent disables memory load AND persist for every run kind, useful for inherently stateless agents that recompute their context each run.
- Memory is delivered to the agent as part of its prompt context, and the agent returns its full updated memory in the `memory` field of the JSON response. The daemon writes the value back to the store after the run. An empty string clears the memory.
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
