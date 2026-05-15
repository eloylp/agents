# HTTP API reference

This page documents the REST endpoints exposed by the daemon. The MCP (Model Context Protocol) server at `/mcp` has its own reference in [mcp.md](mcp.md).

Sensitive endpoints require daemon auth. Browser clients use the `agents_session` `HttpOnly` cookie; REST and MCP clients send a DB-backed API token with `Authorization: Bearer <token>`. `/`, `/status`, `/webhooks/github`, `/auth/status`, `/auth/login`, `/auth/bootstrap`, and UI static assets remain public where applicable. The local-model proxy accepts unauthenticated loopback calls only from direct daemon-host clients; runner containers are remote peers and need daemon auth if they call the proxy.

## Core endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/status` | Health check: JSON with uptime, event queue depth, agent schedules, dispatch counters |
| `POST` | `/webhooks/github` | GitHub webhook receiver (`X-Hub-Signature-256` HMAC verified) |
| `POST` | `/run` | On-demand agent trigger |

## Auth endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/auth/status` | Public auth state: whether first-user bootstrap is required and whether the caller is authenticated |
| `POST` | `/auth/bootstrap` | Create the first admin user on an empty database and set an `agents_session` cookie |
| `POST` | `/auth/login` | Exchange username/password for an `agents_session` cookie |
| `POST` | `/auth/logout` | Revoke the current browser session and clear the session cookie |
| `GET` | `/auth/me` | Current authenticated user |
| `GET` | `/auth/users` | List dashboard users |
| `POST` | `/auth/users` | Create an additional dashboard user with `{"username":"...","password":"..."}`. Admin-only |
| `DELETE` | `/auth/users/{id}` | Remove a dashboard user. Admin-only; the bootstrapped admin user cannot be removed |
| `GET` | `/auth/tokens` | List the current user's API tokens |
| `POST` | `/auth/tokens` | Create a named API token. The plaintext token is returned once |
| `DELETE` | `/auth/tokens/{id}` | Revoke one of the current user's API tokens |

The `/run` body is `{"agent": "<name>", "repo": "owner/repo"}`. It returns `202 Accepted` immediately with an `event_id`; the agent runs asynchronously.

