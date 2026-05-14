# Web dashboard

The daemon ships an embedded web dashboard at `/ui/`. The public root path `/` serves the login/bootstrap page and redirects authenticated browser sessions into `/ui/graph/`, the graph-first workflow designer. The dashboard is the primary interface for managing the agent fleet. Every CRUD operation (agents, prompts, skills, backends, repos, guardrails, bindings) is available there alongside live monitoring.

![Fleet dashboard](img/fleet.png)

## Pages

### Events

Live event firehose with SSE streaming. Every kind of event flows through here: webhooks (`issues.*`, `pull_request.*`, `push`, ...), cron ticks (`cron`), on-demand triggers (`agents.run`), and inter-agent dispatches (`agent.dispatch`). Each row carries time, kind, repo, number, actor, an **agents** column with badges for every agent that ran (or is running) for that event, each badge links to the Fleet page, and a **View runners** link that opens the Runners page filtered to the event id with the matching rows pulsing on arrival.

![Events firehose](img/events.png)

### Traces

Agent run traces with timing, status, and a drill-down to the tool-loop transcript. Each span exposes:

- **Token usage**, input / output / cache hit / cache write counts straight from the AI CLI's reported usage. Cache hit ratio shown inline; useful for spotting agents that bust the prompt cache and tuning their composition.
- **Prompt**, the exact composed prompt the daemon sent to the AI CLI on this run. Gzipped on disk, lazy-fetched via `/traces/{span_id}/prompt` when expanded. This is the operator's "what did the agent see" debug surface.
- **Tool-loop transcript**, ordered tool calls with input / output summaries and durations.
- Summary, error message (when the run failed), Gantt position in the dispatch chain.

![Trace detail with token usage and prompt panel expanded](img/traces.png)

### Graph

Primary workflow designer showing agents as draggable graph nodes and dispatch permissions as edges. Node identity is keyed by the agent's stable database ID, so saved positions survive mutable agent names. Manual layout is persisted per workspace; **Reset layout** clears saved positions and returns to the automatic graph layout.

The graph deliberately keeps one node per real agent. Repo-scoped agents are grouped inside thin dashed repo boundaries; workspace-scoped agents sit outside those boundaries. Repo trigger bindings are visualised as thin lines from the agent to passive repo anchors, so the user can see where an agent runs without duplicating the agent into one node per repo. Dispatch wiring remains the editable agent-to-agent relationship.

The right-side **Agent editor** is the graph's main editing surface. Click an agent to inspect or edit its definition, run it against bound repos, manage repo triggers, add or remove dispatch wiring, and review recent runner rows / trace links. The editor is tabbed by concept: Overview, Settings, Triggers, Dispatch, and Activity. Operators can create agents without leaving the designer.

Dispatch wiring is always editable: drag from one agent to another to add a connection, or click an edge to inspect its Overview, History, and Danger tabs. Dispatch changes write back to the source agent's `can_dispatch` list and the target's `allow_dispatch` flag.

![Agent interaction graph](img/graph.png)

![Graph edit mode, dragging between nodes to wire a dispatch edge](img/graph-edit.gif)

### Agents

Fleet snapshot with per-agent status, skills, bindings, dispatch wiring. Create, edit, and delete agents from this page. Long-form fields like the agent prompt show an **⛶ Expand** affordance that pops the editor into a fullscreen modal, same `value` flows back into the form on close.

<!-- The Fleet page (above) is the agents page: same surface, same capture. -->
![Fleet / Agents page](img/fleet.png)

### Prompts and Skills

Manage reusable prompt contracts and reusable guidance blocks composed into agent prompts. Both catalogs can be global, workspace-scoped, or repo-scoped. The list pages show all catalog items by default and provide local workspace/repo filters where that helps selection. Create, edit, delete. Long-form content editors have the same **⛶ Expand** affordance as the agent prompt, useful when a prompt or skill grows past a screenful.

![Skills editor](img/skills.png)

### Backends and tools

Backend discovery status, including per-backend GitHub MCP connectivity, plus supporting tool health for `gh`, `git`, Go, Rust/Cargo, Node/npm, and TypeScript. The Tools column shows whether GitHub CLI is authenticated, because agents prefer MCP but may fall back to `gh` for complex local checkout/test/PR loops. Manage runtime limits (timeout, max prompt chars), local-backend URLs, and orphaned-model remediation. Lives as a tab inside the Config page.

![Config page, Backends and tools tab is one of three tabs at the top](img/config.png)

### Guardrails

Tab inside the Config page. Lists every prompt guardrail that can be selected for a workspace, with built-in / disabled / position badges. Click a row to edit name, description, content (markdown editor with **⛶ Expand** affordance), enabled toggle, and position. Guardrails can be global, workspace-scoped, or repo-scoped; each workspace chooses the visible guardrails it renders. **Reset to default** restores a built-in's seeded text. **Delete** asks for double confirmation, with a stronger warning when the row is built-in. Disabling the shipped `security` guardrail surfaces an extra-stern confirm modal explaining what protection is removed. The shipped daemon arrives with built-in guardrails for security, public-action discretion, daemon-only memory scope, and repository tool usage (MCP first, gh fallback); operators can add code-style, deployment-policy, or any other policy block on top.

