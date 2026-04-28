# Quick start

The fastest path from zero to a daemon dispatching agents on a GitHub repo.

## Requirements

| Dependency | Purpose |
|---|---|
| Go 1.25+ | Build the daemon |
| Claude Code or Codex | The actual AI backend that does the work |
| [GitHub MCP server](https://github.com/github/github-mcp-server) | Authenticated GitHub access for the AI CLIs (the only path the daemon and its agents use to reach GitHub) |

## Install the AI CLIs

Follow the official setup guides:

- [Claude Code](https://code.claude.com/docs/en/setup) + [GitHub MCP for Claude](https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-claude.md)
- [Codex](https://github.com/openai/codex) + [GitHub MCP for Codex](https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-codex.md)

You only need one of these to get started. The GitHub MCP server is required by both.

## Build

```bash
go build -o agents ./cmd/agents
```

## First-time setup (recommended)

Run the interactive setup assistant:

```bash
./agents setup
```

It walks you through wiring up the daemon and validates readiness end-to-end (`/status`, `/backends/status`, `/backends/discover`, `/agents/orphans/status`) before finishing, so you know your CLIs and tokens are correct before the first scheduled run.

## Manual setup

If you'd rather do it by hand, the daemon always boots from a SQLite database. `config.yaml` is an optional way to seed it; you can also start with an empty database and create the fleet through the dashboard at `/ui/`.

```bash
# Start with an empty database; create the fleet through /ui/ or the CRUD API
./agents --db agents.db

# Or seed the database from YAML at first start
./agents --db agents.db --import config.yaml
```

After the first start, `config.yaml` is no longer read; the daemon boots from the persisted database. You can export the current fleet back to YAML at any time via `GET /export`.

## On-demand single agent run

Useful for testing without waiting for a cron tick or webhook. Hit the running daemon's `/run` endpoint:

```bash
curl -X POST https://<your-host>/run \
  -H "Content-Type: application/json" \
  -d '{"agent":"coder","repo":"owner/repo"}'
```

Or use the MCP `trigger_agent` tool from any registered MCP client. There is no separate CLI mode — running a second `agents` process out-of-band would not share the daemon's run-lock or dispatch dedup state.

## GitHub webhook setup

To receive events (label triggers, issue creation, PR opens, pushes), wire a webhook on each repo:

1. Open **Settings → Webhooks → Add webhook** on the repo.
2. **Payload URL**: `https://<your-host>/webhooks/github`
3. **Content type**: `application/json`
4. **Secret**: same value as `GITHUB_WEBHOOK_SECRET`.
5. **Events**: enable any of Issues, Pull requests, Issue comments, Pull request reviews, Pull request review comments, Pushes. Pick whichever ones you want to trigger agents on. Unused events are silently dropped.
6. **Active**: checked.

See [events.md](events.md) for the full list of supported event kinds and their filtering rules.

## Next steps

- [Mental model](mental-model.md) for how the daemon composes prompts and what an agent must return. Read this before writing your first prompt.
- [Configuration](configuration.md) for the full config schema (skills, agents, repos, backends).
- [Docker deployment](docker.md) for production setup behind a reverse proxy.
- [Web dashboard](ui.md) for the management UI you will spend most of your time in.
- [Local models](local-models.md) for running the fleet on your own LLM.
