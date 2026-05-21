# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-05-22

### Added

- Fresh ephemeral Docker runner containers for every agent run. The daemon image
  is now the control plane, while agent work runs in a separate
  `ghcr.io/eloylp/agents-runner` container with Claude Code, Codex, GitHub CLI,
  git, Go, Rust/Cargo, Node/npm, TypeScript, and runtime tools.
- A Docker runtime boundary with runner image diagnostics, container constraints,
  backend setup scripts, runner image pull/inspection, and one container per
  run created through the Docker Engine API.
- Workspaces as a first-class scope for agents, repos, triggers, memory,
  observability, runners, graph layout, token budgets, prompt catalogs, skill
  catalogs, and repo-local agents.
- Scoped prompt and skill catalogs with stable public refs, plus prompt
  resolution by `prompt_ref` / `prompt_scope`.
- Multi-user daemon auth administration: admin-only user creation/removal,
  current-user password changes, named API token management, and a root
  login/bootstrap flow that redirects authenticated browser sessions into the
  dashboard.
- Graph workflow improvements: stable agent-ID layout persistence, dispatch
  wiring controls, repo boundary grouping, graph prompt editing, transcript
  links, and a persistent sidebar focus mode.
- Trace-step persistence and streaming so live tool-loop transcripts are stored
  durably, replayed to subscribers, and surfaced from trace detail endpoints.
- Codex auth cache injection through `CODEX_AUTH_JSON_BASE64`, OpenAI API-key
  fallback setup, and explicit runner git identity configuration from
  `AGENTS_GIT_USER_NAME` / `AGENTS_GIT_USER_EMAIL`.
- Authenticated `gh` fallback tooling inside runner containers for complex local
  checkout/test/PR loops when GitHub MCP tools are insufficient.
- A one-line quickstart installer script that downloads the Compose bundle,
  guides credential setup, asks for Codex auth mode, and starts the daemon.

### Changed

- The shipped Compose file now pulls `ghcr.io/eloylp/agents:latest`, mounts the
  Docker socket, and runs the daemon container as root for portable Docker
  socket access. `latest` is release-only; `main` publishes explicit
  `dev-<short_sha>` images.
- Docker builds now publish separate daemon and runner images on `main` and
  version tags, including `latest`, `X.Y.Z`, `vX.Y.Z`, and `X.Y` for releases.
- Daemon runtime settings are split from runner runtime settings. Runner image
  and constraints live in SQLite runtime settings, with dashboard, REST, and MCP
  surfaces for inspection and updates.
- Fleet mutations now flow through the service layer, while the store package is
  split by responsibility and backed by stronger SQLite integrity constraints.
- Guardrails are workspace-scoped selections, with refreshed dashboard
  management and documentation.
- Prompt bodies now live in the prompt catalog. Agents reference prompt catalog
  entries instead of carrying inline prompt bodies.
- Fleet import/export, config examples, REST, MCP tools, and the dashboard were
  updated for workspaces, scoped catalogs, runtime settings, and stable internal
  database IDs.
- Dashboard navigation, auth flow, graph overview, backend diagnostics, runners,
  screenshots, and operator docs were refreshed for the current runtime model.
- Runner and backend failures now surface clearer diagnostic detail, including
  backend timeouts and startup/setup failures.
- Testing policy now keeps full `go test ./... -race` on main-branch CI and uses
  normal `go test ./...` plus targeted race tests for local/PR iteration.

### Fixed

- Dispatch authorization now checks runtime wiring as well as config-time
  validation.
- Scoped webhook routing, running status, MCP observability, runner lists, memory
  views, and dispatch dedup by workspace/event intent so multi-workspace fleets
  do not cross-contaminate state.
- Preserved replayed dispatch depth and ensured direct runs clean up after
  panics.
- Persisted observability records synchronously to avoid missing events/traces
  during live views and retries.
- Preserved omitted runtime settings on imports so fleet-only imports do not
  reset runner image or container constraints.
- Fixed prompt migration foreign-key handling and graph prompt edit/error
  flicker paths.
- Fixed stale graph repo filters, collapsed desktop sidebar behavior, graph
  dispatch affordances, and backend diagnostics loading states.
- Switched password hashing to bcrypt and persisted the explicit admin role.
- Relaxed runner package pins and refreshed the runner base image to reduce
  setup breakage.

### Removed

- Removed the legacy `AGENTS_AUTH_BEARER_TOKEN_HASH` auth model. Daemon auth is
  now only DB-backed browser sessions and named API bearer tokens.
- Removed `scripts/setup.sh`, the in-container `agents-setup` flow, and the
  `agents-home` persistent CLI-auth volume. Credentials are now injected into
  fresh runner containers from `.env`.
- Removed legacy inline agent prompt compatibility.

## [0.2.0] - 2026-05-05

### Added

- DB-backed daemon auth for sensitive surfaces: browser sessions for the
  dashboard and named bearer tokens for REST/MCP clients, while keeping
  `/status`, `/webhooks/github`, `/v1/*`, and the UI shell reachable.
