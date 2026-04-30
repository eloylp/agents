# Web dashboard

The daemon ships an embedded web dashboard at `/ui/`. It is the primary interface for managing the fleet. Every CRUD operation (agents, skills, backends, repos, bindings) is available there alongside live monitoring.

<!-- TODO: screenshot — dashboard landing page, full window, light or dark theme depending on whichever is shipped -->

## Pages

### Events

Live event firehose with SSE streaming. Every kind of event flows through here: webhooks (`issues.*`, `pull_request.*`, `push`, ...), cron ticks (`cron`), on-demand triggers (`agents.run`), and inter-agent dispatches (`agent.dispatch`). Each row carries time, kind, repo, number, actor, an **agents** column with badges for every agent that ran (or is running) for that event — each badge links to the Fleet page — and a **View runners** link that opens the Runners page filtered to the event id with the matching rows pulsing on arrival.

<!-- TODO: short video (~10s) — the events page receiving a live event, showing the agents column populating as the runners complete, then clicking "View runners" to navigate to the filtered runners view -->

### Traces

Agent run traces with timing, status, and a drill-down to the tool-loop transcript. Each span exposes:

- **Token usage** — input / output / cache hit / cache write counts straight from the AI CLI's reported usage. Cache hit ratio shown inline; useful for spotting agents that bust the prompt cache and tuning their composition.
- **Prompt** — the exact composed prompt the daemon sent to the AI CLI on this run. Gzipped on disk, lazy-fetched via `/traces/{span_id}/prompt` when expanded. This is the operator's "what did the agent see" debug surface.
- **Tool-loop transcript** — ordered tool calls with input / output summaries and durations.
- Summary, error message (when the run failed), Gantt position in the dispatch chain.

<!-- TODO: screenshot — a trace detail view with the token usage line, the prompt panel expanded, and at least 3 tool calls visible so the reader sees the chain -->

### Graph

Visual dispatch graph showing which agents invoke which, with edge counts. Toggle "Edit wiring" to add or remove dispatch connections by drag-and-drop. The change writes back to the source agent's `can_dispatch` list and the target's `allow_dispatch` flag.

<!-- TODO: short video (~15s) — graph in edit mode, dragging from one agent node to another to create a dispatch edge, then clicking an existing edge to confirm-and-remove it. This is the marquee interaction; worth a polished gif. -->

### Agents

Fleet snapshot with per-agent status, skills, bindings, dispatch wiring. Create, edit, and delete agents from this page.

<!-- TODO: screenshot — agents page with one agent's edit panel open, showing the skills checklist and the dispatch toggles populated -->

### Skills

Manage the reusable guidance blocks composed into agent prompts. Create, edit, delete.

<!-- TODO: screenshot — skills editor with one skill open and another visible in the side list -->

### Backends and tools

Backend discovery status, including per-backend GitHub MCP connectivity. Manage runtime limits (timeout, max prompt chars), local-backend URLs, and orphaned-model remediation.

<!-- TODO: screenshot — backends page showing a healthy claude entry alongside a backend with a discovery error or warning, so the reader sees both states at once -->

### Repos

Repository bindings. Wire agents to repos with labels, events, or cron triggers. Each binding has its own enable / disable toggle.

<!-- TODO: screenshot — a repo with two bindings, one event-triggered and one cron-triggered, so the reader sees the trigger types side by side -->

### Runners

Operator view of the work that's running and recently ran. Each row is one runner — a unit of work the daemon picked up. Mental model:

1. **Event arrives** → visible on the Events page (firehose).
2. **Runners working** → visible here (this page). One row per (event, agent) once traces have been recorded; one row per event with no agent badge while in-flight.
3. **Execution detail** → visible on the Traces page (tool-loop transcript).

The page combines two sources: the durable `event_queue` table for in-flight lifecycle (so freshly queued and currently fanning-out events stay visible), plus per-agent trace spans for completed runs. Once the event_queue row is pruned (>7 days), the trace alone is the source of truth.

Each row carries: event id, queue lifecycle status (`enqueued` / `running`) or trace status (`success` / `error`), agent badge (links to the Fleet page), repo, kind, started at, duration. Click any row to expand: actor, payload, and a **View trace detail** link to `/ui/traces/<event_id>`.

Two per-row actions, both **event-level** (a single event_queue row drives multiple displayed rows after fan-out, so the actions affect every fanned-out agent for that event — the confirm dialog says so explicitly):

- **Retry** copies the original event blob into a fresh `event_queue` row with a new `enqueued_at` and pushes it onto the channel — the source row stays as audit history. Disabled while the source is in `enqueued` or `running` state.
- **Delete** removes the event_queue row from the table. Best-effort: a worker that has already dequeued the `QueuedEvent` from the channel buffer will still run it; the row simply won't appear in subsequent listings.

Arriving with `?event=<id>` (e.g. via the **View runners** link on the Events page) filters to the runners for that event and pulses the matching rows for ~3s so the operator can spot them at a glance.

<!-- TODO: screenshot — runners page with a fanned-out event (multiple rows, same event id, different agents) and an in-flight row visible at the top -->

### Memory

Raw agent memory markdown per `(agent, repo)` pair. Useful for inspecting what an autonomous agent has learned across runs.

<!-- TODO: screenshot — memory page with a non-trivial memory entry showing structure (sections, list items) -->

### Config

Effective parsed config (secrets redacted). Includes YAML import/export.

<!-- TODO: screenshot — config page with a redacted secret visible in the rendered tree -->

## Authentication

The dashboard is unauthenticated at the daemon level. Place the daemon behind a reverse proxy that gates `/ui/`, `/runners`, and the rest of the authenticated surface (everything except `/webhooks/github`, `/status`, `/run`, `/v1/*`). See [docker.md](docker.md) for one concrete pattern using Traefik basic-auth.
