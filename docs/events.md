# Supported events

The `events:` field in repo bindings accepts any of the following GitHub event kinds. Each event delivers a `## Runtime context` block into the agent's prompt with `Event`, `Actor` (the GitHub login that triggered it), an issue/PR number where applicable, and the payload fields listed below.

## Event reference

| Kind | When | Payload fields |
|------|------|----------------|
| `issues.labeled` | Issue receives any label | `label` |
| `issues.opened` | Issue opened | `title`, `body` |
| `issues.edited` | Issue body or title edited | `title`, `body` |
| `issues.reopened` | Issue reopened | `title`, `body` |
| `issues.closed` | Issue closed | `title`, `body` |
| `pull_request.labeled` | PR receives any label (draft PRs are skipped) | `label` |
| `pull_request.opened` | PR opened | `title`, `draft` |
| `pull_request.synchronize` | New commit pushed to PR branch | `title`, `draft` |
| `pull_request.ready_for_review` | Draft PR marked ready | `title`, `draft` |
| `pull_request.closed` | PR closed or merged | `title`, `draft`, `merged` (`true` when PR was merged, `false` when closed without merge) |
| `issue_comment.created` | Comment posted on an issue or PR | `body` |
| `pull_request_review.submitted` | Formal GitHub review submitted | `state`, `body` |
| `pull_request_review_comment.created` | Inline review comment posted on a PR diff | `body` |
| `push` | Commit pushed to a branch | `ref` (e.g. `refs/heads/main`), `head_sha` |
| `agents.run` | On-demand trigger via `POST /run` or MCP `trigger_agent` | `target_agent` |
| `agent.dispatch` | Another agent dispatched this agent | `target_agent`, `reason`, `root_event_id`, `dispatch_depth`, `invoked_by` |

## Event lifecycle

Events flow through a durable, in-process queue:

1. **Push.** A producer (webhook handler, cron tick, dispatch, on-demand trigger) calls `PushEvent`. The event is serialised to JSON and inserted into the SQLite `event_queue` table with an `enqueued_at` stamp; the new row id is then sent on the in-memory channel as a wake-up signal. If the channel buffer is full or the producer's context is cancelled, the just-inserted row is rolled back so the next startup doesn't replay an event the runtime never accepted.
2. **Worker pick-up.** A worker reads the `QueuedEvent` off the channel, stamps `started_at` on the row, and runs the agent.
3. **Completion.** When `HandleEvent` returns (success or failure) the worker stamps `completed_at`. A failed run is still considered "done" from the queue's perspective — agent failures surface through `/traces` and `/events`, not by replaying the same event forever.
4. **Crash recovery.** At startup the daemon scans for rows whose `completed_at` is still `NULL` and pushes each one back onto the channel via `ReplayQueued`. Events buffered at shutdown, or runs interrupted mid-prompt, get a second chance instead of vanishing. Replay relies on agent idempotency — orchestrators (Docker, Kubernetes) `SIGKILL` after ~30 seconds, so an in-flight prompt may be killed mid-execution and re-run from scratch.
5. **Retention.** A consumer-tier cleanup loop ticks hourly and deletes rows whose `completed_at` is older than 7 days. The table stays bounded regardless of throughput.

The `/queue` REST surface, the matching MCP tools (`list_queue_events`, `delete_queue_event`, `retry_queue_event`), and the UI's Queue page all read and write through this same table.

## Event rules

- **`push` scope:** only branch pushes fire the event. Tag pushes, branch deletions, and pushes to non-`refs/heads/` refs are silently dropped. The agent receives the branch ref and the resulting head SHA. There is no PR number in the context.
- `issues.*` events that originate from a PR-backed GitHub issue are dropped; the corresponding `pull_request.*` event covers them instead.
- `pull_request.labeled` events on draft PRs are dropped at the webhook boundary. Use `events: ["pull_request.ready_for_review"]` to act when a draft is marked ready.
- Unknown event kinds are rejected at config load time with a clear error listing the supported set.

## Example binding

```yaml
repos:
  - name: "owner/repo"
    enabled: true
    use:
      # React to every new issue comment (issues and PRs alike)
      - agent: coder
        events: ["issue_comment.created"]

      # React to new commits pushed to any branch
      - agent: sec-reviewer
        events: ["push"]

      # Multiple event kinds in one binding
      - agent: pr-reviewer
        events: ["pull_request.opened", "pull_request.synchronize"]
```
