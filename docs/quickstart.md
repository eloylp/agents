# Quickstart

## Why a container

The daemon dispatches AI CLIs (`claude`, `codex`) with sandbox-bypass flags so agents can edit files and run tools without per-call prompts. The container is what bounds that blast radius, running the binary on your host is not supported. See [security.md](security.md) for the threat model.

## Bring up the daemon

```bash
mkdir agents && cd agents
curl -fsSLO https://raw.githubusercontent.com/eloylp/agents/main/docker-compose.yaml
curl -fsSLO https://raw.githubusercontent.com/eloylp/agents/main/.env.sample
curl -fsSL https://raw.githubusercontent.com/eloylp/agents/main/scripts/init-env.sh | sh
docker compose up -d
```

The shipped [`docker-compose.yaml`](../docker-compose.yaml) is the source of truth for what gets mounted and exposed. The daemon image (`ghcr.io/eloylp/agents`) is the control plane: UI, REST/MCP, scheduler, queue, traces, and SQLite. Agent work runs in fresh ephemeral containers from the runner image (`ghcr.io/eloylp/agents-runner`), which contains Claude Code, Codex, `gh`, git, Go, Rust/Cargo, Node/npm, TypeScript, and the other execution tools. The daemon boots against an empty database with built-in defaults, no YAML seed is required.

Compose mounts `/var/run/docker.sock` into the daemon so it can start runner containers. The shipped Compose file runs the daemon process as root inside the container because Docker socket group IDs vary by host; Docker socket access is root-equivalent on the host, so treat it as a production security boundary.

> **First-run note.** The compose file pulls `ghcr.io/eloylp/agents:latest`, which is only updated from version tags. Main-branch builds are published separately as `ghcr.io/eloylp/agents:dev-<short_sha>` so users do not accidentally pull development images.

Verify the daemon is healthy:

```bash
curl -s http://localhost:8080/status | jq
```

## Configure credentials

Production runs are env-driven. Put credentials in `.env`; they are injected into each short-lived runner container and are not exported through UI, REST, MCP, or fleet YAML.

- `GITHUB_TOKEN`: used for GitHub MCP and `gh` fallback. Use `repo` scope minimum; add `workflow` if agents touch CI.
- Claude: preferred path, run `claude setup-token` locally and set the returned value as `CLAUDE_CODE_OAUTH_TOKEN`. API-key deployments may instead set `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`.
- Codex: preferred for ChatGPT/Plus/Pro subscription access, run `codex login` locally with file-based credential storage and set `CODEX_AUTH_JSON_BASE64` from `~/.codex/auth.json`; alternatively set `OPENAI_API_KEY` for OpenAI Platform API-billed usage.

The one-liner above runs [`scripts/init-env.sh`](../scripts/init-env.sh), which generates `GITHUB_WEBHOOK_SECRET`, explains each credential, and prompts for the values interactively. To rerun it later after rotating credentials:

```bash
curl -fsSL https://raw.githubusercontent.com/eloylp/agents/main/scripts/init-env.sh | sh
docker compose restart agents
```

For Claude subscription auth:

```bash
claude setup-token
# Paste the returned value when the script asks for CLAUDE_CODE_OAUTH_TOKEN.
```

For Codex subscription auth, make sure the local CLI writes a portable auth file:

```toml
# ~/.codex/config.toml
cli_auth_credentials_store = "file"
```

Then run:

```bash
codex login
test -f ~/.codex/auth.json
CODEX_AUTH_JSON_BASE64="$(base64 < ~/.codex/auth.json | tr -d '\n')"
```

Copy that value into `.env`. Treat it like a password; it contains refreshable Codex credentials. The daemon does not mount your home directory or any Codex volume into runner containers. It passes the base64 value through the runner environment and materializes `auth.json` only inside each ephemeral runner container.

## Bootstrap the dashboard and tooling

Open `http://localhost:8080/` and create the first admin user. A fresh install sends you to the browser tooling setup wizard at `/ui/setup/tooling/` before the graph designer. The wizard shows the current health of the daemon, runner image, `gh`, Claude, Codex, GitHub MCP, and backend model discovery.

When the wizard reports missing interactive auth, run the terminal companion again, restart the daemon, then click **Re-check** or **Refresh discovery** in the wizard. When the selected backend tooling is healthy, continue into the graph workflow designer. **Fleet configuration (workspaces, agents, prompts, skills, repos, bindings, webhooks) lives in the dashboard**. Those tasks are graphical-shaped and don't fit a bash prompt loop.

Before enabling scheduled runs, perform a smoke test from the dashboard or REST API: run a trivial agent against a test repository and confirm the run creates a fresh runner container, streams trace steps while in flight, persists the final trace, and removes the runner container afterward. This proves the mounted Docker socket and configured runner image work in your environment.

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

# Re-run backend discovery (after rotating env credentials or changing runner image).
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

The `agents-data` volume is the only piece of state worth backing up regularly. Runtime secrets should live in your environment, compose secret management, or a future secret store, not in config exports.

## Next steps

- [Mental model](mental-model.md), how the daemon composes prompts and what an agent must return. Read this before writing your first prompt.
- [Configuration](configuration.md), full schema (workspaces, prompts, skills, agents, repos, backends, guardrails).
- [Web dashboard](ui.md), the management UI you will spend most of your time in.
- [Local models](local-models.md), running the fleet on your own LLM.
- [Security](security.md), threat model, recommendations, reverse-proxy routing.
