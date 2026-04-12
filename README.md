# agents

![agents](docs/agents.jpg)

**Turn GitHub labels into AI-powered code reviews and issue refinements — automatically.**

A lightweight Go daemon that listens for GitHub webhook events and dispatches AI CLI agents ([Claude Code](https://docs.anthropic.com/en/docs/claude-code), [Codex](https://github.com/openai/codex), ...) to analyze issues and review pull requests. Just add a label, and the right AI agent shows up in seconds.

---

## Why?

- **Zero-friction** — your team already uses labels. No new tools to learn.
- **Multi-backend** — plug in Claude, Codex, or any CLI that speaks MCP.
- **Specialist reviewers** — security, architecture, testing, devops, UX — each with a focused lens.
- **Concurrent** — fan out multiple specialist reviews in parallel on a single PR.
- **Secure by design** — HMAC-verified webhooks, hashed prompt logs, read-only daemon.

---

## How it works

```mermaid
sequenceDiagram
    actor Dev as Developer
    participant GH as GitHub
    participant D as agents
    participant AI as AI Backend<br/>(Claude / Codex)

    Dev->>GH: Create issue / open PR
    Dev->>GH: Add label  ai:refine:claude

    GH->>D: Webhook (issues:labeled)
    Note over D: Verify signature,<br/>deduplicate delivery,<br/>parse label, queue event

    D->>AI: Send tailored prompt via CLI
    AI->>GH: Read context via MCP tools
    AI->>GH: Post comment / review via MCP tools
    AI-->>D: Return artifacts JSON

    Note over Dev,GH: AI feedback appears<br/>as a native GitHub comment
```

The daemon is event-driven for label-based workflows and also supports optional scheduled autonomous agents. Webhook events are queued and dispatched asynchronously; autonomous agents run on cron schedules you configure.

---

## Label architecture

Labels are the control plane. Their format tells `agents` **what** to do, **which backend** to use, and **which agent** to activate.

### Issue refinement — `ai:refine`

| Label | Behavior |
|---|---|
| `ai:refine` | Refine with the **default** backend (`claude` if configured, otherwise `codex`) |
| `ai:refine:<backend>` | Refine with a **specific** backend (e.g. `ai:refine:codex`) |

Produces **one structured comment** on the issue covering feasibility, complexity, recommended approach, acceptance criteria, and open questions.

### PR specialist review — `ai:review`

| Label | Behavior |
|---|---|
| `ai:review` | Review with the default backend (`claude` if configured, otherwise `codex`), **all** agents |
| `ai:review:<backend>` | Review with a specific backend, **all** agents concurrently |
| `ai:review:<backend>:<agent>` | Review with a specific backend and **single** agent |
| `ai:review:<backend>:all` | Review with a specific backend, **all** agents concurrently (explicit) |

Available agents are defined in the `agents` section of `config.yaml`. Each agent references one or more `skills` that carry the domain guidance. The example config ships with: `arch-reviewer`, `sec-reviewer`, `test-reviewer`, `ops-reviewer`, and `ux-reviewer`. You can add new agents without code changes — just add an entry to `agents` with the desired `skills`. Any defined agent can be used with any backend via labels.

When using `:all`, every defined agent runs **concurrently** — one review comment per specialist.

### Label parsing rules

```
ai:<workflow>
ai:<workflow>:<backend>
ai:<workflow>:<backend>:<agent>
```

- Labels are **case-insensitive** and trimmed.
- Only the `labeled` action triggers processing (not `unlabeled`).
- Only labels prefixed with `ai:` are considered; all others are silently ignored.
- The trigger label comes from the **webhook event payload** (`payload.label.name`), not the issue's current label list.

### Applying review fixes — branch ownership

When an AI backend creates a PR (e.g. Codex opens a branch from an issue), **that same backend must apply any subsequent fixes** to the branch. This is because the backend that created the branch owns the local working context — a different backend cannot safely commit to it.

After a specialist review surfaces actionable feedback, **@mention the original backend** in the review comments you want addressed:

- If **Codex** opened the PR → mention `@codex` in the review comment.
- If **Claude** opened the PR → mention `@claude` in the review comment.

This ensures the correct backend picks up the fix request and commits the amendments to its own branch.

> **Example flow:**
> 1. Codex opens a PR with the implementation.
> 2. Label the PR with `ai:review:claude` → Claude reviews the PR.
> 3. Claude leaves review comments with suggested fixes.
> 4. Reply to the relevant review comments mentioning `@codex` → Codex applies the fixes to its branch.

---

## Requirements

| Dependency | Purpose |
|---|---|
| **Go 1.22+** | Build the daemon |
| **GitHub CLI** (`gh`) | Authenticated access to your repos |
| **AI CLI** (Claude Code or Codex) | The actual AI backend, with GitHub MCP server configured |

> **Why is `gh` needed if MCP tools handle GitHub writes?**
> The `agents` daemon never calls `gh` directly — it only spawns the configured AI CLI (`claude` or `codex`) and passes it a prompt. Those CLIs use their built-in GitHub MCP tools to read context and post comments. The MCP tools rely on `gh` under the hood for authentication. So `gh` is an implicit dependency of the AI CLIs, not of the daemon itself. Removing it would silently break GitHub writes even though the daemon never invokes it.

### Setup GitHub CLI

```bash
# Install (macOS)
brew install gh

# Authenticate
gh auth login
```

### Setup Claude Code + GitHub MCP

Follow the official guides:
- [Claude Code setup](https://code.claude.com/docs/en/setup)
- [GitHub MCP server for Claude](https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-claude.md)

### Setup Codex + GitHub MCP

Follow the official guide:
[GitHub MCP server for Codex](https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-codex.md)

---

## Configuration

Copy `config.example.yaml` to `config.yaml` and adapt it to your environment:

```yaml
log:
  level: info    # trace, debug, info, warn, error, fatal
  format: text   # text (human-readable) or json (structured)

http:
  listen_addr: ":8080"
  webhook_path: /webhooks/github
  status_path: /status
  agents_run_path: /agents/run          # POST endpoint for on-demand agent triggers
  webhook_secret_env: GITHUB_WEBHOOK_SECRET
  api_key_env: AGENTS_API_KEY           # Bearer token for POST /agents/run (optional)
  shutdown_timeout_seconds: 15

processor:
  issue_queue_buffer: 256
  pr_queue_buffer: 256
  max_concurrent_agents: 4             # max parallel agent goroutines per PR fan-out

agents_dir: "./agents"                 # base dir for prompt_file paths
memory_dir: "/var/lib/agents/memory"   # persistent memory root for autonomous agents

# Base prompt templates (inline or prompt_file relative to agents_dir)
prompts:
  issue_refinement:
    prompt: |
      Refine issue #{{.Number}} in {{.Repo}}.
      Post exactly one concise GitHub comment and return one JSON object on stdout.
  pr_review:
    prompt: |
      {{.AgentHeading}}
      Review PR #{{.Number}} in {{.Repo}} from the perspective of {{.Agent}}.
      {{template "agent_guidance" .}}
      Post one high-signal review comment and return one JSON object on stdout.
  autonomous:
    prompt: |
      Autonomous run for {{.Repo}} as {{.AgentName}}.
      Focus: {{.Description}}
      Task: {{.Task}}
      Memory file: {{.MemoryPath}}
      Existing memory:
      {{.Memory}}
      {{template "agent_guidance" .}}
      Return one JSON object on stdout.

# Skills — reusable knowledge clusters injected into prompt templates.
# An agent referencing multiple skills receives their guidance merged.
skills:
  - name: architect
    prompt: |
      Focus on architecture boundaries, coupling, extensibility, and maintainability risks.
  - name: security
    prompt: |
      Focus on authn/authz, secrets exposure, injection vectors, and unsafe defaults.
  - name: testing
    prompt: |
      Focus on missing tests, brittle tests, regression coverage, and testability.
  - name: devops
    prompt: |
      Focus on reliability, deployment safety, observability, and operational simplicity.
  - name: ux
    prompt: |
      Focus on clarity, accessibility, copy quality, and user flow friction.

# Agents — actors that compose skills.
# Names are used in labels: ai:review:claude:<agent>
agents:
  - name: arch-reviewer
    skills: [architect]
  - name: sec-reviewer
    skills: [security]
  - name: test-reviewer
    skills: [testing]
  - name: ops-reviewer
    skills: [devops]
  - name: ux-reviewer
    skills: [ux]

ai_backends:
  claude:
    mode: command
    command: claude
    args: ["-p", "--dangerously-skip-permissions"]
    timeout_seconds: 600
    max_prompt_chars: 12000
    redaction_salt_env: LOG_SALT

  codex:
    mode: command
    command: codex
    args: ["exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox"]
    timeout_seconds: 600
    max_prompt_chars: 12000
    redaction_salt_env: LOG_SALT

repos:
  - full_name: "owner/repo"
    enabled: true

autonomous_agents:
  - repo: "owner/repo"   # must also exist in repos[]
    enabled: true
    agents:
      - name: "codebase-scout"
        description: "Architecture sweeps looking for design drift and risky coupling."
        cron: "0 9 * * *"   # standard cron syntax
        backend: "auto"     # auto | claude | codex (default: auto)
        skills: [architect]
        tasks:
          - name: "issues"
            prompt: |
              Scan all open issues and add one succinct comment per issue only
              if this agent has not commented before. Avoid duplicate comments.
          - name: "code"
            prompt: |
              Inspect the codebase for improvements. If changes are large or
              uncertain, open an issue describing them.
```

### Config model

**Skills** define reusable domain guidance (the prompts formerly inlined on agents). **Agents** reference one or more skills by name; their guidance is merged at runtime via the `{{template "agent_guidance" .}}` placeholder in the base prompt templates. Agent names must be unique and are used directly in GitHub labels.

**Autonomous agents** run on a cron schedule. Each defines its own `skills` list and a sequence of `tasks` that run in order on each trigger. Tasks carry their own `prompt` or `prompt_file`. Agent memory is persisted at `<memory_dir>/<agent>/<owner>_<repo>/MEMORY.md` and is read before each task run and updated after.

Each autonomous agent selects its backend with `backend`:
- `auto` (default): use the daemon default (`claude` if configured, otherwise `codex`)
- `claude` / `codex`: force a specific backend

Autonomous agents only run for repositories present under `repos`.

Create a `.env` file in the project root for secrets (loaded automatically):

```
GITHUB_WEBHOOK_SECRET=your-webhook-secret
LOG_SALT=optional-redaction-salt
```

---

## Running

```bash
# Run directly
go run ./cmd/agents -config config.yaml

# Or build first
go build -o agents ./cmd/agents
./agents -config config.yaml
```

### On-demand agent pass

Run one autonomous agent synchronously and exit (useful for testing a new config):

```bash
./agents -config config.yaml --run-agent codebase-scout --repo owner/repo
```

Both `--run-agent` (agent name) and `--repo` (`owner/repo` format) are required together. The daemon loads the config, runs the named agent against the repo, and exits.

### Docker

The project includes a multi-stage Dockerfile that produces a minimal image based on `node:22-alpine`, containing the static Go binary, the AI CLIs (Claude Code, Codex), GitHub CLI, and CA certificates. The container runs as a non-root `agents` user (required by Claude Code's `--dangerously-skip-permissions` flag, which refuses to run as root).

```bash
# Build and start
docker compose up -d

# Rebuild after code changes
docker compose up -d --build

# View logs
docker compose logs -f agents

# Stop
docker compose down
```

The compose file expects:
- `config.yaml` in the project root (mounted read-only at `/etc/agents/config.yaml`)
- `agents/` directory in the project root (mounted read-only at `/etc/agents/agents`) only if you use `prompt_file`
- `.env` in the project root with `GITHUB_WEBHOOK_SECRET` (and optionally `LOG_SALT`)

#### Volume mounts

The container needs access to host CLI configurations to authenticate with AI backends and GitHub:

| Host path | Container path | Purpose |
|---|---|---|
| `agents/` | `/etc/agents/agents` (read-only) | Optional prompt_file source directory (not needed for fully inline prompts) |
| `~/.claude` | `/home/agents/.claude` | Claude Code session data, project settings |
| `~/.claude.json` | `/home/agents/.claude.json` | Claude Code main config (auth, MCP servers) |
| `~/.codex` | `/home/agents/.codex` | Codex configuration |
| `~/.config/gh` | `/home/agents/.config/gh` (read-only) | GitHub CLI auth tokens |

The `agents-memory` Docker volume (mounted at `/var/lib/agents/memory`) is recommended to persist autonomous memory across container restarts.

#### Environment variables

| Variable | Purpose |
|---|---|
| `HOME=/home/agents` | Ensures CLIs find their config under the non-root user's home |
| `GITHUB_WEBHOOK_SECRET` | Webhook signature verification (loaded from `.env`) |
| `GITHUB_PAT_TOKEN` | GitHub personal access token for Codex backend |

#### MCP server configuration

Claude Code stores MCP server configuration **per-project**, keyed by working directory path in `~/.claude.json`. Since the container's working directory is `/`, you must ensure `~/.claude.json` has a project entry for `/` with the MCP servers configured. For example:

```json
{
  "projects": {
    "/": {
      "mcpServers": {
        "github": {
          "type": "http",
          "url": "https://api.githubcopilot.com/mcp",
          "headers": {
            "Authorization": "Bearer <your-github-token>"
          }
        }
      }
    }
  }
}
```

Without this entry, Claude Code inside the container will not find any MCP servers. You can verify with:

```bash
docker exec agents claude mcp list
```

---

## GitHub webhook setup

Once the daemon is running and reachable, register the webhook in each repository you want to monitor:

1. Go to **Settings → Webhooks → Add webhook** in your GitHub repository.
2. Set **Payload URL** to `https://<your-host>/webhooks/github`.
3. Set **Content type** to `application/json`.
4. Set **Secret** to the same value as `GITHUB_WEBHOOK_SECRET`.
5. Under **Which events?**, choose **Let me select individual events** and enable:
   - **Issues**
   - **Pull requests**
6. Make sure **Active** is checked, then click **Add webhook**.

GitHub will send a ping event immediately — the daemon will receive it and log the delivery ID.

## Webhook endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/status` | none | Health check — returns JSON with uptime, queue depths, and agent schedules |
| `POST` | `/webhooks/github` | `X-Hub-Signature-256` HMAC | Webhook receiver |
| `POST` | `/agents/run` | `Authorization: Bearer <key>` | Trigger an autonomous agent on demand (requires `api_key_env`) |

The `/agents/run` endpoint accepts:

```json
{"agent": "codebase-scout", "repo": "owner/repo"}
```

It runs the agent synchronously, blocks until the agent finishes, and returns `200 OK` on success. If `api_key_env` is not configured the endpoint returns `403 Forbidden`.

Duplicate webhook deliveries are automatically suppressed using `X-GitHub-Delivery` with an in-memory TTL cache (configurable via `delivery_ttl_seconds`).

---

## AI runner contract

When `mode: command`, the daemon executes the CLI and sends the prompt via **stdin**. The CLI performs its work through MCP tools and must print a **single JSON object to stdout**:

```json
{
  "summary": "Reviewed PR for security vulnerabilities",
  "artifacts": [
    {
      "type": "pr_review",
      "part_key": "review/claude/security",
      "github_id": "123456",
      "url": "https://github.com/owner/repo/pull/1#pullrequestreview-123456"
    }
  ]
}
```

The daemon uses this metadata for observability, logging, and run summaries.

---

## Security

- **Webhook verification** — all payloads are validated with HMAC SHA-256 (`X-Hub-Signature-256`).
- **Read-only daemon** — `agents` itself never writes to GitHub. All writes go through the AI backend via MCP tools.
- **Prompt redaction** — prompts are never logged in plaintext; only their hash and length are recorded.
- **MCP scoping** — toolsets should be allow-listed to `repos`, `issues`, and `pull_requests`.
- **`--dangerously-skip-permissions`** — required for headless Claude Code operation. Ensure the host environment is trusted.

---

## Logging

Two formats are available via `log.format` in `config.yaml`:

- **`text`** (default) — coloured, human-readable output, good for terminals and development.
- **`json`** — raw JSON lines, good for log aggregation pipelines (Loki, Datadog, etc.).

Every log entry includes `repo`, `issue_number` or `pr_number`, and `component` for easy filtering and tracing.

Example JSON entry:

```json
{"level":"info","component":"workflow_engine","repo":"owner/repo","issue_number":42,"backend":"claude","message":"invoking ai backend for issue refinement"}
```

---

## Testing

```bash
go test ./...
```

---

## Project structure

```
cmd/agents/main.go            # Daemon entry point
internal/
  config/config.go             # YAML config parsing, env var resolution
  ai/                          # Prompt generation + runner contract
  autonomous/                  # Cron scheduler + filesystem-backed agent memory
  workflow/                    # Label parsing, request types, event orchestration
  webhook/                     # HTTP server, signature verification, delivery dedupe
  logging/logging.go           # Structured logger setup (zerolog)
agents/                        # Filesystem prompts for refinement, reviews, autonomous agents
```
