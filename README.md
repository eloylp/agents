# agents

A Go daemon that polls GitHub repositories for issues and pull requests, then launches AI CLI agents ([Claude Code](https://docs.anthropic.com/en/docs/claude-code), Codex, etc.) selected dynamically from labels to provide automated feedback via the GitHub MCP server. No webhooks required.

## Available workflows

### Issue refinement (`ai:refine` labels)

Labels:
- `ai:refine`
- `ai:refine:<agent>`

Each matched agent posts exactly one structured issue comment.

### PR specialist review (`ai:review` labels)

Labels:
- `ai:review`
- `ai:review:<agent>:<role>`
- `ai:review:<agent>:all`

Roles: `architect`, `security`, `testing`, `devops`, `ux`.

`all` expands to all roles configured for that agent and runs them concurrently. Each role counts as one run against quota limits.

## Flow

```mermaid
sequenceDiagram
    actor User
    participant GH as GitHub
    participant Daemon
    participant Claude

    User->>GH: Create issue
    User->>GH: Add "ai:refine:claude" label

    Daemon->>GH: Poll detects issue update
    Daemon->>Claude: Send issue refinement prompt
    Claude->>GH: Read issue context (MCP)
    Claude->>GH: Post issue comments
    Claude-->>Daemon: Return artifacts JSON

    User->>GH: Assign issue to Copilot
    Note over GH: Copilot creates PR

    User->>GH: Add "ai:review:claude:all" label

    Daemon->>GH: Poll detects PR update
    Daemon->>Claude: Send PR review prompt
    Claude->>GH: Read PR diff & files (MCP)
    Claude->>GH: Submit review with suggestions
    Claude-->>Daemon: Return artifacts JSON
```

The daemon only performs minimal GitHub REST polling for detection and fingerprinting. All detailed reads and writes (issue comments, PR reviews) are delegated to Claude via MCP tools.

## Requirements

- **Go** 1.22+
- **PostgreSQL** 14+
- **GitHub CLI** (`gh`) authenticated with access to monitored repositories
- **AI CLI backend**: Claude Code CLI or Codex CLI, with the GitHub MCP server configured
- **GitHub token** with read access to the monitored repositories

### Setting up the GitHub CLI

Install and authenticate:

```bash
# Install (macOS)
brew install gh

# Authenticate
gh auth login
```

### Setting up Claude Code CLI with the GitHub MCP server

Install Claude Code and add the GitHub MCP server:

```bash
# Install Claude Code
npm install -g @anthropic-ai/claude-code

# Add the GitHub MCP server (uses the gh CLI under the hood)
claude mcp add github -- gh copilot mcp
```

Verify the server is registered:

```bash
claude mcp list
```

### Setting up Codex CLI with the GitHub MCP server

Follow the official Codex + GitHub MCP setup guide:

https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-codex.md

## Configuration

Copy `config.example.yaml` to `config.yaml` and adjust:

```yaml
log:
  level: info

database:
  dsn_env: DATABASE_URL       # env var containing the Postgres DSN
  auto_migrate: true           # apply schema on startup

github:
  token_env: GITHUB_TOKEN      # env var containing the GitHub token
  api_base_url: https://api.github.com

poller:
  per_page: 50                 # items per GitHub API page
  max_items_per_poll: 200      # max items fetched per poll cycle
  max_idle_interval_seconds: 600
  jitter_seconds: 5
  comment_fingerprint_limit: 5
  file_fingerprint_limit: 50
  max_fingerprint_bytes: 20000
  max_posts_per_run: 10
  max_runs_per_hour: 5         # per work item (across all roles/agents)
  max_runs_per_day: 20         # per work item (across all roles/agents)

default_agent: claude

agents:
  claude:
    mode: command
    command: claude
    args:
      - "-p"                              # print mode (non-interactive)
      - "--dangerously-skip-permissions"  # required for headless operation
    timeout_seconds: 600
    max_prompt_chars: 12000
    redaction_salt_env: LOG_SALT
    roles: [architect, security, testing, devops, ux]
  openai:
    mode: command
    command: codex
    args:
      - "-p"
    timeout_seconds: 600
    max_prompt_chars: 12000
    redaction_salt_env: LOG_SALT
    roles: [architect, security, testing, devops, ux]

repos:
  - full_name: "owner/repo"
    enabled: true
    poll_interval_seconds: 60
```

Backward compatibility: if `agents` is not set, legacy `ai_backend` + `claude`/`openai` blocks are still accepted and auto-migrated at load time.

You can also create a `.env` file in the project root. The daemon loads it automatically on startup:

```
DATABASE_URL=postgresql://user:pass@localhost:5432/agents
GITHUB_TOKEN=ghp_...
LOG_SALT=optional-salt
```

## Running

```bash
go run ./cmd/agentd -config config.yaml
```

Or build and run:

```bash
go build -o agentd ./cmd/agentd
./agentd -config config.yaml
```

## AI runner contract

When `agents.<name>.mode=command`, the daemon executes the configured command and sends the prompt via STDIN. After performing actions through MCP tools, the command must output a single JSON object to STDOUT:

```json
{
  "summary": "one-line summary of what was done",
  "artifacts": [
    {
      "type": "issue_comment",
      "part_key": "issue/part1",
      "github_id": "123456",
      "url": "https://github.com/..."
    }
  ]
}
```

The daemon persists these artifacts for idempotency (same fingerprint = no duplicate run).

## Database

The schema is embedded in the binary at `internal/store/schema.sql`. Set `database.auto_migrate: true` to let the daemon apply it on startup. Tables:

- **repos** -- registered repositories and poll cursors
- **work_items** -- individual issues/PRs being tracked
- **workflow_runs** -- execution records, deduplicated by content fingerprint
- **posted_artifacts** -- outputs produced by each run (idempotency guard)
- **locks** -- pessimistic locks preventing concurrent processing of the same work item

## Logging

Structured JSON logs with correlation fields: `repo`, `issue_number`/`pr_number`, `fingerprint`, and `component`. Prompts are never logged directly; only their hash and length are recorded.

## Security

- The Go daemon has **read-only** GitHub access (polling only). All writes go through Claude via MCP.
- MCP toolsets should be allow-listed to `repos`, `issues`, and `pull_requests`.
- `--dangerously-skip-permissions` is required for headless operation. Ensure the host environment is trusted.
- Prompts are hashed in logs; secrets are never logged.
