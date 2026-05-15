# CLAUDE.md

## Project Overview

**agents** is a self-hosted Go daemon that dispatches AI CLIs (Claude, Codex) to work on GitHub repos. Agents are configured declaratively in YAML and bound to repos via labels, GitHub event subscriptions (event-driven), and/or cron schedules (autonomous). GitHub operations happen through the AI backend inside a fresh runner container: GitHub MCP tools are preferred, with `gh` available as fallback for complex local checkout/test/PR loops. The daemon itself is read-only against GitHub. The daemon also ships a built-in Anthropic↔OpenAI translation proxy so the `claude` CLI can be routed through any OpenAI-compatible backend (local `llama.cpp`, hosted Qwen, vLLM, etc.), see [`docs/local-models.md`](docs/local-models.md). Agents can additionally invoke each other at runtime via the reactive inter-agent dispatcher (see Architecture Notes).

## Directory Structure

```
cmd/agents/main.go          # Daemon entry point + --db / --import flags
internal/
  fleet/                    # Domain entities: Workspace, Agent, Prompt, Skill, Guardrail, Repo, Backend, Binding (zero deps)
  config/                   # YAML parsing, prompt/skill file resolution, validation (uses fleet)
  ai/                       # Prompt composition + command-based CLI runner (per-backend env)
  anthropic_proxy/          # Built-in Anthropic↔OpenAI Chat Completions translation proxy
  observe/                  # Observability store: events, traces, dispatch graph, SSE hubs
  scheduler/                # Cron scheduler + agent memory (SQLite-backed)
  runtime/                  # Runner interface + ContainerSpec/ExitStatus types, Docker implementation, per-backend container setup (env, scripts, paths)
  backends/                 # Backend discovery: CLI probing, GitHub MCP health checks, tool diagnostics, orphan detection
  store/                    # SQLite-backed config + event_queue store: Open, Import, Load, CRUD; *store.Store facade hides the *sql.DB
  workflow/                 # Event routing engine, durable event queue (persist-on-push + replay), processor, dispatcher
  daemon/                   # Daemon as a single composed unit: lifecycle, router, /status, /run, proxy + UI + MCP mounts
  daemon/observe/           # Observability HTTP handlers (events, traces, graph, dispatches, memory, SSE)
  daemon/config/            # /config snapshot, /export, /import HTTP handlers and methods; token budget CRUD and leaderboard endpoints
  daemon/fleet/             # Agents/skills/backends CRUD + GET /agents fleet view + orphans (incl. /agents/orphans/status)
  daemon/repos/             # Repos + per-binding HTTP CRUD handlers and methods
  daemon/runners/           # /runners listing + delete + retry (event_queue × traces JOIN; in-flight + completed runs)
  webhook/                  # GitHub webhook receiver only: HMAC verification, delivery dedupe, /webhooks/github event parsing
  mcp/                      # MCP server exposing fleet-management tools at /mcp
  ui/                       # Embedded Next.js web dashboard (served at /ui/)
  logging/                  # zerolog setup
docs/                       # Long-form docs: configuration, events, dispatch, API, docker, local-models, security
```

## Config Model

The import/export config file is a fleet strategy document with reusable catalog domains plus workspace-local wiring:

- `backends`, AI CLI/runtime definitions agents can use
- `prompts`, reusable prompt catalog entries, optionally scoped by workspace/repo
- `skills`, reusable guidance catalog entries, keyed by stable id and optionally scoped by workspace/repo
- `guardrails`, reusable policy catalog entries, optionally scoped by workspace/repo
- `workspaces`, operational contexts with selected guardrails, workspace-local agents, repos, and token budgets

An agent is a workspace-local capability definition, it doesn't run until a repo in the same workspace binds it. Agents persist stable `prompt_id` references; human-facing inputs may use `prompt_ref` plus optional `prompt_scope` (`global`, `workspace`, or `workspace/owner/repo`, case-insensitive), which is resolved to `prompt_id` at the API boundary. Legacy inline prompt imports are accepted into the prompt catalog for migration compatibility. Skills are stored as stable ids so duplicate display names across catalog scopes stay deterministic. A binding sets exactly one trigger: `labels: [...]`, `events: [...]`, or `cron: "..."`. The same agent can have multiple bindings on the same repo with different triggers.