- Dashboard token prompt and local token management so browser users can
  authenticate against protected API and SSE endpoints without query-string
  secrets.
- Codex-compatible MCP access with bearer auth, enabling both Claude Code and
  Codex users to manage the fleet through the same `/mcp` endpoint.
- Token budgets and leaderboard across REST, MCP, and the dashboard:
  global/backend/agent caps, daily/weekly/monthly UTC windows, alert
  thresholds, per-scope CRUD, and per-agent average total tokens per run.
- Built-in `discretion` and `memory-scope` guardrails. `discretion` reduces
  noisy public GitHub actions, while `memory-scope` instructs agents to ignore
  CLI-native, global, or session memory and use only the daemon-rendered memory
  for the current `(agent, repo)` pair.
- Daemon runtime environment overrides for log, HTTP, processor, dispatch, and
  proxy settings, documented in `.env.sample` and `docs/configuration.md`.

### Changed

- Daemon runtime configuration is no longer part of the persisted fleet
  config, `/config`, `/export`, `/import`, REST CRUD, or MCP config surfaces.
  Fleet strategy remains database-backed and import/exportable; process
  settings are startup-only environment variables.
- Authenticated live streams now use fetch-based SSE clients so runners,
  traces, events, and memory streams can send `Authorization: Bearer ...`
  headers instead of leaking tokens through URL query parameters.
- Token budget scope selection in the dashboard uses backend/agent dropdowns
  instead of requiring operators to type stored names manually.
- Trace cards in the dashboard are easier to open, and the transcript filter
  remains sticky inside scroll containers.
- `/runners` adds a per-repo filter, improving queue and trace triage on
  multi-repo fleets.
- Daemon route registration was refactored to register handlers per HTTP
  method instead of dispatching inside handler bodies.
- UI documentation screenshots and auth guidance were refreshed for the
  current dashboard.
- Quickstart install and upgrade commands now force a local image rebuild so
  the checked-out release tag is what actually runs.

### Fixed

- Propagated `GITHUB_TOKEN` into AI subprocesses so GitHub MCP tooling keeps
  working after the environment variable rename from `GITHUB_PAT_TOKEN`.
- Normalized daemon SSE event handling to keep live transcript/ring-buffer
  rendering consistent across Claude and Codex backend output parsers.
- Corrected token leaderboard average wording so average total tokens per run
  is not confused with the separate input/output/cache columns.

## [0.1.0] - 2026-05-03

First public release. The daemon is a self-hosted orchestrator that dispatches
AI CLIs (Claude Code, Codex, or any local OpenAI-compatible LLM) to work on
GitHub repos, driven by labels, GitHub events, cron schedules, on-demand HTTP
calls, or runtime inter-agent dispatch. Everything below ships in this release.

### Added

#### Core orchestration

- Agent capability model: each agent is a backend + skills + prompt + optional
  dispatch wiring, decoupled from where it runs. See
  [`docs/configuration.md`](docs/configuration.md).
- Composable skills: reusable prompt fragments referenced by name and
  concatenated into the agent's composed prompt at render time.
- Repos with bindings that wire one agent to exactly one trigger per binding;
  the same agent can have many bindings on the same repo. See
  [`docs/architecture.md`](docs/architecture.md).
- Three trigger kinds: GitHub label events, GitHub event subscriptions, and
  cron schedules. All converge on a single `engine.runAgent` path.
- Reactive inter-agent dispatch: agents return a `dispatch[]` array in their
  JSON response to invoke other agents, gated by `allow_dispatch` (target
  opt-in) and `can_dispatch` (originator whitelist). Safety limits cover
  `max_depth`, `max_fanout`, and a `(target, repo, number)` dedup window.
  See [`docs/dispatch.md`](docs/dispatch.md).
- Per-(agent, repo) memory: the daemon loads existing memory into the prompt
  before every run and persists the agent's returned `memory` field after.
  Toggle uniformly across all trigger surfaces with `allow_memory`.
- `allow_prs` hard guard: when false, the renderer prepends a non-negotiable
  no-PR instruction so reviewer-class agents cannot author PRs regardless of
  their prompt.

#### Backends

- Built-in backends: `claude` (Claude Code) and `codex` (OpenAI Codex CLI),
  with daemon-managed launch flags so the structured JSON / stream-JSON output
  feeds the trace transcript reliably.
- Per-backend `local_model_url`: setting it injects `ANTHROPIC_BASE_URL` into
  the subprocess so the same `claude` binary routes through any
  OpenAI-compatible endpoint. See [`docs/local-models.md`](docs/local-models.md).
- Built-in Anthropic-to-OpenAI translation proxy at `/v1/messages` and
  `/v1/models`, opt-in via `daemon.proxy.enabled`. Translates text, system
  messages, tool-use round-trips, and SSE streaming end-to-end.
- Backend discovery: CLI probing, GitHub MCP health checks, model catalogue
  persistence, orphan detection. Auto-discovery runs once when the backends
  table is empty; manual refresh via `POST /backends/discover`.
