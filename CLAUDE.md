# CLAUDE.md

## Project Overview

**agents** (`agentd`) is a Go daemon that receives GitHub webhook events for issue and PR label updates, then invokes Claude (via CLI with GitHub MCP tools) to post automated issue refinement comments and PR specialist reviews.

## Directory Structure

```
cmd/agentd/main.go          # Daemon entry point: bootstrap, signal handling
internal/
  config/config.go           # YAML config parsing, env var resolution, defaults
  github/client.go           # GitHub REST API client (issues, PRs, comments, files)
  claude/
    runner.go                # Claude CLI subprocess execution
    prompt.go                # Prompt generation for issue_refine and pr_review workflows
  workflow/
    engine.go                # Workflow orchestration (label gating, locking, quota, Claude invocation)
    fingerprint.go           # SHA256-based content fingerprinting for idempotency
  webhook/*                  # HTTP server, GitHub webhook verification/handling, delivery dedupe
  store/
    store.go                 # PostgreSQL data access (repos, work items, runs, artifacts, locks)
    schema.sql               # Database schema (embedded via go:embed)
  logging/logging.go         # zerolog structured logger setup
```

## Build & Run

**Prerequisites:** Go 1.24+

```bash
# Run tests
go test ./...

# Build
go build -o agentd ./cmd/agentd

# Run (requires config.yaml and env vars)
go run ./cmd/agentd -config config.yaml
```

There is no Makefile. Standard `go` toolchain commands are used directly.

## Configuration

Configuration is in YAML (`config.yaml`, see `config.example.yaml` for reference). Secrets are loaded from environment variables referenced by `_env` suffixed fields:

- `DATABASE_URL` - PostgreSQL connection string (via `database.dsn_env`)
- `GITHUB_TOKEN` - GitHub API token (via `github.token_env`)
- `LOG_SALT` - Optional prompt redaction salt (via `claude.redaction_salt_env`)

A `.env` file is automatically loaded at startup if present.

Files excluded from version control (`.gitignore`): `.env`, `config.yaml`, `main` (binary).

## Architecture & Key Patterns

### Polling with Adaptive Backoff

The `poller` package drives the main loop. Each repo has independent state with exponential backoff (doubles interval up to `max_idle_interval_seconds`) when no updates are found, resetting to the base interval when updates occur. Jitter prevents thundering herd.

### Workflow Engine

`workflow.Engine` handles two workflows:

- **`issue_refine`** - Gated by a configurable label (default `ai:refine`). Fetches recent comments, generates fingerprint, invokes Claude to post 1-3 issue comments.
- **`pr_review`** - Gated by label (default `ai:review`), skips drafts. Fetches changed files, generates fingerprint from head SHA + diffs, invokes Claude for multi-specialist review.

Both workflows follow the same pattern:
1. Check label gate
2. Generate content fingerprint for idempotency
3. Create workflow run (unique on `work_item_id + workflow + fingerprint`)
4. Acquire distributed lock (PostgreSQL-based)
5. Enforce hourly/daily quota
6. Invoke Claude subprocess
7. Store artifacts and update status

### Fingerprinting

`workflow/fingerprint.go` produces deterministic SHA256 hashes of issue/PR content. Format: `<type>:<version>:<key>:<hash>`. Content is truncated to `max_fingerprint_bytes` before hashing. Duplicate fingerprints cause the workflow to skip (idempotency).

### AI CLI Integration

The configured backend runner executes the selected AI CLI as a subprocess. Prompts are sent via STDIN; JSON responses are expected on STDOUT. Environment variables (`AI_DAEMON_WORKFLOW`, `AI_DAEMON_REPO`, `AI_DAEMON_NUMBER`, `AI_DAEMON_FINGERPRINT`) are passed to the subprocess. Supports `noop` mode for testing and `command` mode for production.

### Database Layer

`store.Store` uses `database/sql` with the `pgx` driver. Schema is embedded via `go:embed` and auto-migrated on startup when `auto_migrate: true`. All operations use parameterized queries. Idempotency is enforced through `ON CONFLICT` clauses. Distributed locking uses a `locks` table with TTL-based expiration.

### Structured Logging

All packages use `zerolog` with component-scoped loggers (`logger.With().Str("component", "...").Logger()`). Prompts are never logged directly - only their SHA256 hash (with optional salt) and character count.

## Code Conventions

- **Go standard layout**: `cmd/` for entry points, `internal/` for private packages
- **Limited interfaces for boundary abstractions**: Concrete struct types are preferred, with targeted interfaces where backend selection requires it (for example, `ai.Runner`)
- **Constructor pattern**: `New*()` or `Open()` functions return initialized structs
- **Error wrapping**: All errors use `fmt.Errorf("context: %w", err)` for wrapping
- **Sentinel errors**: Package-level `var errQuotaExceeded = errors.New(...)` for control flow
- **Configuration defaults**: Constants defined at package level (e.g., `defaultPollIntervalSeconds = 60`), applied in `applyDefaults()`
- **Context propagation**: All I/O functions accept `context.Context` as first parameter
- **Explicit cleanup**: `defer` for unlocking, connection closing, and context cancellation
- **No ORM**: Raw SQL with parameterized queries throughout `store.go`
- **Embedded SQL schema**: `schema.sql` loaded via `//go:embed`

## Testing

Tests use Go's standard `testing` package. No external test frameworks or mocks libraries.

```bash
go test ./...
```

Test files:
- `internal/claude/prompt_test.go` - Verifies prompt generation includes correct markers and fingerprints
- `internal/workflow/fingerprint_test.go` - Verifies fingerprint stability and change detection

## Database Schema

Five tables: `repos`, `work_items`, `workflow_runs`, `posted_artifacts`, `locks`. See `internal/store/schema.sql` for full DDL. Key uniqueness constraints enforce idempotency:

- `workflow_runs(work_item_id, workflow, fingerprint)` - prevents duplicate runs
- `posted_artifacts(workflow_run_id, artifact_type, part_key)` - prevents duplicate posts
- `work_items(repo_full_name, kind, number)` - one record per issue/PR

## Important Defaults

| Setting | Default |
|---|---|
| Poll interval per repo | 60s |
| Max items per poll | 200 |
| Max idle interval (backoff cap) | 600s |
| Claude timeout | 600s |
| Max prompt chars | 12,000 |
| Max runs per hour per item | 5 |
| Max runs per day per item | 20 |
| Max posts per run | 10 |
| Max fingerprint bytes | 20,000 |

## Security Notes

- The daemon is read-only against GitHub. All writes (comments, reviews) are delegated to Claude via MCP tools.
- Secrets are loaded from environment variables, never stored in config files.
- Prompts are hashed (with optional salt) in logs, never logged in plaintext.
- Distributed locking prevents concurrent processing of the same work item.
- GitHub rate limits are detected and surfaced as `RateLimitError` with retry-after duration.