### Repos

Repository bindings. Wire agents to repos with labels, events, or cron triggers. Each binding has its own enable / disable toggle.

![Repos with mixed event-triggered and cron-triggered bindings](img/repos.png)

### Runners

Operator view of the work that's running and recently ran. Each row is one runner, a unit of work the daemon picked up. Mental model:

1. **Event arrives** → visible on the Events page (firehose).
2. **Runners working** → visible here (this page). One row per (event, agent) once traces have been recorded; one row per event with no agent badge while in-flight.
3. **Execution detail** → visible on the Traces page (tool-loop transcript).

The page combines two sources: the durable `event_queue` table for in-flight lifecycle (so freshly queued and currently fanning-out events stay visible), plus per-agent trace spans for completed runs. Once the event_queue row is pruned (>7 days), the trace alone is the source of truth.

Each row carries: event id, queue lifecycle status (`enqueued` / `running`) or trace status (`success` / `error`), agent badge (links to the Fleet page), repo, kind, started at, duration. Click any row to expand: actor, payload, and a **View trace detail** link to `/ui/traces/<event_id>`.

Two per-row actions, both **event-level** (a single event_queue row drives multiple displayed rows after fan-out, so the actions affect every fanned-out agent for that event, the confirm dialog says so explicitly):

- **Retry** copies the original event blob into a fresh `event_queue` row with a new `enqueued_at` and pushes it onto the channel, the source row stays as audit history. Disabled while the source is in `enqueued` or `running` state.
- **Delete** removes the event_queue row from the table. Best-effort: a worker that has already dequeued the `QueuedEvent` from the channel buffer will still run it; the row simply won't appear in subsequent listings.

Arriving with `?event=<id>` (e.g. via the **View runners** link on the Events page) filters to the runners for that event and pulses the matching rows for ~3s so the operator can spot them at a glance.

**Live stream.** Rows in the `running` state with a known `span_id` show a `▶ Live` button. Clicking it opens a modal that subscribes to `/traces/{span_id}/stream` and renders persisted `TraceStep` rows as `🔧 tool call` cards, `💬 thinking` text, and `📤 tool result` payloads. Each card collapses to a one-line preview by default and expands to the full detail. Arriving mid-run replays the rows already committed to `trace_steps` before live-tailing newly committed rows. When the run ends, the modal shows a "✓ Run completed" footer with a link to the trace detail. The DB is the transcript source of truth during and after the run; the in-memory channel is only a notification path. The view auto-follows new entries as they arrive; if you scroll up to read older output, a **↓ Latest** pill appears that re-sticks scroll to the bottom on click.

![Runners page, event #144 fanned out to coder and pr-reviewer, both in flight with the ▶ Live button visible](img/runners.png)

### Memory

Raw agent memory markdown per `(workspace, agent, repo)` key. Useful for inspecting what an autonomous agent has learned across runs.

![Agent memory entry](img/memory.png)

### Config

Current fleet config snapshot. Includes YAML import/export for shareable fleet strategy. The Runtime tab controls the global runner image, basic Docker constraints, and the selected workspace's optional runner image override. Secret values are not shown in the dashboard; Claude, Codex, GitHub MCP, and `gh` credentials come from daemon environment variables and are injected into each ephemeral runner container.

![Config inspector, webhook_secret rendered as `[redacted]`](img/config.png)

## Authentication

The root login page supports first-user setup and username/password login before redirecting authenticated browsers to `/ui/`. Inside the dashboard, Config -> Authentication supports logout, admin-only user creation/removal, and named API token management. The first bootstrapped user is the admin user and cannot be removed. Browser sessions use an opaque DB-backed token in an `HttpOnly` cookie. MCP and REST clients use API tokens created in the dashboard and sent as `Authorization: Bearer <token>`.

The authenticated dashboard uses a left-side navigation shell on desktop and a hamburger drawer on small screens. The shell owns global alerts for orphaned agents and token-budget thresholds; individual pages keep their own operational state.

Use your reverse proxy for TLS/routing, not as the primary API auth layer. See [security.md → Daemon auth](security.md#daemon-auth) and [Reverse-proxy routing](security.md#reverse-proxy-routing).

## Regenerating these screenshots

The images in `docs/img/` are generated from a synthetic fixture so the
content stays neutral and reproducible. Regenerate after a UI change:

```bash
# Terminal 1, boot the seeded daemon on :8081
go run ./cmd/screenshotseed

# Terminal 2, drive headless Chromium (Playwright) + ffmpeg → docs/img/
cd internal/ui
node scripts/screenshots.mjs
```

`cmd/screenshotseed` builds a tempdir SQLite, imports a fictional fleet
(`acme/widgets`, `acme/control-plane`), seeds events / traces / dispatch
history, registers an in-flight `pr-reviewer` run on event #144, and
swaps the AI runner for a stub that blocks forever, so the runners
page shows live rows for the screenshot rather than completed-but-failed
ones (the screenshotting host has no real `claude` / `codex` binary).
First-time setup needs `npm install --save-dev playwright` in
`internal/ui` and `npx playwright install chromium`. The graph edit
GIF additionally needs `ffmpeg`.