## Observability endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/agents` | Fleet snapshot: per-agent status, bindings, dispatch wiring |
| `GET` | `/agents/orphans/status` | DB-only orphan report (agents pinning models unavailable in their backend's catalog) |
| `GET` | `/events` | Recent webhook events (time-windowed) |
| `GET` | `/events/stream` | Live event firehose (SSE) |
| `GET` | `/traces` | Recent agent run traces with timing |
| `GET` | `/traces/stream` | Live trace updates (SSE) |
| `GET` | `/traces/{root_event_id}` | All spans for a single root event |
| `GET` | `/traces/{span_id}/steps` | Tool-loop transcript for a completed agent span |
| `GET` | `/traces/{span_id}/prompt` | Composed prompt the daemon sent to the AI CLI for this run. Stored gzipped on the trace row; this endpoint decompresses on the fly and returns `text/plain`. `404` when no prompt was recorded (pre-009-migration spans). Operator-grade, keep behind your auth proxy. |
| `GET` | `/traces/{span_id}/stream` | Server-Sent Events stream of persisted `TraceStep` rows (one JSON object per `data:` line) for a span. The daemon parses AI CLI stdout incrementally through the same backend parser used for `/traces/{span_id}/steps`, writes each step to `trace_steps`, replays existing rows on connect, then live-tails newly committed rows until the run ends or the client disconnects. Emits a final `event: end` SSE message when the run finishes. `404` when the span has no active stream and no persisted steps. |
| `GET` | `/graph` | Workspace-scoped agent graph data: agent nodes plus dispatch edges. The UI overlays repo-scope boundaries and passive repo binding anchors from the agents/repos payloads. |
| `GET` | `/dispatches` | Dispatch dedup store contents + counters |
| `GET` | `/memory/{agent}/{repo}` | Raw agent memory markdown. `{repo}` uses `owner_repo` format (underscore-separated) |
| `GET` | `/memory/stream` | Memory file change notifications (SSE) |
| `GET` | `/config` | Current fleet config snapshot |

## Runners management

The daemon's event queue is durable: every `PushEvent` writes to the SQLite `event_queue` table before signalling workers. Rows whose `completed_at` is `NULL` are replayed on startup; completed rows are pruned after 7 days. The `/runners` surface presents this table as a per-runner view: each event_queue row is JOINed with `observe.traces` so an event that fanned out to N agents shows up as N rows on the wire (one per trace span). Completed events with no matching trace appear as one `skipped` row so operators can distinguish "matched no runnable agent" from missing data. Events still in flight (no spans yet) appear as a single row with `agent: null`.

| Method | Path | Description |
|---|---|---|
| `GET` | `/runners` | Paginated listing, newest first. Each row carries event-level fields (`id`, `event_id`, `kind`, `repo`, `number`, `actor`, `target_agent`, `enqueued_at`, `started_at`, `completed_at`, `payload`) plus per-run fields when a trace exists (`agent`, `span_id`, `run_duration_ms`, `summary`, `prompt_size`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`). The composed prompt body is fetched separately via `GET /traces/{span_id}/prompt`. The `status` field is the unified lifecycle: `enqueued`/`running` (in-flight), `success`/`error` (from the trace), or `skipped` for completed queue rows that produced no trace. Query params: `status` (filter on the event_queue lifecycle, accepts `enqueued`/`running`/`completed`), `limit` (default 100, applies to events not output rows), `offset`. |
| `DELETE` | `/runners/{id}` | Remove one event_queue row. **Best-effort:** if a worker has already received the `QueuedEvent` from the in-memory channel buffer, it will still run; the row simply won't appear in subsequent listings. Affects every fanned-out agent for this event since the action is event-level. Returns `404` for unknown ids. |
| `POST` | `/runners/{id}/retry` | Re-enqueue an event by copying its blob into a fresh `event_queue` row and pushing onto the channel. Re-runs every fanned-out agent for the event (event-level retry). The original row stays as audit history. The response body is `{"new_id": <id>}`. Returns `409` when the source row is in `running` state, `404` when missing, `503` when the in-memory channel is full or closed. |

## Backend diagnostics

| Method | Path | Description |
|---|---|---|
| `GET` | `/backends/status` | Health snapshot for every configured backend (CLI version, model catalog, GitHub MCP probe result) plus supporting tool diagnostics (`tools[]`) for GitHub CLI authentication, git, Go, Rust/Cargo, Node/npm, and TypeScript. `github_cli` is kept as a compatibility alias for the GitHub CLI tool row. |
| `POST` | `/backends/discover` | Trigger an explicit re-probe of every backend's CLI and update the stored model catalog. |
| `POST` | `/backends/local` | Probe one local OpenAI-compatible base URL and return its advertised models without persisting. Useful for dry-running a `local_model_url` setting before saving it. |

## Web dashboard

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | Public login/bootstrap page. Redirects authenticated browser sessions to `/ui/` |
| `GET` | `/ui/` | Built-in web dashboard (static assets; embedded in binary) |

## Proxy endpoints (opt-in)

These are only mounted when `AGENTS_PROXY_ENABLED=true` is set in the daemon environment. Unauthenticated access is limited to loopback clients (`127.0.0.1` / `::1`) so local backend subprocesses can call the proxy. Remote clients must authenticate with the same daemon session/API-token mechanisms as the rest of the sensitive API.

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/messages` | Anthropic-to-OpenAI translation proxy |
| `GET` | `/v1/models` | Companion stub; lists the configured upstream model |

## CRUD endpoints

These routes are always mounted and backed by the SQLite database.

Workspace-local resources accept `?workspace=<id-or-name>` and default to `default` when omitted. This applies to fleet snapshots, agents, repos, bindings, graph layout, runners, events, traces, memory, and workspace-scoped token leaderboard queries. Catalog resources (`prompts`, `skills`, `guardrails`) are reusable assets: each row exposes `workspace_id` and `repo` to express global, workspace-scoped, or repo-scoped visibility.

Prompt catalog rows expose a stable `id` plus a display `name`. Agents persist `prompt_id`; human-facing REST writes may provide `prompt_ref` plus optional `prompt_scope` instead. `prompt_scope` is case-insensitive and accepts `global`, `workspace`, or `workspace/owner/repo`, for example `default/eloylp/agents`.

| Method | Path | Description |
|---|---|---|
| `GET` | `/{resource}` | List all entries for a resource type (`workspaces`, `prompts`, `skills`, `backends`, `repos`, `guardrails`). Note: `GET /agents` is the workspace-filterable fleet snapshot above, not the CRUD list. |
| `GET` | `/{resource}/{name-or-id}` | Fetch one entry. Repos use two path segments: `/repos/{owner}/{repo}`. Catalog routes (`prompts`, `skills`, `guardrails`) use stable IDs; legacy global names are accepted as a compatibility fallback. |
| `POST` | `/{resource}` | Create or replace an entry. Resources: `workspaces`, `prompts`, `agents`, `skills`, `backends`, `repos`, `guardrails`. |
| `PATCH` | `/{resource}/{name-or-id}` | Partial update of an entry. Only fields present in the JSON body are applied; unset fields are preserved. At least one field required. Resources: `workspaces`, `prompts`, `agents`, `skills`, `backends`, `guardrails`. Catalog routes (`prompts`, `skills`, `guardrails`) use stable IDs; legacy global names are accepted as a compatibility fallback. |
| `PATCH` | `/repos/{owner}/{repo}` | Toggle a repo's `enabled` flag. Only `enabled` is patchable; binding edits go through `/repos/{owner}/{repo}/bindings/{id}`, and full repo replacement (including bindings) goes through `POST /repos`. |
| `DELETE` | `/{resource}/{name-or-id}` | Remove an entry. Catalog routes (`prompts`, `skills`, `guardrails`) use stable IDs; legacy global names are accepted as a compatibility fallback. |
| `DELETE` | `/agents/{name}` | Same as the generic delete, plus a `cascade` query param. By default returns `409 Conflict` with the list of repos still binding the agent; pass `?cascade=true` to also drop those bindings in the same transaction. |
| `POST` | `/repos/{owner}/{repo}/bindings` | Create one binding on a repo. Returns the persisted binding with its generated ID. |
| `GET` | `/repos/{owner}/{repo}/bindings/{id}` | Fetch one binding by ID. |
| `PATCH` | `/repos/{owner}/{repo}/bindings/{id}` | Replace all fields of a binding by ID. |
| `DELETE` | `/repos/{owner}/{repo}/bindings/{id}` | Remove a binding by ID. |
| `POST` | `/guardrails/{id}/reset` | Copy a built-in guardrail's `default_content` back into its `content`. Legacy global names are accepted as a compatibility fallback. Returns 400 for operator-added rows (no default to fall back to). |
| `GET` | `/workspaces/{workspace}/guardrails` | List the selected guardrail references for one workspace in render order. |
| `PUT` | `/workspaces/{workspace}/guardrails` | Replace the selected guardrail references for one workspace. |
| `GET` | `/export` | Export full fleet config as workspace-aware YAML, including reusable prompts, skills, guardrails, and workspace-local agents/repos/budgets. |
| `POST` | `/import` | Import workspace-aware YAML into the SQLite store. Legacy top-level agents/repos remain accepted into `default`. |

### Token budgets

Token budget periods use UTC calendar boundaries: `daily` starts at 00:00 UTC,
`weekly` starts Sunday 00:00 UTC, and `monthly` starts on the first day of the
month at 00:00 UTC.

Budget scopes preserve their current enforcement semantics. `global` covers all
workspaces. `workspace` covers one workspace. Simple `repo`, `agent`, and
`backend` scopes are global by name across all workspaces. Use
`workspace+repo`, `workspace+agent`, or `workspace+backend` for workspace
isolation, and `workspace+repo+agent` for one agent/repo pair inside one
workspace. REST wire shapes are unchanged; `PATCH /token_budgets/{id}` is a
partial update and omitted fields are preserved.

### Guardrails

Guardrails are reusable policy catalog entries; workspaces choose which visible catalog entries to render. Catalog wire shape: `{id, workspace_id, repo, name, description, content, default_content, is_builtin, enabled, position}`. Empty `workspace_id` and `repo` means global visibility; `workspace_id` with empty `repo` is workspace-only; both fields set is repo-scoped. Workspace references use `{workspace_id, guardrail_name, position, enabled}`, where `guardrail_name` carries the stable guardrail id after scoped-catalog migration. PATCH covers catalog `description`, `content`, `enabled`, `position` only; `is_builtin` and `default_content` are migration-managed and not editable from the API. The renderer combines mandatory dynamic workspace/repository boundary guidance with the selected workspace references in one guardrails section. See [security.md](security.md) for the threat model and what the default does, and does not, close.

Duplicate webhook deliveries are suppressed via `X-GitHub-Delivery` with a TTL cache.

## Runtime settings

| Method | Path | Description |
|---|---|---|
| `GET` | `/runtime` | Read global runner image and container constraints. |
| `PUT/PATCH` | `/runtime` | Replace global runner image and constraints. Secret values are not accepted here. |
| `PUT/PATCH` | `/workspaces/{workspace}/runtime` | Set or clear the selected workspace's runner image override. |

Runtime settings are also included in `/config`, `/export`, and `/import`. Credentials are daemon environment variables and are never returned by these routes.

## AI runner contract

The contract between the daemon and the AI CLI subprocess inside the ephemeral runner container (prompt composition, structured JSON output, schema enforcement) is documented in [mental-model.md](mental-model.md).
