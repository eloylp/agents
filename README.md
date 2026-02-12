# agents

Claude-powered issue refinement and PR specialist review daemon for GitHub repositories.

## Overview

This Go daemon polls GitHub repositories for issue/PR updates (no webhooks) and launches Claude sessions that use the GitHub MCP server to read context and post feedback. The daemon itself only performs minimal GitHub REST polling for detection and fingerprinting; all detailed reads and writes are delegated to Claude via MCP tools.

### Key features

- Polls issues and PRs with adaptive intervals and jitter.
- Maintains idempotency with fingerprints and persisted workflow runs.
- Persists state in Postgres (repos, work items, runs, posted artifacts).
- Produces structured JSON logs with correlation metadata.
- Supports issue refinement and PR specialist review workflows.

## Requirements

- Go 1.22+
- Postgres 14+
- GitHub token with read access to the monitored repositories
- Claude runner capable of using the GitHub MCP server (CLI or wrapper command)

## Configuration

Create `config.yaml` (or pass `-config`):

```yaml
log:
  level: info

database:
  dsn_env: DATABASE_URL
  auto_migrate: true

github:
  token_env: GITHUB_TOKEN
  api_base_url: https://api.github.com

poller:
  per_page: 50
  max_items_per_poll: 200
  issue_label: "ai:refine"
  pr_label: "ai:review"
  max_idle_interval_seconds: 600
  jitter_seconds: 5
  comment_fingerprint_limit: 5
  file_fingerprint_limit: 50
  max_fingerprint_bytes: 20000
  max_posts_per_run: 10
  max_runs_per_hour: 5
  max_runs_per_day: 20

claude:
  mode: command
  command: claude
  args:
    - "--mcp"
    - "github"
  timeout_seconds: 600
  max_prompt_chars: 12000
  redaction_salt_env: LOG_SALT

repos:
  - full_name: "owner/repo"
    enabled: true
    poll_interval_seconds: 60
    issue_label: "ai:refine"   # optional override
    pr_label: "ai:review"      # optional override
```

### Claude runner contract

When `claude.mode=command`, the daemon executes the configured command and sends the prompt via STDIN. The command should output JSON to STDOUT:

```json
{
  "summary": "optional run summary",
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

The daemon persists these artifacts for idempotency. Use GitHub MCP toolsets (`repos`, `issues`, `pull_requests`) inside Claude to read context and post comments/reviews.

## Database schema

The schema matches the issue specification and is available at `migrations/001_init.sql`. Set `database.auto_migrate: true` to let the daemon apply it on startup.

## Running

```bash
export DATABASE_URL=postgres://user:pass@localhost:5432/agents?sslmode=disable
export GITHUB_TOKEN=ghp_...
export LOG_SALT=optional-salt

go run ./cmd/agentd -config config.yaml
```

## Logging

Logs are JSON with correlation fields such as `repo`, `issue_number`/`pr_number`, and `fingerprint`. Prompts are never logged directly; only their hash/length is recorded.

## Workflow summary

### Issue refinement

Trigger: issue created/edited (optionally label-gated). Claude posts 1–3 issue comments with a deterministic footer marker:

```
<!-- ai-daemon:issue-refine v1; fingerprint=...; part=1/3 -->
```

### PR specialist review

Trigger: PR opened/updated/ready-for-review (optionally label-gated). Claude posts a review summary and inline comments with suggestion blocks where possible. The top-level review includes:

```
<!-- ai-daemon:pr-review v1; fingerprint=... -->
```

## Security

- No webhook handling or GitHub write access in Go.
- MCP toolsets should be allow-listed to `repos`, `issues`, and `pull_requests`.
- Prompts are hashed in logs; secrets are never logged.
