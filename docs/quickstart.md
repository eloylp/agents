# Quickstart

## Why a container

The daemon dispatches AI CLIs (`claude`, `codex`) with sandbox-bypass flags so agents can edit files and run tools without per-call prompts. The container is what bounds that blast radius, running the binary on your host is not supported. See [security.md](security.md) for the threat model.

## Bring up the daemon

```bash
mkdir agents && cd agents
curl -fsSLO https://raw.githubusercontent.com/eloylp/agents/main/docker-compose.yaml
curl -fsSLO https://raw.githubusercontent.com/eloylp/agents/main/.env.sample
# .env holds runtime secrets (loaded automatically by compose).
# Webhook secret: random per install. PAT: from https://github.com/settings/tokens with repo scope.
cp .env.sample .env
sed -i.bak "s/^GITHUB_WEBHOOK_SECRET=.*/GITHUB_WEBHOOK_SECRET=$(openssl rand -hex 32)/" .env && rm .env.bak
# Edit GITHUB_TOKEN in .env before continuing.
docker compose up -d
```

The shipped [`docker-compose.yaml`](../docker-compose.yaml) is the source of truth for what gets mounted and exposed. Two named volumes back the runtime: `agents-data` (SQLite store) and `agents-home` (Claude / Codex auth, MCP config, and `gh` auth). The image includes the AI CLIs plus `git`, `gh`, Go, Rust/Cargo, Node/npm, and TypeScript so agents can run local checkout/test loops when MCP alone is not enough. The daemon boots against an empty database with built-in defaults, no YAML seed is required.

> **First-run note.** The compose file pulls `ghcr.io/eloylp/agents:latest`, which is only updated from version tags. Main-branch builds are published separately as `ghcr.io/eloylp/agents:dev-<short_sha>` so users do not accidentally pull development images.

Verify the daemon is healthy:

```bash
curl -s http://localhost:8080/status | jq
```

## Authenticate the AI CLIs

```bash
docker compose exec -it agents agents-setup
```

`agents-setup` is a small bash script (see [`scripts/setup.sh`](../scripts/setup.sh)) that does only what genuinely needs interactive shell access:

1. picks which AI backend(s) you want, claude, codex, or both,
2. runs `claude auth login` and `codex login --device-auth` against your terminal so you can complete the OAuth flow in your browser,
3. registers the GitHub MCP server on each authenticated CLI,
4. authenticates the `gh` CLI with the same `GITHUB_TOKEN` for fallback local checkout/test/PR flows,
5. refreshes the daemon's backend discovery so the fleet sees the freshly authenticated tooling,
6. prints diagnostics from `/status`, `/backends/status`, `/agents/orphans/status`.

Once it finishes, the daemon has working backends and tools. **Fleet configuration (agents, skills, repos, bindings, webhooks) lives in the dashboard**, open `http://localhost:8080/`, sign in or bootstrap the first user, and configure from there. Those tasks are graphical-shaped and don't fit a bash prompt loop.

## Production essentials

Before exposing the daemon publicly, open the root login page and create the first local user, then create named API tokens for MCP/REST clients from Config -> Authentication. Configure your reverse proxy for TLS/routing: see [security.md → Daemon auth](security.md#daemon-auth) and [Reverse-proxy routing](security.md#reverse-proxy-routing).

## Day-2 operations

```bash
# Tail logs.
docker compose logs -f agents

# Graceful restart (in-flight runs are allowed to finish).
docker compose restart agents

# Upgrade to the latest published image.
docker compose pull agents && docker compose up -d agents

# To pin a tagged release, edit docker-compose.yaml to use either:
# image: ghcr.io/eloylp/agents:0.2.0
# or:
# image: ghcr.io/eloylp/agents:v0.2.0
# then run:
# docker compose pull agents && docker compose up -d agents

# To test an unreleased main-branch build, explicitly use:
# image: ghcr.io/eloylp/agents:dev-<short_sha>

# Re-run backend discovery (after rotating auth or adding a CLI).
curl -X POST http://localhost:8080/backends/discover

# Snapshot the SQLite store while the daemon runs (the agents image
# does not ship sqlite3, use a one-shot Alpine sidecar against the
# data volume; SQLite's online-backup API handles concurrent writes).
docker run --rm \
  -v $(basename "$PWD")_agents-data:/src \
  -v "$PWD/backups":/dst \
  alpine sh -c 'apk add --no-cache sqlite >/dev/null && \
    sqlite3 /src/agents.db ".backup /dst/agents-$(date +%F).db"'

# Export / re-import the fleet as YAML.
curl -s http://localhost:8080/export > fleet.yaml
curl -X POST -H 'Content-Type: application/x-yaml' \
  --data-binary @fleet.yaml http://localhost:8080/import
```

The `agents-data` volume is the only piece of state worth backing up regularly, `agents-home` holds OAuth tokens and is meant to be re-populated via `agents-setup` rather than backed up.

## Next steps

- [Mental model](mental-model.md), how the daemon composes prompts and what an agent must return. Read this before writing your first prompt.
- [Configuration](configuration.md), full schema (skills, agents, repos, backends, guardrails).
- [Web dashboard](ui.md), the management UI you will spend most of your time in.
- [Local models](local-models.md), running the fleet on your own LLM.
- [Security](security.md), threat model, recommendations, reverse-proxy routing.
