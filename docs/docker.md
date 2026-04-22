# Docker deployment

## Quick start

```bash
# First run: import config into the SQLite database
docker compose run --rm agents --db /var/lib/agents/agents.db --import /etc/agents/config.yaml

# Start the daemon (subsequent runs boot from the persisted DB)
docker compose up -d
docker compose logs -f agents
docker compose down
```

The compose file expects:
- `config.yaml` in the project root (mounted read-only at `/etc/agents/config.yaml`)
- `.env` in the project root with `GITHUB_WEBHOOK_SECRET` (and optionally `LOG_SALT` and `GITHUB_PAT_TOKEN`)

## Volume mounts

| Host path | Container path | Purpose |
|---|---|---|
| `config.yaml` | `/etc/agents/config.yaml` (read-only) | Daemon config (used for `--import` seeding; optional once DB is seeded) |
| `./agents` | `/etc/agents/agents` (read-only) | Optional: source tree for agent and skill `prompt_file:` paths that reference this directory |
| `~/.claude` | `/home/agents/.claude` | Claude Code session data |
| `~/.claude.json` | `/home/agents/.claude.json` | Claude Code main config |
| `~/.codex` | `/home/agents/.codex` | Codex configuration |
| `~/.config/gh` | `/home/agents/.config/gh` (read-only) | GitHub CLI auth tokens |
| `agents-data` (volume) | `/var/lib/agents` | SQLite database (config + agent memory) across restarts |

## MCP server configuration

Claude Code stores MCP config per-project, keyed by working directory in `~/.claude.json`. Since the container's working directory is `/`, ensure `~/.claude.json` has a project entry for `/` with the GitHub MCP server configured. Verify with:

```bash
docker compose exec agents claude mcp list
```

## Image details

Multi-stage build on `node:22-alpine`. The image includes Claude Code, Codex, and `gh` CLIs alongside the daemon. Runs as non-root `agents` user. Default CMD is `--db /var/lib/agents/agents.db`.
