# Web dashboard

The daemon ships an embedded web dashboard at `/ui/`. It is the primary interface for managing the fleet. Every CRUD operation (agents, skills, backends, repos, bindings) is available there alongside live monitoring.

<!-- TODO: screenshot — dashboard landing page, full window, light or dark theme depending on whichever is shipped -->

## Pages

### Events

Live webhook firehose with SSE streaming. Watch GitHub events arrive in real time as the daemon processes them.

<!-- TODO: short video (~10s) — the events page receiving a live event from a manually-fired webhook, showing the row appearing at the top with the kind, repo, and timestamp populated -->

### Traces

Agent run traces with timing, status, and a drill-down to the tool-loop transcript. Each trace shows the prompt that was composed, the tool calls the AI made, the tool results, and the final response payload.

<!-- TODO: screenshot — a trace detail view with the tool-loop transcript visible, ideally one with at least 3 tool calls so the reader sees the chain -->

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

### Memory

Raw agent memory markdown per `(agent, repo)` pair. Useful for inspecting what an autonomous agent has learned across runs.

<!-- TODO: screenshot — memory page with a non-trivial memory entry showing structure (sections, list items) -->

### Config

Effective parsed config (secrets redacted). Includes YAML import/export.

<!-- TODO: screenshot — config page with a redacted secret visible in the rendered tree -->

## Authentication

The dashboard is unauthenticated at the daemon level. Place the daemon behind a reverse proxy that gates `/ui/` and the rest of the authenticated surface (everything except `/webhooks/github`, `/status`, `/run`, `/v1/*`). See [docker.md](docker.md) for one concrete pattern using Traefik basic-auth.
