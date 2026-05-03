# Reactive inter-agent dispatch

Agents can invoke each other at runtime. When an agent's AI run returns a `dispatch[]` field in its JSON response, the daemon validates and enqueues a synthetic `agent.dispatch` event for each entry. The target agent then runs with the full event payload as its runtime context.

## Agent response contract

```json
{
  "summary": "Reviewed PR; escalating to sec-reviewer for crypto usage",
  "artifacts": [],
  "dispatch": [
    {
      "agent": "sec-reviewer",
      "number": 42,
      "reason": "PR introduces custom crypto primitives; needs a security review"
    }
  ]
}
```

- `agent`: name of the target agent (must be in the originator's `can_dispatch` list and have `allow_dispatch: true`).
- `number`: issue/PR number to associate with the dispatched run. If omitted, the originating event's number is used.
- `reason`: human-readable rationale, included in the target agent's prompt context.

## Runtime context for dispatched agents

The dispatched agent receives an `agent.dispatch` event with these payload fields:

| Field | Value |
|-------|-------|
| `target_agent` | Name of the agent being invoked (this agent) |
| `reason` | Reason string supplied by the originator |
| `root_event_id` | ID of the original triggering event (stable across the full chain) |
| `dispatch_depth` | How many hops deep in the chain this invocation is |
| `invoked_by` | Name of the agent that dispatched this run |
| `parent_span_id` | Span ID of the originating agent's run (used by trace stitching to link the dispatch chain) |

## Safety limits (`daemon.processor.dispatch`)

| Field | Default | Meaning |
|-------|---------|----------|
| `max_depth` | 3 | Maximum dispatch chain length. Requests that would exceed this are dropped with a warning. |
| `max_fanout` | 4 | Maximum number of dispatches a single agent run may enqueue. Additional requests are dropped. |
| `dedup_window_seconds` | 300 | Suppress duplicate `(target, repo, number)` dispatch requests within this window (seconds). |

All three fields must be positive integers; the daemon rejects non-positive values at startup.

## Dispatch flow

```
Agent A runs -> returns dispatch[{agent:"B", number:42, reason:"..."}]
    |
    v
Dispatcher checks:
  1. B is in A's can_dispatch list
  2. B has allow_dispatch: true
  3. depth <= max_depth, fanout <= max_fanout
  4. (B, repo, 42) not seen within dedup_window_seconds
    |
    v
agent.dispatch event enqueued -> Agent B runs with full payload
```

Dispatch chains work across both event-driven and cron and event-driven paths, and the shared dedup store prevents a cron-triggered run and a near-simultaneous dispatch from running the same target twice within the window.

## Config wiring

```yaml
agents:
  - name: pr-reviewer
    backend: claude
    can_dispatch: [sec-reviewer]       # whitelist of targets
    prompt: |
      Review the pull request for correctness and escalate specialist concerns when needed.

  - name: sec-reviewer
    description: "Deep-dive security reviewer"
    backend: claude
    allow_dispatch: true               # opt-in to being dispatched
    prompt: |
      Review the change for security risks and unsafe assumptions.
```

## UI wiring editor

The **Graph** page in the web dashboard (`/ui/`) has an "Edit wiring" toggle. When active:

- **Add a connection**: drag from any agent node to another. The daemon writes the source agent's `can_dispatch` list and enables `allow_dispatch` on the target via `PATCH /agents`.
- **Remove a connection**: click an existing edge to open a confirmation modal. The daemon removes the target from the source agent's `can_dispatch` list; the target's `allow_dispatch` flag is left alone, since other agents may still dispatch to it.

Self-dispatch and duplicate edges are rejected before any network call. Config-level constraints (`description` required on dispatch targets, no self-reference) still apply, the UI enforces them before writing.
