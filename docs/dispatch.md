# Reactive inter-agent dispatch

Agents can invoke each other at runtime. When an agent's AI run returns a `dispatch[]` field in its JSON response, the daemon validates and enqueues a synthetic `agent.dispatch` event for each entry. The target agent then runs with the full event payload as its runtime context.

Repo bindings decide how agents start independently from labels, GitHub events, cron, or manual runs. Dispatch wiring decides which already-running agents may call other agents. A dispatched target does not need its own binding on the repo; it inherits the source run's repo and issue/PR context.

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

- `agent`: name of the target agent (must be in the originator's `can_dispatch` list and have `allow_dispatch: true`; all agents have a required `description` for identification and routing context).
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

## Safety limits

| Env var | Default | Meaning |
|-------|---------|----------|
| `AGENTS_DISPATCH_MAX_DEPTH` | 3 | Maximum dispatch chain length. Requests that would exceed this are dropped with a warning. |
| `AGENTS_DISPATCH_MAX_FANOUT` | 4 | Maximum number of dispatches a single agent run may enqueue. Additional requests are dropped. |
| `AGENTS_DISPATCH_DEDUP_WINDOW_SECONDS` | 300 | Suppress duplicate `(target, repo, number)` dispatch requests within this window (seconds). |

All three fields must be positive integers; the daemon rejects non-positive values at startup.

## Dispatch flow

```
Agent A runs -> returns dispatch[{agent:"B", number:42, reason:"..."}]
    |
    v
Dispatcher checks:
  1. B is in A's can_dispatch list
  2. B has allow_dispatch: true
  3. B has a non-empty description
  4. depth <= max_depth, fanout <= max_fanout
  5. (B, repo, 42) not seen within dedup_window_seconds
    |
    v
agent.dispatch event enqueued -> Agent B runs with inherited repo context and full payload
```

Dispatch chains work across both event-driven and cron paths, and the shared dedup store prevents a cron-triggered run and a near-simultaneous dispatch from running the same target twice within the window.

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

The **Graph** page in the web dashboard (`/ui/`) has an "Edit wiring" toggle and a right-side Agent editor. When active:

- **Add a connection**: drag from any agent node to another. The dashboard writes the source agent's `can_dispatch` list and enables `allow_dispatch` on the target through the normal agent save surface.
- **Remove a connection**: click an existing edge to open it in the Agent editor, then remove the wiring. The daemon removes the target from the source agent's `can_dispatch` list; the target's `allow_dispatch` flag is left alone, since other agents may still dispatch to it.

Self-dispatch and duplicate edges are rejected before any network call. Config-level constraints (agent `description` is required, targets must opt in with `allow_dispatch`, no self-reference) still apply, the UI enforces them before writing.

Creating a dispatch edge is enough to authorize runtime dispatch when the target opts in. Do not add fake repo bindings for targets that should only run when another agent dispatches them. Repo triggers are managed separately from the agent node panel and still use the normal repo binding API.
