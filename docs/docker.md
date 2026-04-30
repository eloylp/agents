# Docker deployment

A complete guide to running the daemon with Docker Compose: the compose file reference, pre-flight setup, reverse-proxy routing, first boot, and day-2 operations.

- [Quick start](#quick-start)
- [Compose reference](#compose-reference)
- [Pre-flight setup](#pre-flight-setup)
- [Reverse-proxy routing](#reverse-proxy-routing)
- [First boot](#first-boot)
- [Operations](#operations)
- [Image details](#image-details)

---

## Quick start

```bash
# First run: import config into the SQLite database, then exit.
docker compose run --rm agents --db /var/lib/agents/agents.db --import /etc/agents/config.yaml

# Start the daemon (subsequent runs boot from the persisted DB).
docker compose up -d
docker compose logs -f agents
docker compose down
```

The compose file shipped in the repo expects:

- `config.yaml` in the project root (mounted read-only at `/etc/agents/config.yaml` for `--import` seeding).
- `.env` in the project root, holding at least `GITHUB_WEBHOOK_SECRET` (and optionally `LOG_SALT` and `GITHUB_PAT_TOKEN`).

After the DB is seeded, `config.yaml` is no longer read at boot. The fleet lives in the SQLite volume and is managed through the `/ui/` dashboard or the CRUD API.

---

## Compose reference

The shipped `docker-compose.yaml` is intentionally minimal. Treat it as a base and layer your own reverse-proxy / auth on top.

```yaml
services:
  agents:
    build: .
    ports:
      - "8080:8080"
    environment:
      - HOME=/home/agents
      - GITHUB_PAT_TOKEN=${GITHUB_PAT_TOKEN}
    volumes:
      - ./config.yaml:/etc/agents/config.yaml:ro
      - agents-data:/var/lib/agents
      - ~/.claude:/home/agents/.claude
      - ~/.claude.json:/home/agents/.claude.json
      - ~/.codex:/home/agents/.codex
    env_file:
      - .env
    restart: unless-stopped

volumes:
  agents-data:
```

### Environment variables

| Variable | Source | Purpose |
|---|---|---|
| `HOME` | compose `environment:` | Must be `/home/agents` so Claude Code / Codex find their config dirs at the mounted paths. |
| `GITHUB_WEBHOOK_SECRET` | `.env` via `env_file` | HMAC shared secret used to verify `POST /webhooks/github`. Matches `daemon.http.webhook_secret_env`. |
| `LOG_SALT` (optional) | `.env` | Salt used to redact logged prompts. Matches `daemon.ai_backends.<name>.redaction_salt_env`. |
| `GITHUB_PAT_TOKEN` (optional) | `.env` | Surfaced in the container for tools that read it; the daemon itself does not call GitHub directly. |

### Volume mounts

| Host path | Container path | Purpose |
|---|---|---|
| `./config.yaml` | `/etc/agents/config.yaml` (ro) | Seed file for the one-time `--import`. Safe to leave mounted; unused once the DB is seeded. |
| `agents-data` (named volume) | `/var/lib/agents` | SQLite database (fleet config + autonomous-agent memory). This is the only piece of state you need to back up. |
| `~/.claude` | `/home/agents/.claude` | Claude Code session data and local auth. |
| `~/.claude.json` | `/home/agents/.claude.json` | Claude Code main config. **MCP server entries with auth headers live here**, not in `~/.claude/settings.json`. |
| `~/.codex` | `/home/agents/.codex` | Codex configuration. |

### Ports

The daemon listens on `:8080` (matches `daemon.http.listen_addr` in the example config). The compose file publishes `8080:8080` for local use. In production, drop the host-side publish and expose the service only through your reverse proxy's internal Docker network.

---

## Pre-flight setup

Before first boot, prepare the host so the bind-mounted Claude Code / Codex configs are valid inside the container.

### 1. AI CLIs

Install Claude Code and/or Codex on the host (the container has them too, but the session/auth data lives on the host through bind mounts):

- [Claude Code setup](https://code.claude.com/docs/en/setup)
- [Codex setup](https://github.com/openai/codex)

### 2. GitHub MCP server

GitHub access flows exclusively through the GitHub MCP server configured on each AI CLI. The daemon does not call GitHub directly, and no `gh` binary is installed in the image. Register the GitHub MCP server against Claude Code:

```bash
claude mcp add -t http -s user github https://api.githubcopilot.com/mcp
```

Important: entries that carry auth headers must end up in `~/.claude.json` (the bind-mounted file), not in `~/.claude/settings.json`. Verify from inside the running container:

```bash
docker compose exec agents claude mcp list
```

Claude Code stores MCP config per-project keyed by the working directory. Since the container's working directory is `/`, the project entry in `~/.claude.json` must be keyed under `/`. If you configured MCP from a different CWD on the host, copy the entry under the `/` key before starting the container.

See the companion install guides:

- [Claude Code + GitHub MCP](https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-claude.md)
- [Codex + GitHub MCP](https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-codex.md)

### 3. `.env`

```bash
cat > .env <<'EOF'
GITHUB_WEBHOOK_SECRET=<long-random-string>
LOG_SALT=<optional-salt>
GITHUB_PAT_TOKEN=<optional-pat>
EOF
chmod 600 .env
```

Use the same `GITHUB_WEBHOOK_SECRET` when you configure the GitHub webhook (see [README](../README.md#github-webhook-setup)).

---

## Reverse-proxy routing

All endpoints are unauthenticated at the daemon level. **Access control is the reverse proxy's responsibility** (see [security.md](security.md)). A working production pattern is a two-router split: authenticated UI/API, public webhook endpoints.

| Router | Paths | Auth | Purpose |
|---|---|---|---|
| **UI / API** (authenticated) | everything except the public paths below | basic auth, OAuth2 proxy, or mTLS | `/ui/`, `/agents`, `/skills`, `/repos`, `/traces`, `/events`, `/graph`, `/memory`, `/config`, `/export`, `/import`, `/backends` |
| **Public** (no auth) | `/status`, `/webhooks/github`, `/run`, `/v1/*` | none | GitHub can't send a basic-auth header on webhooks; `/status` must stay reachable for liveness probes; `/run` and `/v1/*` (proxy) are meant to be called by trusted external systems that authenticate with their own mechanism. |

`/webhooks/github` is safe to expose publicly because every request is HMAC-verified against `GITHUB_WEBHOOK_SECRET` before it is accepted. `/run` does not currently authenticate callers. If you enable it, restrict it at the proxy with an allowlist or a shared secret header.

### Adapting the compose for production

The shipped `docker-compose.yaml` publishes `8080:8080` for local use. For a production deploy behind a proxy:

- Drop the `ports:` block; let the proxy reach the container on the internal Docker network instead.
- Replace `build: .` with `image:` pointing at a pre-built image you ship from CI.
- Add proxy-specific routing on top — labels for Traefik, server blocks for nginx, a Caddyfile for Caddy. The compose file stays proxy-agnostic; the auth layer lives at your proxy.

The two-router split below is one concrete pattern using Traefik. The principle (auth on UI/API, no auth on webhooks/status/run/v1) carries over to any proxy.

### Traefik example

```yaml
services:
  agents:
    # ... build, volumes, env as above ...
    # No host-side ports: the proxy routes to the container on the internal network.
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=web"

      # Public router: webhooks, status, on-demand trigger, proxy.
      - "traefik.http.routers.agents-public.rule=Host(`agents.example.com`) && (PathPrefix(`/webhooks`) || Path(`/status`) || PathPrefix(`/run`) || PathPrefix(`/v1`))"
      - "traefik.http.routers.agents-public.entrypoints=websecure"
      - "traefik.http.routers.agents-public.tls.certresolver=letsencrypt"

      # Authenticated router: everything else.
      - "traefik.http.routers.agents-ui.rule=Host(`agents.example.com`)"
      - "traefik.http.routers.agents-ui.entrypoints=websecure"
      - "traefik.http.routers.agents-ui.tls.certresolver=letsencrypt"
      - "traefik.http.routers.agents-ui.middlewares=agents-auth@docker"

      - "traefik.http.middlewares.agents-auth.basicauth.usersfile=/etc/traefik/agents.htpasswd"

      - "traefik.http.services.agents.loadbalancer.server.port=8080"

networks:
  default:
    name: web
    external: true
```

Traefik picks the more specific router first (public matches specific path prefixes; the UI router is the host-wide catch-all), so webhook traffic bypasses the auth middleware.

Adapt the pattern to Caddy, nginx, or whichever proxy you already run. The key constraint is that the auth layer must not be applied to `/webhooks/github`, `/status`, `/run`, or `/v1/*`.

---

## First boot

1. **Start with an empty DB.** On first launch the daemon runs backend discovery automatically (because the backends table is empty) and persists the result. Subsequent starts skip discovery; rerun it explicitly with `POST /backends/discover`.

2. **Verify the daemon is up:**

   ```bash
   curl -s http://localhost:8080/status | jq
   ```

   Expect uptime, event queue depth, and an `orphaned_agents.count` field. If you proxy through the auth router, hit `/status` via the public router instead.

3. **Open the dashboard** at `/ui/` and create agents, skills, repos through the CRUD editors. This is the intended fleet-management path; the YAML file is only a seeding convenience.

4. **Or import from YAML** into the already-running instance:

   ```bash
   docker compose exec agents /agents --db /var/lib/agents/agents.db --import /etc/agents/config.yaml
   ```

   Or via HTTP:

   ```bash
   curl -X POST http://localhost:8080/import \
     -H "Content-Type: application/x-yaml" \
     --data-binary @config.yaml
   ```

5. **Wire the GitHub webhook** so events reach `/webhooks/github`. See the [webhook setup section in the README](../README.md#github-webhook-setup) for the exact event list.

---

## Operations

### Logs

```bash
docker compose logs -f agents
docker compose logs --since 1h agents | jq  # when daemon.log.format: json
```

### Restart

```bash
docker compose restart agents
```

The daemon serves SIGTERM gracefully (up to `daemon.http.shutdown_timeout_seconds`). In-flight agent runs are allowed to finish.

### Rerun backend discovery

Backend discovery only runs automatically on the first boot with an empty DB. Trigger a manual refresh after installing a new CLI, rotating auth, or changing a local model URL:

```bash
curl -X POST http://localhost:8080/backends/discover
```

`GET /backends/status` returns the same diagnostics live, without persisting.

### Trigger an agent on demand

```bash
curl -X POST http://localhost:8080/run \
  -H "Content-Type: application/json" \
  -d '{"agent":"coder","repo":"owner/repo"}'
```

Returns `202 Accepted` with an `event_id`; the agent runs asynchronously. Watch its trace from `/ui/` → Traces, or via `GET /traces/stream`.

### Export and import the fleet

```bash
curl -s http://localhost:8080/export > fleet.yaml
curl -X POST http://localhost:8080/import \
  -H "Content-Type: application/x-yaml" \
  --data-binary @fleet.yaml
```

### Back up the database

Everything reproducible about the fleet is in the `agents-data` volume. Back it up either by copying the volume or by taking a SQLite snapshot from inside the container:

```bash
docker compose exec agents sqlite3 /var/lib/agents/agents.db ".backup /var/lib/agents/backup-$(date +%F).db"
docker compose cp agents:/var/lib/agents/backup-$(date +%F).db ./
```

For a consistent snapshot without quiescing the daemon, `.backup` uses SQLite's online backup API and is safe while the daemon is running.

### Upgrade

```bash
git pull
docker compose build
docker compose up -d
```

The schema migrates on start; no manual step. The DB is forward-compatible within a major version; roll back by restoring the backup if needed.

---

## Image details

Multi-stage build on `node:22-alpine`:

1. **UI builder** compiles the embedded Next.js dashboard.
2. **Go builder** (`golang:1.25-alpine`) produces a static daemon binary with the UI assets embedded.
3. **Runtime** installs Claude Code and Codex via npm, creates a non-root `agents` user, and runs `/agents --db /var/lib/agents/agents.db` as the default CMD. GitHub access flows through the GitHub MCP server configured on each AI CLI; no `gh` binary is baked in.

The container runs as UID:GID of the `agents` user. Bind-mounted host paths (`~/.claude`, `~/.claude.json`, `~/.codex`) must be readable by that user. If you're mounting from a host account with a different UID, either `chown` the host paths or add a `user:` override in compose.