- Runtime model guardrail: an agent pinning a model not present in its
  backend's catalogue fails fast with an actionable error.

#### Operator surfaces

- REST API spanning fleet CRUD (`/agents`, `/skills`, `/backends`, `/repos`,
  `/repos/{owner}/{repo}/bindings`, `/guardrails`), operations (`/run`,
  `/status`, `/backends/discover`, `/backends/local`), observability
  (`/events`, `/traces`, `/runners`, `/graph`, `/dispatches`, `/memory`,
  `/config`), and YAML round-trip (`/export`, `/import`). PATCH endpoints
  follow partial-update semantics; bindings PATCH is full-replace by design.
  See [`docs/api.md`](docs/api.md).
- MCP server at `/mcp` over Streamable HTTP, ~45 tools covering the full
  CRUD surface, observability, queue ops, and `trigger_agent`. REST and MCP
  share the same handler instances, so the surfaces cannot drift. See
  [`docs/mcp.md`](docs/mcp.md).
- Embedded Next.js dashboard at `/ui/`: agent / skill / repo / backend /
  guardrail editors, live event firehose, traces with tool-loop transcripts,
  dispatch graph with drag-to-wire editor, memory viewer, runners panel.
  See [`docs/ui.md`](docs/ui.md).
- SQLite store as the single source of truth: `config.yaml` is an optional
  import / export format, not a runtime dependency. CRUD writes are visible
  on the next event read; no in-memory cache to invalidate.

#### Observability

- Durable SQLite event queue: every triggering event is persisted before
  workers are signalled, and rows whose `completed_at` is still `NULL` at
  startup are replayed. Includes an hourly retention sweep (7-day cutoff).
- Persisted traces: every completed run records the gzipped composed prompt
  and the AI CLI's reported token usage (input, output, cache_read,
  cache_write). Lazy fetch via `GET /traces/{span_id}/prompt`.
- Tool-loop transcript on `trace_steps`: ordered tool calls with input /
  output summaries and durations, plus persisted thinking blocks. Codex
  parity with Claude (via `--json`).
- SSE streams: `/events/stream`, `/traces/stream`, `/memory/stream`, plus a
  per-span live stdout tail at `/traces/{span_id}/stream` that replays the
  in-memory ring buffer before live-tailing.
- `/runners` surface: durable event_queue rows JOINed with traces. Completed
  events fanned out to N agents show as N rows; in-flight events show as 1
  row with `agent: null`. Supports per-row delete and event-level retry.
- Dispatch graph and counters at `/graph` and `/dispatches`, including drop
  reasons (depth, fanout, dedup, missing opt-in).

#### Security

- HMAC SHA-256 webhook verification on `/webhooks/github`, with delivery
  dedup keyed by `X-GitHub-Delivery`. The only authentication enforced
  inside the daemon; everything else is the operator's reverse-proxy job.
  See [`docs/security.md`](docs/security.md).
- Default `security` guardrail seeded by migration into the new
  `guardrails` table, prepended to every composed prompt ahead of skills
  and the agent body. Recommends against indirect prompt injection,
  refuses secret reads, blocks arbitrary network egress and out-of-tree
  filesystem access.
- Operator-editable guardrails: add, edit, disable, reorder, or reset
  built-ins to default via REST, MCP, or the dashboard's Guardrails tab.

#### Deployment

- Single Go binary, multi-stage Docker image on `node:22-alpine` so Claude
  Code and Codex live alongside the daemon. Runs as non-root `agents` user.
- Two named volumes: `agents-data` (SQLite database) and `agents-home`
  (Claude / Codex auth and MCP config), populated once via `agents-setup`.
  No host bind-mount of `~/.claude.json`.
- `agents-setup` interactive bash script: picks backends, runs OAuth flows,
  registers the GitHub MCP server on each CLI, refreshes backend discovery,
  and prints diagnostics. See [`docs/quickstart.md`](docs/quickstart.md).
- `--db <path>` flag for the SQLite store location; `--import <file>` for
  one-time YAML seeding. Default boot is against an empty database with
  built-in defaults; no YAML required.

### Notes

- The supported runtime is Docker Compose. Running the binary directly on a
  host is not supported because the daemon dispatches AI CLIs with
  sandbox-bypass flags and the container is what bounds that blast radius.
- All endpoints except `/webhooks/github` are unauthenticated at the daemon
  level. Production deployments must front the daemon with a reverse proxy
  that handles auth on UI / API paths and leaves `/status`, `/webhooks/*`,
  `/run`, and `/v1/*` open. See the Traefik example in `docs/security.md`.
- Concurrent runs share the container filesystem; running one agent per
  container is the workaround until invocation-level isolation ships.
- The fleet is single-developer maintained today; external contributions
  are expected as issues, with the autonomous fleet implementing
  `ai ready`-labelled work. See [`CONTRIBUTING.md`](CONTRIBUTING.md).

[Unreleased]: https://github.com/eloylp/agents/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/eloylp/agents/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/eloylp/agents/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/eloylp/agents/releases/tag/v0.1.0