The dashboard's graph-first workflow designer is the primary visual workflow surface. It keeps graph node identity/layout on stable agent database IDs, edits agents through the shared form, shows repo-scoped agents inside dashed repo boundaries, shows workspace-scoped agents outside those boundaries, draws thin binding lines to passive repo anchors for trigger bindings, and edits dispatch edges through `can_dispatch` / `allow_dispatch`.

No framework prompt templates. Each agent owns its full prompt; skill guidance is concatenated in Go code before the agent's prompt at render time. A runtime "Available experts" roster is also injected, see Architecture Notes.

## Build & Run

The supported runtime is Docker Compose, see the Docker section below. For development and PR iteration, run `go test ./...`; use targeted `go test ./internal/<pkg> -race` when changing concurrent code. Agents should not run full `go test ./... -race` locally unless explicitly asked; GitHub PR CI runs the normal suite, and `main` CI runs the full race suite after merge. The multi-stage Dockerfile handles `go build` during image build, no local-binary workflow.

On-demand runs go through the running daemon: `POST /run` (HTTP) or the `trigger_agent` MCP tool. There is no separate CLI mode for ad-hoc execution, it would be a second runtime that doesn't share the daemon's run-lock or dispatch dedup, opening a memory-write race window.

## Docker

```bash
docker compose pull
docker compose up -d
```

The default compose file pulls the published `ghcr.io/eloylp/agents:latest` daemon image. `latest` is release-only; main-branch builds are explicit `dev-<short_sha>` tags. The Dockerfile also builds `ghcr.io/eloylp/agents-runner`, which contains Claude Code, Codex, git, GitHub CLI, Go, Rust/Cargo, Node/npm, TypeScript, and runner tools. The daemon image is the minimal control plane. Default CMD is `--db /var/lib/agents/agents.db`. Compose mounts:
- `agents-data` named volume → `/var/lib/agents` (SQLite database persistence)
- `/var/run/docker.sock` → `/var/run/docker.sock` so the daemon can create ephemeral runner containers. This is root-equivalent access to the Docker host and must be treated as a serious deployment boundary.

The daemon image itself defaults to the non-root `agents` user, but the shipped Compose file sets `user: "0:0"` so Docker socket access works on hosts where `/var/run/docker.sock` belongs to a host-specific `docker` group ID. Operators who replace this with a group-based setup must ensure the daemon can create and remove runner containers before enabling scheduled runs.

YAML config is import/export only, not a runtime input. To seed an empty fleet, POST a YAML payload at `/import`.

## Environment Variables

- `GITHUB_WEBHOOK_SECRET`, HMAC shared secret for the webhook receiver.
- `GITHUB_TOKEN`, Personal Access Token injected into runner containers for GitHub MCP and `gh` fallback. `repo` scope minimum; add `workflow` if agents touch CI.
- Claude credentials: `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`, or `ANTHROPIC_AUTH_TOKEN`.
- Codex credentials: `CODEX_AUTH_JSON_BASE64` for ChatGPT/Plus/Pro CLI subscription auth, or `OPENAI_API_KEY` for OpenAI Platform API-billed usage.
- Daemon runtime settings can be overridden at startup with `AGENTS_*` env vars for log, HTTP, processor, and dispatch fields. See `docs/configuration.md` for the full mapping. Empty env vars are ignored, and changes still require a process/container restart.

## Architecture Notes

