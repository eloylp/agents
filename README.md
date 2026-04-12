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

Available agents are defined in the `agents` section of `config.yaml`. The default configuration ships with: `architect`, `security`, `testing`, `devops`, and `ux`. You can add new agents without code changes — just add an entry to `agents` with a `prompt_file` or inline `prompt`. Any defined agent can be used with any backend via labels.

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
  level: info           # debug | info | warn | error
  format: text          # text (human-readable, default) | json (raw JSON lines)

http:
  listen_addr: ":8080"
  webhook_path: /webhooks/github
  status_path: /status
  webhook_secret_env: GITHUB_WEBHOOK_SECRET   # env var name (not the secret itself)
  shutdown_timeout_seconds: 15

processor:
  issue_queue_buffer: 256
  pr_queue_buffer: 256

agents_dir: "./agents"  # optional prompt_file base dir + autonomous memory root
allow_autonomous_prs: false  # require explicit opt-in for autonomous PR creation

# Inline-first prompt configuration.
# You can replace any `prompt` with `prompt_file` (relative to agents_dir) if preferred.
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
  autonomous:
    prompt: |
      Autonomous run for {{.Repo}} as {{.AgentName}}.
      Task: {{.Task}}
      {{template "agent_guidance" .}}
  autonomous_issue_task:
    prompt: |
      Scan all open issues and add one succinct comment per issue only if this agent has not commented before. Avoid duplicate comments.
  autonomous_code_task:
    prompt: |
      Inspect the codebase for improvements. If changes are large or uncertain, open an issue describing them. If changes are small and high-confidence, open a PR directly.
  autonomous_code_task_no_prs:
    prompt: |
      Inspect the codebase for improvements. If changes are large or uncertain, open an issue describing them. If changes are small and high-confidence, describe the diff in an issue but do not open a PR.

agents:
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
    args: ["-p"]
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
      - name: "architect"                # must reference a defined agent
        description: "Architecture sweeps looking for design drift and risky coupling."
        cron: "0 9 * * *"   # standard cron syntax
        backend: "auto"     # auto | claude | codex (default: auto)
```

Agents are defined in the top-level `agents` section. Inline `prompt` is the recommended default for fast iteration. `prompt_file` (relative to `agents_dir`) is optional for longer shared prompts. Each agent must provide exactly one of `prompt` or `prompt_file`, and agent names must be unique.

Base prompt templates and autonomous task prompts follow the same inline-first pattern; each entry supports either `prompt` or `prompt_file`.

Any defined agent can be used with any backend via labels — there is no per-backend agent allowlist. Autonomous agents must reference agents defined in the top-level `agents` list.

Each autonomous agent can select its backend independently with `backend`:
- `auto` (default): use the daemon default (`claude` if configured, otherwise `codex`)
- `claude`: force Claude backend for that agent
- `codex`: force Codex backend for that agent

Autonomous agents only run for repositories that are also present and enabled under `repos`. Each scheduled run performs two parallel passes:
- Sweep open issues and add a single comment only if this agent has not commented yet.
- Sweep the codebase for improvements; open an issue for large/uncertain work or open a PR when the change is small and high-confidence (PR creation is gated by `allow_autonomous_prs`, default `false`).

Agent memory is stored per repo at `agents_dir/autonomous/<agent>/<owner_repo>/MEMORY.md` (repo slashes are replaced with `_`). The daemon serializes writes so concurrent runs cannot corrupt memory; the file is created automatically if missing.

A default prompt and memory layout is included:

```
agents/
├── autonomous/
│   ├── base/PROMPT.md           # autonomous base template (default)
│   └── owner_repo/
│       └── MEMORY.md            # created on first run
├── guidance/
│   ├── architect.md             # agent prompt files
│   ├── devops.md
│   ├── security.md
│   ├── testing.md
│   └── ux.md
├── issue_refinement_prompts/
│   └── PROMPT.md                # issue refinement base template (default)
└── pr_review_prompts/
    └── base/PROMPT.md           # PR review base template (default)
```

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

| Method | Path | Description |
|---|---|---|
| `GET` | `/status` | Health check — returns `ok` |
| `POST` | `/webhooks/github` | Webhook receiver with `X-Hub-Signature-256` verification |

Duplicate deliveries are automatically suppressed using `X-GitHub-Delivery` with an in-memory TTL cache.

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
