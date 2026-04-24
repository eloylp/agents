# HTTP API reference

All endpoints are unauthenticated at the daemon level -- access control is the reverse proxy's responsibility.

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
| `GET` | `/events` | Recent webhook events (time-windowed) |
| `GET` | `/events/stream` | Live event firehose (SSE) |
| `GET` | `/traces` | Recent agent run traces with timing |
| `GET` | `/traces/stream` | Live trace updates (SSE) |
| `GET` | `/traces/{root_event_id}` | All spans for a single root event |
| `GET` | `/traces/{span_id}/steps` | Tool-loop transcript for a completed agent span |
| `GET` | `/graph` | Agent interaction graph (dispatch edges) |
| `GET` | `/dispatches` | Dispatch dedup store contents + counters |
| `GET` | `/memory/{agent}/{repo}` | Raw agent memory markdown. `{repo}` uses `owner_repo` format (underscore-separated) |
| `GET` | `/memory/stream` | Memory file change notifications (SSE) |
| `GET` | `/config` | Effective parsed config (secrets redacted) |

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

## MCP endpoint

The daemon exposes a [Model Context Protocol](https://modelcontextprotocol.io) server at `/mcp` using the Streamable HTTP transport. MCP-capable clients (Claude Code, Cursor, Cline) register the endpoint and discover the available tools automatically.

Register from Claude Code:

```bash
claude mcp add -t http -s user agents-fleet https://agents.example.com/mcp \
  -H "Authorization: Basic $(echo -n 'user:password' | base64)"
```

When the daemon runs behind a reverse proxy with basic auth (e.g. Traefik), the `-H` flag passes the credentials so MCP requests authenticate automatically.

### Available tools

**Fleet management:**

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

**Operations:**

| Tool | Description |
|---|---|
| `get_status` | Daemon health: uptime, queue depth, schedules, dispatch counters. |
| `trigger_agent` | Fire an on-demand agent run (async, returns event ID). |

**Observability:**

| Tool | Description |
|---|---|
| `list_events` | Recent events with optional `since` filter. |
| `list_traces` | Recent agent run spans with timing and summary. |
| `get_trace` | Full dispatch chain by root event ID. |
| `get_trace_steps` | Tool-loop transcript for one span. |
| `get_graph` | Agent interaction graph (dispatch edges). |
| `get_dispatches` | Dispatch counters and drop reasons. |
| `get_memory` | Agent memory for an agent/repo pair. |

**Config:**

| Tool | Description |
|---|---|
| `get_config` | Effective config snapshot (secrets redacted). |
| `export_config` | Fleet config as YAML (round-trippable via `import_config`). |
| `import_config` | Import YAML config. `mode=replace` prunes missing entries. |

## AI runner contract

The daemon spawns the configured CLI, sends the composed prompt on **stdin**, and expects a **single JSON object on stdout**:

```json
{
  "summary": "Reviewed PR for security vulnerabilities",
  "artifacts": [
    {
      "type": "pr_review",
      "part_key": "review/claude/security",
      "github_id": "123456",
      "url": "https://github.com/owner/repo/pull/1#pullrequestreview-123456"
    }
  ],
  "dispatch": [
    {
      "agent": "sec-reviewer",
      "number": 42,
      "reason": "Custom crypto primitives found -- needs deeper security review"
    }
  ],
  "memory": "## 2026-04-21\n- Reviewed PR #42 -- escalated crypto concerns to sec-reviewer."
}
```

The metadata is used for observability, logging, and run summaries. Agents that don't post anything still return an empty `artifacts: []`. The `dispatch` field is optional -- omit it or leave it empty when the agent does not need to invoke another agent. See [dispatch.md](dispatch.md) for the full contract.

The `memory` field is how autonomous agents persist state across scheduled runs. The daemon reads the stored memory before each autonomous run and writes the `memory` value from the response back to the SQLite store. An empty string clears the memory. Event-driven runs (webhooks, label triggers) do not receive or persist memory.