- Event-driven for label-based workflows; cron scheduler for autonomous agents. Both paths resolve to the same agent definitions.
- **Durable event queue.** Every `PushEvent` writes the event to the SQLite `event_queue` table before signalling workers via the in-memory channel, the DB is the source of truth, the channel is just a wake-up notification. At startup the daemon replays rows whose `completed_at` is still `NULL` so events buffered at shutdown (or runs interrupted mid-prompt) get a second chance instead of vanishing. An hourly cleanup loop prunes completed rows older than 7 days. `/runners` exposes the table, JOINed with traces, for inspection, deletion, and retry.
- **Prompts and tokens on the trace.** Every completed run gzips its composed prompt onto the `traces` row and stores the AI CLI's reported token usage (input / output / cache_read / cache_write, Anthropic shape; OpenAI/Codex emits only input/output). Surfaced on `/runners`, `/traces`, and the UI's expanded panels. The prompt body is fetched lazily via `GET /traces/{span_id}/prompt` to keep listings cheap. Logs no longer carry a prompt hash, the trace span IS the audit record. Persistence is gated by daemon auth after first-user setup.
- **Structured concurrency.** Every long-lived goroutine implements `Run(ctx) error`. The daemon arranges them in two errgroup tiers with separate contexts: producers (HTTP listener, cron scheduler) live on a context derived from the parent, they stop emitting webhooks and cron events as soon as the parent fires; consumers (worker pool, delivery dedup eviction, dispatch dedup eviction, queue cleanup, the one-shot replay step) live on a separate background context that outlives the producer tier so the queue can drain after producers stop. Phase boundaries are logged.
- HTTP endpoints:
  - `GET /status`, JSON with uptime, event queue depth, agent schedules, dispatch counters, and orphaned-agent summary.
  - `POST /webhooks/github`, HMAC-verified webhook receiver.
  - `POST /run`, on-demand agent trigger (body: `{"agent":"<name>","repo":"owner/repo"}`).
  - `POST /v1/messages`, Anthropic↔OpenAI translation proxy (disabled by default; enabled via `AGENTS_PROXY_ENABLED=true`).
  - `GET /v1/models`, companion stub for `/v1/messages`; returns the configured upstream model. Only mounted when the proxy is enabled.
  - `GET /agents`, fleet snapshot with per-agent status, skills, dispatch wiring, bindings.
  - `GET /agents/orphans/status`, DB-only orphan report (agents pinning models unavailable in backend model catalogs).
  - `GET /events[/stream]`, recent events + SSE firehose.
  - `GET /runners`, paginated runner-row listing. Each event_queue row is JOINed with traces: completed events with N agents fan out to N rows on the wire, completed events with no matching trace appear as one `skipped` row, and in-flight events appear as 1 row with `agent: null`. Carries event metadata + per-run trace fields. `?status=enqueued|running|completed` filters on event_queue lifecycle; `?limit` / `?offset` paginate.
  - `DELETE /runners/{id}`, best-effort row removal (event-level). A worker that has already received the QueuedEvent from the in-memory channel may still run it; the row simply won't appear in subsequent listings.
  - `POST /runners/{id}/retry`, re-enqueue an event by copying its blob into a fresh event_queue row and pushing onto the channel. Re-runs every fanned-out agent (event-level retry). The original row stays as audit history. Returns 409 when the source is in `running` state.
  - `GET /traces[/stream]`, recent agent run traces with timing, summary, status + SSE.
  - `GET /traces/{root_event_id}`, all spans for a single root event.
  - `GET /traces/{span_id}/steps`, tool-loop transcript (ordered tool calls + durations) for a completed agent span.
  - `GET /traces/{span_id}/prompt`, composed prompt the daemon sent to the AI CLI for this run (text/plain). Stored gzipped on the trace row.
  - `GET /traces/{span_id}/stream`, SSE stream of persisted `TraceStep` rows (one JSON object per line). The daemon parses AI CLI stdout incrementally, writes each step to `trace_steps`, replays committed rows on connect, then live-tails newly committed rows. Emits `event: end` on run completion. SQLite is the transcript source of truth; the in-memory channel is only a notification path.
  - `GET /graph`, workspace-scoped agent graph data (agent nodes plus dispatch edges + counts). The UI overlays repo-scope boundaries and passive repo binding anchors from the agents/repos payloads.
  - `GET /dispatches`, dispatch dedup store snapshot + counters.
  - `GET /memory/{agent}/{repo}`, raw agent memory markdown.
  - `GET /memory/stream`, memory change notifications (SSE).
  - `GET /config`, current fleet config snapshot.
  - `GET /`, public login/bootstrap page; authenticated browser sessions redirect to `/ui/`.
  - `GET /ui/`, embedded web dashboard (Next.js static assets).
  - CRUD endpoints (always mounted): `GET|POST /skills`, `GET|POST /backends`, `GET|POST /repos`, `POST /agents`, `GET|POST /guardrails`, plus item routes (`GET|PATCH|DELETE /agents/{name}`, `GET|PATCH|DELETE /backends/{name}`, `GET|PATCH|DELETE /repos/{owner}/{repo}`). Catalog item routes are `GET|PATCH|DELETE /prompts/{id}`, `GET|PATCH|DELETE /skills/{id}`, `GET|PATCH|DELETE /guardrails/{id}`, and `POST /guardrails/{id}/reset`; they use stable IDs because scoped catalog entries may share display names, with legacy global names accepted as a compatibility fallback. PATCH is partial-update semantics, only fields present in the JSON body are applied, the rest are preserved; POST remains full-replace. Exception: `PATCH /repos/{owner}/{repo}` is enabled-only (the only patchable repo field is `enabled`); use the per-binding routes for binding edits, or `POST /repos` for a full replace including bindings. Atomic per-binding routes: `POST /repos/{owner}/{repo}/bindings`, `GET|PATCH|DELETE /repos/{owner}/{repo}/bindings/{id}`. Exception: binding `PATCH` is a full-replace, all fields (agent, labels, events, cron, enabled) must be supplied. Guardrail PATCH covers `description`, `content`, `enabled`, `position`; `is_builtin` and `default_content` are migration-managed and not patchable. `POST /guardrails/{id}/reset` copies `default_content` back into `content` (built-ins only; operator-added rows return 400).
  - Backend diagnostics endpoints: `GET /backends/status`, `POST /backends/discover`, `POST /backends/local`.
  - `GET /export`, `POST /import`, export/import fleet config as YAML (includes reusable prompts, skills, guardrails, backends, and workspace-local agents, repos, guardrail references, and token budgets).
  - Token budget endpoints: `GET|POST /token_budgets`, `GET /token_budgets/alerts`, `GET|PATCH|DELETE /token_budgets/{id}`, `GET /token_leaderboard?workspace=&repo=&period=`.
  - `POST /mcp`, MCP (Model Context Protocol) Streamable HTTP endpoint. Registered MCP clients (Claude Code, Cursor, Cline) discover fleet-management tools automatically. The MCP surface covers workspace-aware fleet CRUD (agents, prompts, skills, backends, repos, bindings, guardrails, workspace guardrail selections), config import/export, token budgets/leaderboard, runners, dispatch graph, memory, events, traces, and on-demand `trigger_agent`. Prompt item tools use stable prompt IDs, or human selectors with `name` plus `scope` / `prompt_ref` plus `prompt_scope` for case-insensitive paths like `default/eloylp/agents`. `update_agent`, `update_prompt`, `update_skill`, `update_backend`, and `update_guardrail` follow partial-update semantics, only supplied fields are changed. `update_repo` is the exception: only `enabled` is patchable. `update_binding` is a full-replace (all binding fields required), matching the binding `PATCH` route. Queue tools mirror the `/runners` REST surface (list / delete / retry).
