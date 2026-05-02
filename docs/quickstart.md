# Quick start

The fastest path from zero to a daemon dispatching agents on a GitHub repo. Plan on ~10 minutes if you already have Claude Code or Codex installed; ~20 if you don't.

Docker Compose is the recommended way to run the daemon, and is the path this guide follows. The agents call their AI CLIs with sandbox-bypass flags (`--dangerously-skip-permissions` for Claude, `--dangerously-bypass-approvals-and-sandbox` for Codex) so they can edit files and run tools without per-call prompts. Container isolation is what makes that safe — the runners are unrestricted *inside* the container, bounded *outside* it. Running the binary directly on your host is supported but not recommended; see [Build from source](#build-from-source) at the bottom.

## Requirements

| Dependency | Where | Purpose |
|---|---|---|
| Docker + Docker Compose | host | Runs the daemon |
| Claude Code or Codex | host | The AI backend that does the work; its config dir is bind-mounted into the container |
| [GitHub MCP server](https://github.com/github/github-mcp-server) | configured on the AI CLI | Authenticated GitHub access for the AI CLIs (the only path the daemon and its agents use to reach GitHub) |

You only need one of Claude Code or Codex to get started. Both can coexist in the same fleet on a per-agent basis.

## 1. Install an AI CLI on the host

Follow the official setup guides:

- [Claude Code](https://code.claude.com/docs/en/setup)
- [Codex](https://github.com/openai/codex)

Then register the GitHub MCP server against whichever CLI you installed:

- [Claude Code + GitHub MCP](https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-claude.md)
- [Codex + GitHub MCP](https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-codex.md)

The container reuses your host's CLI auth and MCP config through bind mounts, so getting these working on the host once is enough.

> Claude Code stores MCP config per-project keyed by the working directory. Inside the container the working directory is `/`, so the relevant `~/.claude.json` entry must be keyed under `/`. The simplest way to make this true is to add the GitHub MCP server with `claude mcp add -t http -s user github https://api.githubcopilot.com/mcp` from `/` (or `cd /` first), which writes a user-scope entry that applies everywhere.

## 2. Clone the repo and prepare config

```bash
git clone https://github.com/eloylp/agents
cd agents
cp config.example.yaml config.yaml
```

Edit `config.yaml` to taste. The shipped example is small enough to read in one pass and is a working starting point; you can grow the fleet later through the dashboard or CRUD API.

Create `.env` next to `docker-compose.yaml`:

```bash
cat > .env <<EOF
GITHUB_WEBHOOK_SECRET=$(openssl rand -hex 32)
EOF
chmod 600 .env
```

Save the secret — you'll paste the same value into the GitHub webhook in step 5.

## 3. Boot the daemon

```bash
docker compose up -d
docker compose logs -f agents
```

The compose file bind-mounts `./config.yaml`, your host's Claude Code / Codex config, and a named volume for the SQLite database. The daemon imports `config.yaml` on first boot if the database is empty; on subsequent boots it reads exclusively from the database and the YAML file is no longer consulted.

## 4. Verify it's healthy

```bash
curl -s http://localhost:8080/status | jq
```

Expect uptime, event-queue depth, and an `orphaned_agents.count` field. Then open the dashboard at <http://localhost:8080/ui/> — agents, skills, repos, and bindings are all manageable from there.

## 5. Wire a GitHub webhook

So events (label triggers, issue creation, PR opens, pushes) reach the daemon:

1. Open **Settings → Webhooks → Add webhook** on the repo.
2. **Payload URL**: `https://<your-host>/webhooks/github`
3. **Content type**: `application/json`
4. **Secret**: same value as `GITHUB_WEBHOOK_SECRET` in `.env`.
5. **Events**: enable any of Issues, Pull requests, Issue comments, Pull request reviews, Pull request review comments, Pushes. Pick whichever ones you want to trigger agents on. Unused events are silently dropped.
6. **Active**: checked.

See [events.md](events.md) for the full list of supported event kinds and their filtering rules.

For a publicly reachable URL behind auth (recommended for everything except `/webhooks/github`, `/status`, `/run`, and `/v1/*`), see [docker.md](docker.md#reverse-proxy-routing).

## 6. (Optional) Run an agent on demand

Useful for testing without waiting for a webhook or cron tick:

```bash
curl -X POST http://localhost:8080/run \
  -H "Content-Type: application/json" \
  -d '{"agent":"coder","repo":"owner/repo"}'
```

You can also use the MCP `trigger_agent` tool from any registered MCP client (Claude Code, Cursor, Cline). There is no separate CLI mode — running a second `agents` process out-of-band would not share the daemon's run-lock or dispatch dedup state.

## Next steps

- [Mental model](mental-model.md) for how the daemon composes prompts and what an agent must return. Read this before writing your first prompt.
- [Configuration](configuration.md) for the full config schema (skills, agents, repos, backends).
- [Docker deployment](docker.md) for the compose reference, reverse-proxy routing, day-2 operations, and image internals.
- [Web dashboard](ui.md) for the management UI you will spend most of your time in.
- [Local models](local-models.md) for running the fleet on your own LLM.

---

## Build from source

Prefer the Docker Compose path above unless you have a specific reason — running the AI CLIs with their sandbox-bypass flags directly on your host means the runners can edit any file and execute any tool the host user can. The container isolates that blast radius.

If you still want to:

```bash
go build -o agents ./cmd/agents
./agents setup                           # interactive first-time setup
# or:
./agents --db agents.db --import config.yaml   # one-time seed
./agents --db agents.db                         # subsequent starts
```

`./agents setup` walks you through wiring up the daemon and validates readiness end-to-end (`/status`, `/backends/status`, `/backends/discover`, `/agents/orphans/status`) before finishing, so you know your CLIs and tokens are correct before the first scheduled run.

After the first start, `config.yaml` is no longer read; the daemon boots from the persisted database. Export the current fleet back to YAML at any time via `GET /export`.
