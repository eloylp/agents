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
| `DELETE` | `/{resource}/{name}` | Remove an entry. |
| `GET` | `/export` | Export full fleet config as YAML. |
| `POST` | `/import` | Import a YAML config into the SQLite store. |

Duplicate webhook deliveries are suppressed via `X-GitHub-Delivery` with a TTL cache.

## MCP endpoint

The daemon exposes a [Model Context Protocol](https://modelcontextprotocol.io) server at `/mcp` using the Streamable HTTP transport. MCP-capable clients (Claude Code, Cursor, Cline) register the endpoint and discover the available tools automatically.

Register from Claude Code:

```json
{
  "mcpServers": {
    "agents": {
      "url": "https://agents.example.com/mcp"
    }
  }
}
```

First-cut tools (tracked in [#227](https://github.com/eloylp/agents/issues/227)):

| Tool | Description |
|---|---|
| `list_agents` | List every configured agent. |
| `list_skills` | List every configured skill with its prompt. |
| `list_backends` | List every configured AI backend. |
| `list_repos` | List every repo with its agent bindings. |
| `get_status` | Daemon health snapshot (same payload as `GET /status`). |
| `trigger_agent` | Enqueue an on-demand agent run (same payload as `POST /run`). |

CRUD write tools, observability queries, and config import/export tools are being added in follow-up PRs.

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
