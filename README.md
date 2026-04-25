# Agents

![agents](docs/agents.jpg)

**Your personal, provider-agnostic tool for building reusable, event-driven agentic workflows.**

Define your agents once. Wire them to repos with labels, cron schedules, or event subscriptions. The daemon dispatches them via AI CLIs ([Claude Code](https://docs.anthropic.com/en/docs/claude-code), [Codex](https://github.com/openai/codex)) and lets them work through native GitHub primitives -- issues, PRs, reviews, comments.

---

## Features

- **Self-hosted, no SaaS** -- your code and prompts stay on your infrastructure.
- **Multi-backend** -- Claude, Codex, and named local backends. Mix backends per agent.
- **Discovery + diagnostics** -- daemon detects backend/tools, validates CLI health (MCP connectivity, models), and persists discovery snapshots.
- **Local model support** -- built-in Anthropic-to-OpenAI translation proxy routes the fleet through `llama.cpp`, Ollama, vLLM, or any OpenAI-compatible endpoint. Zero vendor lock-in.
- **One agent model, many triggers** -- label events, cron schedules, GitHub event subscriptions, on-demand API calls. Same agent, wired however you want.
- **Composable skills** -- reusable guidance blocks (architecture, security, testing, DX, ...) merged into any agent.
- **Reactive inter-agent dispatch** -- agents invoke each other at runtime with depth, fanout, and dedup safety limits.
- **SQLite config store** -- manage the fleet over a CRUD API instead of editing YAML. Import/export between the two.
- **Built-in web dashboard** -- live event firehose, agent traces with tool-loop transcripts, dispatch graph, memory viewer.
- **Transparent** -- every agent action is a GitHub comment, issue, or PR. Reviewable. Revertable.

---

## Web dashboard

The daemon ships an embedded web dashboard at `/ui/` with real-time views of your fleet. The dashboard is the primary interface for managing agents -- all fleet operations (create, edit, delete agents, skills, backends, repos) are available through the CRUD editors, alongside live monitoring and observability.

<!-- TODO: add screenshots of the dashboard pages (events, traces, graph, agents, memory) -->

| Page | What it shows |
|------|---------------|
| **Events** | Live webhook event firehose with SSE streaming |
| **Traces** | Agent run traces with timing, status, and drill-down to tool-loop transcripts |
| **Graph** | Visual dispatch graph -- which agents invoke which, with edge counts |
| **Agents** | Fleet snapshot -- per-agent status, skills, bindings, dispatch wiring. Create, edit, and delete agents |
| **Skills** | Manage reusable guidance blocks -- create, edit, delete |
| **Backends and tools** | Backend discovery status (including per-backend GitHub MCP connectivity), runtime limits, local backend URL management, and orphaned-model remediation |
| **Repos** | Repository bindings -- wire agents to repos with labels, events, or cron triggers |
| **Memory** | Raw agent memory markdown per (agent, repo) pair |
| **Config** | Effective parsed config (secrets redacted). YAML import/export |

---

## How it works

Agents are triggered in three ways. Every path ends with the same execution model: the daemon composes a prompt (skills + agent prompt + context), hands it to the AI CLI, and the CLI reads/writes GitHub through its MCP tools.

### Label-triggered (event-driven)

```
Developer adds label "ai:review:arch-reviewer" to a PR
  → GitHub sends a webhook to the daemon
  → Daemon verifies, deduplicates, matches the repo binding
  → Dispatches the bound agent via the AI CLI
  → AI reads the PR, posts a review comment
```

### Cron-scheduled (autonomous)

```
Cron fires (e.g. every 30 minutes)
  → Daemon injects the agent's persisted memory into the prompt
  → AI CLI picks up where it left off — checks open issues, continues work
  → Returns updated memory + artifacts for the next cycle
```

### Event-subscribed

```
Developer opens an issue / pushes to a branch / submits a review
  → GitHub sends the matching webhook event
  → Daemon routes it to every agent subscribed to that event kind
  → Each agent runs independently with the event payload as context
```

### Reactive dispatch (agent-to-agent)

Any agent can invoke another at runtime by returning a `dispatch` array. The daemon enqueues the target agent as a new event with depth, fanout, and dedup safety limits.

```
Agent A finishes a code fix, returns dispatch: [{agent: "pr-reviewer", ...}]
  → Daemon validates wiring (allow_dispatch + can_dispatch)
  → Enqueues a synthetic event for Agent B
  → Agent B reviews the PR opened by Agent A
```

---

## Quick start

### Requirements

| Dependency | Purpose |
|---|---|
| **Go 1.25+** | Build the daemon |
| **AI CLI** (Claude Code and/or Codex) | The actual AI backend |
| **[GitHub MCP server](https://github.com/github/github-mcp-server)** | Authenticated GitHub access for the AI CLIs — the only path the daemon and its agents use to reach GitHub |

### Setup

Follow the official setup guides:
- [Claude Code](https://code.claude.com/docs/en/setup) + [GitHub MCP](https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-claude.md)
- [Codex](https://github.com/openai/codex) + [GitHub MCP](https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-codex.md)

### Build and run

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

The setup assistant now validates daemon APIs during setup (`/status`, `/backends/status`, `/backends/discover`, `/agents/orphans/status`) so backend/tool readiness is verified before completion.

### On-demand agent pass

Run one autonomous agent synchronously and exit (useful for testing):

```bash
./agents --db agents.db --run-agent coder --repo owner/repo
```

Or via HTTP on the running daemon:

```bash
curl -X POST https://<your-host>/run \
  -H "Content-Type: application/json" \
  -d '{"agent":"coder","repo":"owner/repo"}'
```

### Backend discovery and diagnostics

- On startup, backend auto-discovery runs only when the backends table is empty.
- `POST /backends/discover` reruns discovery and persists results to DB.
- `GET /backends/status` runs live diagnostics without mutating DB.
- `GET /agents/orphans/status` reports agents that pin unavailable models.
- `GET /status` includes `orphaned_agents.count` for global warning/banner UX.

### GitHub webhook setup

1. Go to **Settings -> Webhooks -> Add webhook** in your repository.
2. **Payload URL**: `https://<your-host>/webhooks/github`
3. **Content type**: `application/json`
4. **Secret**: same value as `GITHUB_WEBHOOK_SECRET`.
5. **Events**: enable **Issues**, **Pull requests**, **Issue comments**, **Pull request reviews**, **Pull request review comments**, and/or **Pushes** -- whichever ones you want to trigger agents on. Unused events are silently dropped.
6. **Active**: checked.

---

## Documentation

| Document | What it covers |
|----------|----------------|
| [Configuration](docs/configuration.md) | Full config walkthrough: daemon, skills, agents, repos, labels, environment variables, SQLite mode |
| [Supported events](docs/events.md) | All GitHub event kinds, payload fields, and filtering rules |
| [Inter-agent dispatch](docs/dispatch.md) | Reactive dispatch: response contract, safety limits, config wiring |
| [HTTP API](docs/api.md) | All endpoints: core, observability, proxy, CRUD, and the AI runner contract |
| [Docker deployment](docs/docker.md) | Docker Compose setup, volume mounts, MCP configuration |
| [Local models](docs/local-models.md) | Run the fleet on your own LLM: proxy setup, model picks, tuning, cost math |
| [Security](docs/security.md) | Webhook verification, prompt redaction, access control model |
| [Contributing](CONTRIBUTING.md) | How to contribute: issues-first model, what makes a great issue |

---

## Project structure

```
cmd/agents/main.go          # Daemon entry point + --run-agent / --db / --import modes
internal/
  config/                   # YAML parsing, prompt/skill file resolution, validation
  ai/                       # Prompt composition + command-based CLI runner (per-backend env)
  anthropic_proxy/          # Built-in Anthropic-to-OpenAI translation proxy (opt-in)
  observe/                  # Observability store (events, traces, dispatch graph, SSE hubs)
  autonomous/               # Cron scheduler + agent memory (SQLite-backed)
  backends/                 # Backend discovery: CLI probing, GitHub MCP health checks, orphan detection
  store/                    # SQLite-backed config store: schema migrations, CRUD helpers
  workflow/                 # Event routing engine, single event queue, processor, inter-agent dispatcher
  server/                   # Shared HTTP server types (cross-cutting interfaces, error sentinels)
  webhook/                  # HTTP server, signature verification, delivery dedupe, CRUD API handlers
  mcp/                      # MCP server exposing fleet-management tools at /mcp
  ui/                       # Embedded Next.js web dashboard (served at /ui/)
  setup/                    # Interactive first-time setup command
  logging/                  # zerolog setup
docs/                       # Long-form docs (configuration, events, dispatch, API, docker, etc.)
```

---

## Testing

```bash
go test ./... -race
```

---

## Logging

Two formats via `daemon.log.format`:

- **`text`** (default) -- coloured, human-readable.
- **`json`** -- structured for log aggregation (Loki, Datadog, etc).

Every entry includes `repo`, `issue_number` or `pr_number`, and `component` for filtering.

```json
{"level":"info","component":"workflow_engine","repo":"owner/repo","pr_number":42,"backend":"claude","message":"invoking ai agent"}
```

---

## Contributing

This project is built by its own agent fleet. **You bring the ideas, the agents bring the implementation.** Open an issue with the `discussing` label, a maintainer triages it, and the autonomous coder agent implements accepted issues -- reviewed by the pr-reviewer agent before merge.

See **[CONTRIBUTING.md](CONTRIBUTING.md)** for the full process, what makes a great issue, and the exceptions (doc typo PRs and security patches are accepted directly).