- Supported webhook events: `issues.*` (labeled, opened, edited, reopened, closed), `pull_request.*` (labeled, opened, synchronize, ready_for_review, closed), `issue_comment.created`, `pull_request_review.submitted`, `pull_request_review_comment.created`, `push` (branches only). Label-triggered routing uses `payload.label.name`. Non-label `events:` subscriptions match the event kind exactly. Draft PRs skip `pull_request.labeled`.
- Internal event kinds (not from webhooks): `agents.run` (on-demand trigger from `POST /run` or MCP `trigger_agent`), `agent.dispatch` (inter-agent dispatch), `cron` (cron-scheduler tick).
- Duplicate webhook suppression via `X-GitHub-Delivery` TTL cache.
- Memory persistence is a per-agent property, not a per-trigger property. When `allow_memory: true` (the default), the daemon reads existing memory before every run and persists the response's `memory` field back to the store afterwards, uniformly across every trigger surface (cron tick, webhook events, inter-agent dispatch, `POST /run`, MCP `trigger_agent`). Setting `allow_memory: false` on an agent disables memory load AND persist for every run kind, useful for inherently stateless agents that recompute their context each run.
- Memory is delivered to the agent as part of its prompt context, and the agent returns its full updated memory in the `memory` field of the JSON response. The daemon writes the value back to the store after the run. An empty string clears the memory. The built-in `memory-scope` guardrail tells agents to ignore CLI-native/global/session memory and use only the daemon-rendered `Existing memory:` section for the current workspace/repo/agent.
- Memory backend: SQLite (stored in the `memory` table alongside config data).
- Backend resolution: agents must explicitly name a backend (no `auto` behavior). Built-ins are `claude` and `codex`; additional local backends are named entries with `local_model_url`.
- Startup auto-discovery runs only when the backends table is empty. Manual refresh is explicit via `POST /backends/discover`.
- Runtime guardrail: if an agent pins a model not present in its backend's DB model catalog, the run fails fast with an actionable error (and the agent appears in orphan reports).
- Local-model routing is configured via `local_model_url`; the daemon injects `ANTHROPIC_BASE_URL` into the runner container for that backend at runtime. The URL must be reachable from the runner container. See [`docs/local-models.md`](docs/local-models.md).
- **Reactive inter-agent dispatch**: agents can return a `dispatch: [{agent, number, reason}]` array in their JSON response to invoke other agents. Enqueued as synthetic `agent.dispatch` events. Target must opt in via `allow_dispatch: true`; originator must whitelist targets in `can_dispatch: [...]`. Safety limits are process-owned daemon settings configured by `AGENTS_DISPATCH_MAX_DEPTH`, `AGENTS_DISPATCH_MAX_FANOUT`, and `AGENTS_DISPATCH_DEDUP_WINDOW_SECONDS`. The originating agent's prompt receives an `## Available experts` roster listing dispatchable targets.
- **Prompt guardrails**: the `guardrails` table holds reusable policy blocks, and `workspace_guardrails` selects the ordered subset rendered for each workspace. The renderer combines mandatory dynamic workspace/repository boundary guidance and selected workspace guardrails in one guardrails section ahead of skills and selected prompt content. Operators can edit the reusable catalog and manage selected workspace references via the `Guardrails` tab in `/ui/config`, the REST surface, or the MCP tools.
- **Token budgets**: token caps over daily/weekly/monthly UTC calendar periods for global, workspace, repo, agent, backend, and workspace-combined scopes (`workspace+repo`, `workspace+agent`, `workspace+backend`, `workspace+repo+agent`). Checked before each agent run through the concrete SQLite store; fail-open with error logging so a broken `token_budgets` table never blocks runs. Alert thresholds (0 = disabled, 1-100 = percentage of cap) drive the NavBar danger banner. Token leaderboard aggregates per-agent usage from the `traces` table, including total tokens, run count, and average tokens per run. Budget CRUD is exposed at `/token_budgets` (REST) and via the `list_token_budgets`, `create_token_budget`, `update_token_budget`, `delete_token_budget` MCP tools. `PATCH /token_budgets/{id}` and `update_token_budget` are partial-update surfaces. The `get_token_leaderboard` MCP tool and `GET /token_leaderboard` endpoint aggregate per-agent usage. Budgets are included in `/export` and `/import` round-trips.

## Contribution Model

This project is built alongside its own agent fleet. Both paths are welcome:
- **Issue path**: open an issue. If a maintainer applies the `ai ready` label, the coder agent picks it up on its next run, opens a PR, and the pr-reviewer validates. A maintainer merges.
- **PR path**: open a pull request directly. The maintainer either reviews it themselves or applies `ai ready` to invite the pr-reviewer agent. A human always makes the final merge decision.

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the full flow, label semantics, and security disclosure.

## Security Notes

- Webhook authenticity is enforced with HMAC SHA-256 signature verification.
- Sensitive API and MCP endpoints require daemon auth after first-user setup. Browser users sign in or bootstrap at `/`, receive DB-backed opaque session tokens in `HttpOnly` cookies, and are redirected into `/ui/`; MCP/API clients use named DB-backed bearer tokens. The first bootstrapped dashboard user is the admin user, can create/remove additional non-admin users through the UI/REST auth surface, and cannot be removed. User/password administration is intentionally not exposed as MCP tools, so passwords do not flow through MCP clients.
- Prompts are never logged in plaintext; only the length is recorded.
- The daemon delegates GitHub operations to the configured AI backend; agents prefer MCP tools and may use authenticated gh only as the documented fallback.
