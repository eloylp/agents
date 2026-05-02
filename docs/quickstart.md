# Quickstart

## Why a container

The daemon dispatches AI CLIs (`claude`, `codex`) with sandbox-bypass flags so agents can edit files and run tools without per-call prompts. The container is what bounds that blast radius — running the binary on your host is not supported. See [security.md](security.md) for the threat model.

## Bring up the daemon

```bash
git clone https://github.com/eloylp/agents
cd agents
cp config.example.yaml config.yaml
echo "GITHUB_WEBHOOK_SECRET=$(openssl rand -hex 32)" > .env
docker compose up -d
```

The shipped [`docker-compose.yaml`](../docker-compose.yaml) is the source of truth for what gets mounted and exposed. Two named volumes back the runtime: `agents-data` (SQLite store) and `agents-home` (Claude / Codex auth + MCP config).

Verify the daemon is healthy:

```bash
curl -s http://localhost:8080/status | jq
```

## Run the setup wizard

```bash
docker compose exec -it agents agents setup
```

This drops you into an interactive Claude REPL inside the container. The wizard walks the operator phase by phase:

1. asks which AI backend(s) you want to use (claude, codex, or both),
2. guides you through `!claude login` / `!codex login` and GitHub MCP registration via shell-escape commands,
3. gathers the repos you want to manage and validates them via the GitHub MCP,
4. seeds a starter fleet by POSTing a YAML payload to `/import`,
5. runs diagnostics against `/status`, `/backends/status`, `/agents/orphans/status`,
6. registers webhooks via the GitHub MCP server.

When the wizard finishes, the fleet is live. Manage it from `http://localhost:8080/ui/`.

## Manual walkthrough (optional)

If you'd rather not use the wizard, the steps it performs are documented in [`internal/setup/prompt.md`](../internal/setup/prompt.md). Each phase is a few HTTP calls or `claude` / `codex` invocations against the running container — copy them yourself.

## Production essentials

Before exposing the daemon publicly, configure your reverse proxy: see [security.md → Reverse-proxy routing](security.md#reverse-proxy-routing) for the auth-vs-public path split (which paths must sit behind your auth layer, which must stay open so GitHub webhooks and liveness probes can reach the daemon).

## Day-2 operations

```bash
# Tail logs.
docker compose logs -f agents

# Graceful restart (in-flight runs are allowed to finish).
docker compose restart agents

# Upgrade.
git pull && docker compose build && docker compose up -d

# Re-run backend discovery (after rotating auth or adding a CLI).
curl -X POST http://localhost:8080/backends/discover

# Snapshot the SQLite store while the daemon runs.
docker compose exec agents sqlite3 /var/lib/agents/agents.db \
  ".backup /var/lib/agents/backup-$(date +%F).db"

# Export / re-import the fleet as YAML.
curl -s http://localhost:8080/export > fleet.yaml
curl -X POST -H 'Content-Type: application/x-yaml' \
  --data-binary @fleet.yaml http://localhost:8080/import
```

The `agents-data` volume is the only piece of state worth backing up regularly — `agents-home` holds OAuth tokens and is meant to be re-populated via `claude login` rather than backed up.

## Next steps

- [Mental model](mental-model.md) — how the daemon composes prompts and what an agent must return. Read this before writing your first prompt.
- [Configuration](configuration.md) — full schema (skills, agents, repos, backends, guardrails).
- [Web dashboard](ui.md) — the management UI you will spend most of your time in.
- [Local models](local-models.md) — running the fleet on your own LLM.
- [Security](security.md) — threat model, recommendations, reverse-proxy routing.
