# MCP server

The daemon exposes a [Model Context Protocol](https://modelcontextprotocol.io) server at `/mcp` over the Streamable HTTP transport. MCP-capable clients (Claude Code, Cursor, Cline, ...) register the endpoint once and then discover the available tools automatically. From there you can manage agents, skills, repos, and bindings, trigger runs, and inspect traces conversationally from your editor.

The MCP surface is functionally equivalent to the REST CRUD API documented in [api.md](api.md). The difference is the wire protocol and the consumer: REST is for scripts and dashboards; MCP is for AI clients that can call tools.

## Register the server

From Claude Code:

```bash
claude mcp add -t http -s user agents-fleet https://agents.example.com/mcp \
  -H "Authorization: Basic $(echo -n 'user:password' | base64)"
```

When the daemon runs behind a reverse proxy with basic auth (e.g. Traefik), the `-H` flag passes the credentials so MCP requests authenticate automatically. For unauthenticated local development, drop the `-H` flag.

The same pattern works for Cursor, Cline, and any other MCP-compatible client; consult their docs for the exact config syntax.

## Available tools

### Fleet management

| Tool | Description |
|---|---|
| `list_agents` | List all agents with backend, model, skills, dispatch wiring. |
| `get_agent` | Fetch one agent by name. |
| `create_agent` | Create or update an agent (upsert, full replace). |
| `update_agent` | Partially update an agent by name (only supplied fields are changed). |
| `delete_agent` | Delete an agent. `cascade=true` also removes repo bindings. |
| `list_skills` | List all skills with prompt content. |
| `get_skill` | Fetch one skill by name. |
| `create_skill` | Create or update a skill (full replace). |
| `update_skill` | Partially update a skill by name. |
| `delete_skill` | Delete a skill. |
| `list_backends` | List all AI backends with models and health. |
| `get_backend` | Fetch one backend by name. |
| `create_backend` | Create or update a backend (full replace). |
| `update_backend` | Partially update a backend by name. |
| `delete_backend` | Delete a backend. |
| `list_repos` | List all repos with bindings. |
| `get_repo` | Fetch one repo by name. |
| `create_repo` | Create or update a repo with bindings (full replace of the bindings list). |
| `delete_repo` | Delete a repo and its bindings. |
| `create_binding` | Create one binding on a repo; returns the persisted binding with its generated ID. |
| `get_binding` | Fetch one binding by ID, scoped to a repo. |
| `update_binding` | Replace all fields of a binding by ID. |
| `delete_binding` | Delete a binding by ID. |

### Operations

| Tool | Description |
|---|---|
| `get_status` | Daemon health: uptime, queue depth, schedules, dispatch counters. |
| `trigger_agent` | Fire an on-demand agent run (async, returns event ID). |

### Observability

| Tool | Description |
|---|---|
| `list_events` | Recent events with optional `since` filter. |
| `list_traces` | Recent agent run spans with timing and summary. |
| `get_trace` | Full dispatch chain by root event ID. |
| `get_trace_steps` | Tool-loop transcript for one span. |
| `get_graph` | Agent interaction graph (dispatch edges). |
| `get_dispatches` | Dispatch counters and drop reasons. |
| `get_memory` | Agent memory for an agent/repo pair. |

### Config

| Tool | Description |
|---|---|
| `get_config` | Effective config snapshot (secrets redacted). |
| `export_config` | Fleet config as YAML (round-trippable via `import_config`). |
| `import_config` | Import YAML config. `mode=replace` prunes missing entries. |
