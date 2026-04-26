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

## Build and run

```bash
# Copy and edit the example config
cp config.example.yaml config.yaml

# Build
go build -o agents ./cmd/agents

# Import config into SQLite and start
./agents --db agents.db --import config.yaml

# Subsequent starts (no --import needed)
./agents --db agents.db
```

Or run the interactive assistant:

```bash
./agents setup
```

The setup assistant validates daemon readiness (`/status`, `/backends/status`, `/backends/discover`, `/agents/orphans/status`) before completion, so you know your CLIs and tokens are wired correctly before the first scheduled run.

## On-demand single agent run

Useful for testing without waiting for a cron tick or webhook:

```bash
./agents --db agents.db --run-agent <agent-name> --repo owner/repo
```

Or via HTTP on the running daemon:

```bash
curl -X POST https://<your-host>/run \
  -H "Content-Type: application/json" \
  -d '{"agent":"coder","repo":"owner/repo"}'
```

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

- [Configuration](configuration.md) for the full config schema (skills, agents, repos, backends).
- [Docker deployment](docker.md) for production setup behind a reverse proxy.
- [Web dashboard](ui.md) for the management UI you will spend most of your time in.
- [Local models](local-models.md) for running the fleet on your own LLM.
