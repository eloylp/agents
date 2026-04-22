# Docker deployment

## Quick start

```bash
docker compose up -d
docker compose up -d --build   # rebuild after code changes
docker compose logs -f agents
docker compose down
```

The compose file expects:
- `config.yaml` in the project root (mounted read-only at `/etc/agents/config.yaml`)
- `.env` in the project root with `GITHUB_WEBHOOK_SECRET` (and optionally `LOG_SALT` and `GITHUB_PAT_TOKEN`)

## Volume mounts

| Host path | Container path | Purpose |
|---|---|---|
| `config.yaml` | `/etc/agents/config.yaml` (read-only) | Main daemon config (used for `--import`; optional once DB is seeded) |
| `./agents` | `/etc/agents/agents` (read-only) | Optional: source tree for agent and skill `prompt_file:` paths that reference this directory |
| `~/.claude` | `/home/agents/.claude` | Claude Code session data |
| `~/.claude.json` | `/home/agents/.claude.json` | Claude Code main config |
| `~/.codex` | `/home/agents/.codex` | Codex configuration |
| `~/.config/gh` | `/home/agents/.config/gh` (read-only) | GitHub CLI auth tokens |
| `agents-memory` (volume) | `/var/lib/agents/memory` | Autonomous agent memory across restarts |

## MCP server configuration

Claude Code stores MCP config per-project, keyed by working directory in `~/.claude.json`. Since the container's working directory is `/`, ensure `~/.claude.json` has a project entry for `/` with the GitHub MCP server configured. Verify with:

```bash
docker exec agents claude mcp list
```

## Image details

Multi-stage build on `node:22-alpine`. The image includes Claude Code, Codex, and `gh` CLIs alongside the daemon. Runs as non-root `agents` user. Default CMD is `--db /var/lib/agents/agents.db` (SQLite mode; no `--config` flag needed after the initial `--import`).
