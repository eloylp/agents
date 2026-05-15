# MCP server

The daemon exposes a [Model Context Protocol](https://modelcontextprotocol.io) server at `/mcp` over the Streamable HTTP transport. MCP-capable clients (Claude Code, Cursor, Cline, ...) register the endpoint once and then discover the available tools automatically. From there you can manage workspaces, agents, prompts, skills, guardrails, repos, and bindings, trigger runs, and inspect traces conversationally from your editor.

The MCP surface mirrors the fleet-management REST API documented in [api.md](api.md). The difference is the wire protocol and the consumer: REST is for scripts and dashboards; MCP is for AI clients that can call tools. Dashboard user administration stays in the UI/REST auth surface so passwords are not sent through MCP clients.

![Claude Code session asking "show me all agents and their status" and rendering a table from list_agents](img/mcp-terminal.png)

## Register the server

From Claude Code:

```bash
export AGENTS_MCP_TOKEN='agents_...'
claude mcp add -t http -s user agents-fleet https://agents.example.com/mcp \
  -H "Authorization: Bearer $AGENTS_MCP_TOKEN"
```

From Codex:

```bash
export AGENTS_MCP_TOKEN='agents_...'
codex mcp add agents-fleet --url https://agents.example.com/mcp --bearer-token-env-var AGENTS_MCP_TOKEN
```

Create `AGENTS_MCP_TOKEN` from the dashboard at Config -> Authentication. Plaintext tokens are shown only once and can be revoked from the same page.

The same pattern works for Cursor, Cline, and any other MCP-compatible client; consult their docs for the exact config syntax.

## Available tools

Most fleet tools accept `workspace` for workspace-local resources and default to `Default` when omitted. Prompt catalog tools expose stable prompt ids, and agent tools accept either `prompt_id` or the human selector `prompt_ref` plus optional `prompt_scope`. `prompt_scope` is case-insensitive and accepts `global`, `workspace`, or `workspace/owner/repo`, for example `default/eloylp/agents`.

### Fleet management

| Tool | Description |
|---|---|
| `list_agents` | List all agents with backend, model, skills, dispatch wiring. |
| `get_agent` | Fetch one agent by name. |
| `create_agent` | Create or update an agent (upsert, full replace). |
| `update_agent` | Partially update an agent by name (only supplied fields are changed). |
| `delete_agent` | Delete an agent. `cascade=true` also removes repo bindings. |
| `list_skills` | List all skill catalog entries with prompt content, including global, workspace-scoped, and repo-scoped skills. |
| `get_skill` | Fetch one skill by stable id; legacy global display-name lookup is accepted as a fallback. |
| `create_skill` | Create or update a skill catalog entry. |
| `update_skill` | Partially update a skill by stable id; legacy global display-name lookup is accepted as a fallback. |
| `delete_skill` | Delete a skill by stable id; legacy global display-name lookup is accepted as a fallback. |
| `list_prompts` | List all prompt catalog entries, including global, workspace-scoped, and repo-scoped prompts. |
| `get_prompt` | Fetch one prompt by stable id, or by `name` plus optional `workspace_id` / `repo` when unambiguous. |
| `create_prompt` | Create or update a prompt catalog entry. |
| `update_prompt` | Partially update a prompt by stable id, or by `name` plus optional `workspace_id` / `repo` when unambiguous. |
| `delete_prompt` | Delete a prompt by stable id, or by `name` plus optional `workspace_id` / `repo` when unambiguous. |
| `list_workspaces` | List all workspaces. |
| `get_workspace` | Fetch one workspace by id or display name. |
| `create_workspace` | Create or update a workspace. |
| `update_workspace` | Partially update a workspace. |
| `delete_workspace` | Delete an unused non-default workspace. |
| `list_workspace_guardrails` | List selected guardrail references for one workspace. |
| `update_workspace_guardrails` | Replace selected guardrail references for one workspace. |
| `list_backends` | List all AI backends with models and health. |
| `get_backend` | Fetch one backend by name. |
| `create_backend` | Create or update a backend (full replace). |
| `update_backend` | Partially update a backend by name. |
| `delete_backend` | Delete a backend. |
| `list_repos` | List all repos with bindings. |
| `get_repo` | Fetch one repo by name. |
| `create_repo` | Create or update a repo with bindings (full replace of the bindings list). |
| `update_repo` | Toggle a repo's `enabled` flag without churning binding IDs. Only `enabled` is patchable; binding edits go through the binding tools below. |
| `delete_repo` | Delete a repo and its bindings. |
| `create_binding` | Create one binding on a repo; returns the persisted binding with its generated ID. |
| `get_binding` | Fetch one binding by ID, scoped to a repo. |
| `update_binding` | Replace all fields of a binding by ID. |
| `delete_binding` | Delete a binding by ID. |
| `list_guardrails` | List every guardrail catalog entry, including built-ins and scoped operator-added guardrails. |
| `get_guardrail` | Fetch one guardrail by stable id; legacy global display-name lookup is accepted as a fallback. |
| `create_guardrail` | Create or update an operator-defined guardrail. Built-in flags (`is_builtin`, `default_content`) are migration-managed and ignored on the wire. |
| `update_guardrail` | Partially update a guardrail by stable id. Patchable: `description`, `content`, `enabled`, `position`. |
| `delete_guardrail` | Delete a guardrail by stable id. Built-ins can be deleted from the MCP path; the dashboard double-confirms in the UI. |
| `reset_guardrail` | Copy a built-in guardrail's `default_content` back into its `content` by stable id. Returns a validation error on operator-added rows. |

### Operations

| Tool | Description |
|---|---|
| `get_status` | Daemon health: uptime, queue depth, schedules, dispatch counters. |
| `trigger_agent` | Fire an on-demand agent run (async, returns event ID). |

### Observability

| Tool | Description |
|---|---|
| `list_events` | Recent events with optional `since` filter. |
| `list_traces` | Recent agent run spans with timing, summary, and token usage (`input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `prompt_size`). The composed prompt body is fetched separately via the `/traces/{span_id}/prompt` REST endpoint, not an MCP tool, since it can be many KB. |
| `get_trace` | Full dispatch chain by root event ID. |
| `get_trace_steps` | Tool-loop transcript for one span. |
| `get_trace_prompt` | Composed prompt the daemon sent to the AI CLI for one span (gzipped on disk; decompressed on the fly). The "what did the agent see" debug artefact. Errors when no prompt is recorded (pre-009-migration spans). |

The live stdout stream (`GET /traces/{span_id}/stream`) is intentionally not mirrored as an MCP tool, SSE is a long-lived streaming protocol that doesn't fit MCP's request/response contract. MCP clients that need the post-completion transcript use `get_trace_steps` instead.
| `get_graph` | Workspace-scoped agent interaction graph with dispatch edges. |
| `get_dispatches` | Dispatch counters and drop reasons. |
| `get_memory` | Agent memory for an agent/repo pair. |

### Runners

These tools mirror the `/runners` REST surface; see [api.md](api.md#runners-management) for the wire shape, JOIN-with-traces semantics, and retry behaviour.

| Tool | Description |
|---|---|
| `list_runners` | One row per (event, agent) once traces have been recorded, one `skipped` row for completed events that produced no trace, and one row per event with `agent: null` while in-flight. Carries event metadata (kind, repo, number, actor, target_agent, payload, timestamps) plus trace fields (agent, span_id, run_duration_ms, summary, prompt_size, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens) when present. Optional `status` filter on the event_queue lifecycle (`enqueued`/`running`/`completed`); `limit` (default 100); `offset`. |
| `delete_runner` | Remove an event_queue row by id. Best-effort, if a worker has already dequeued the QueuedEvent from the channel buffer it will still run. Event-level: affects every fanned-out agent for this event. |
| `retry_runner` | Re-enqueue an event by copying its blob into a fresh row and pushing onto the channel. Re-runs every fanned-out agent (event-level retry). The original row stays as audit history. Errors when the source is in `running` state, when the channel is full, or when the queue has been closed. |

### Config

| Tool | Description |
|---|---|
| `get_config` | Current fleet config snapshot. |
| `get_runtime` | Read global runner image and container constraints. |
| `update_runtime` | Patch global runner image and basic constraints, preserving omitted settings. Secret values are not accepted. |
| `update_workspace_runtime` | Set or clear one workspace's runner image override. |
| `export_config` | Fleet config as YAML (round-trippable via `import_config`). |
| `import_config` | Import YAML config. `mode=replace` prunes missing entries. |

### Token budgets

Token budget tools keep the REST enforcement model unchanged. Periods are UTC
calendar windows: `daily` starts at 00:00 UTC, `weekly` starts Sunday 00:00 UTC,
and `monthly` starts on the first day of the month at 00:00 UTC.

Scope kinds are explicit about global versus workspace-isolated coverage:
`global` covers all workspaces, `workspace` covers one workspace, and simple
`repo`, `agent`, and `backend` scopes apply globally across workspaces by name.
For workspace isolation, use `workspace+repo`, `workspace+agent`, or
`workspace+backend`; use `workspace+repo+agent` for one agent/repo pair inside
one workspace.
