# HTTP API reference

This page documents the REST endpoints exposed by the daemon. The MCP (Model Context Protocol) server at `/mcp` has its own reference in [mcp.md](mcp.md).

All endpoints are unauthenticated at the daemon level; access control is the reverse proxy's responsibility.

## Core endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/status` | Health check: JSON with uptime, event queue depth, agent schedules, dispatch counters |
| `POST` | `/webhooks/github` | GitHub webhook receiver (`X-Hub-Signature-256` HMAC verified) |
| `POST` | `/run` | On-demand agent trigger |

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
| `GET` | `/traces/{span_id}/prompt` | Composed prompt the daemon sent to the AI CLI for this run. Stored gzipped on the trace row; this endpoint decompresses on the fly and returns `text/plain`. `404` when no prompt was recorded (pre-009-migration spans). Operator-grade — keep behind your auth proxy. |
| `GET` | `/graph` | Agent interaction graph (dispatch edges) |
| `GET` | `/dispatches` | Dispatch dedup store contents + counters |
| `GET` | `/memory/{agent}/{repo}` | Raw agent memory markdown. `{repo}` uses `owner_repo` format (underscore-separated) |
| `GET` | `/memory/stream` | Memory file change notifications (SSE) |
| `GET` | `/config` | Effective parsed config (secrets redacted) |

## Runners management

The daemon's event queue is durable: every `PushEvent` writes to the SQLite `event_queue` table before signalling workers. Rows whose `completed_at` is `NULL` are replayed on startup; completed rows are pruned after 7 days. The `/runners` surface presents this table as a per-runner view: each event_queue row is JOINed with `observe.traces` so an event that fanned out to N agents shows up as N rows on the wire (one per trace span). Events still in flight (no spans yet) appear as a single row with `agent: null`.

| Method | Path | Description |
|---|---|---|
| `GET` | `/runners` | Paginated listing, newest first. Each row carries event-level fields (`id`, `event_id`, `kind`, `repo`, `number`, `actor`, `target_agent`, `enqueued_at`, `started_at`, `completed_at`, `payload`) plus per-run fields when a trace exists (`agent`, `span_id`, `run_duration_ms`, `summary`, `prompt_size`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`). The composed prompt body is fetched separately via `GET /traces/{span_id}/prompt`. The `status` field is the unified lifecycle: `enqueued`/`running` (in-flight) or `success`/`error` (from the trace). Query params: `status` (filter on the event_queue lifecycle, accepts `enqueued`/`running`/`completed`), `limit` (default 100, applies to events not output rows), `offset`. Completed events with no traces (webhook with no matching binding) are skipped — listing them would be misleading. |
| `DELETE` | `/runners/{id}` | Remove one event_queue row. **Best-effort:** if a worker has already received the `QueuedEvent` from the in-memory channel buffer, it will still run; the row simply won't appear in subsequent listings. Affects every fanned-out agent for this event since the action is event-level. Returns `404` for unknown ids. |
| `POST` | `/runners/{id}/retry` | Re-enqueue an event by copying its blob into a fresh `event_queue` row and pushing onto the channel. Re-runs every fanned-out agent for the event (event-level retry). The original row stays as audit history. The response body is `{"new_id": <id>}`. Returns `409` when the source row is in `running` state, `404` when missing, `503` when the in-memory channel is full or closed. |

## Backend diagnostics

| Method | Path | Description |
|---|---|---|
| `GET` | `/backends/status` | Health snapshot for every configured backend (CLI version, model catalog, GitHub MCP probe result). |
| `POST` | `/backends/discover` | Trigger an explicit re-probe of every backend's CLI and update the stored model catalog. |
| `POST` | `/backends/local` | Probe one local OpenAI-compatible base URL and return its advertised models without persisting. Useful for dry-running a `local_model_url` setting before saving it. |

## Web dashboard

| Method | Path | Description |
|---|---|---|
| `GET` | `/ui/` | Built-in web dashboard (static assets; embedded in binary) |

## Proxy endpoints (opt-in)

These are only mounted when `daemon.proxy.enabled: true` is set in the config.

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/messages` | Anthropic-to-OpenAI translation proxy |
| `GET` | `/v1/models` | Companion stub; lists the configured upstream model |

## CRUD endpoints

These routes are always mounted and backed by the SQLite database.

| Method | Path | Description |
|---|---|---|
| `GET` | `/{resource}` | List all entries for a resource type (`skills`, `backends`, `repos`). Note: `GET /agents` is the fleet snapshot above, not the CRUD list. |
| `GET` | `/{resource}/{name}` | Fetch one entry. Repos use two path segments: `/repos/{owner}/{repo}`. |
| `POST` | `/{resource}` | Create or replace an entry. Resources: `agents`, `skills`, `backends`, `repos`. |
| `PATCH` | `/{resource}/{name}` | Partial update of an entry. Only fields present in the JSON body are applied; unset fields are preserved. At least one field required. Resources: `agents`, `skills`, `backends`, `repos`. |
| `DELETE` | `/{resource}/{name}` | Remove an entry. |
| `POST` | `/repos/{owner}/{repo}/bindings` | Create one binding on a repo. Returns the persisted binding with its generated ID. |
| `GET` | `/repos/{owner}/{repo}/bindings/{id}` | Fetch one binding by ID. |
| `PATCH` | `/repos/{owner}/{repo}/bindings/{id}` | Replace all fields of a binding by ID. |
| `DELETE` | `/repos/{owner}/{repo}/bindings/{id}` | Remove a binding by ID. |
| `GET` | `/export` | Export full fleet config as YAML. |
| `POST` | `/import` | Import a YAML config into the SQLite store. |

Duplicate webhook deliveries are suppressed via `X-GitHub-Delivery` with a TTL cache.

## AI runner contract

The contract between the daemon and the AI CLI subprocess (prompt composition, structured JSON output, schema enforcement) is documented in [mental-model.md](mental-model.md).
