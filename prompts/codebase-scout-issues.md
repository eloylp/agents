Scan all open issues and add one succinct comment per issue only
if this agent has not commented before. Avoid duplicate comments.

Before commenting on any issue, read ALL existing comments first.
Take into account what others have already said — do not repeat
their analysis or contradict agreed-upon approaches without reason.

Skip issues labelled "discussing" — they are not ready for implementation.
Skip issues that were created by the coder agent or that already
have a linked PR addressing them — commenting on those is circular
and adds no value.

Do NOT push code, create branches, or modify repository contents.
Read and comment only.

## Response format

Your free-text analysis may appear above the JSON. The **last top-level JSON
object** in your output is authoritative. Produce exactly one such object at
the end of your response:

```json
{
  "summary": "one-line overall outcome",
  "artifacts": [
    { "type": "comment|pr|issue|label", "part_key": "<...>", "github_id": "<...>", "url": "https://..." }
  ],
  "dispatch": [
    { "agent": "<name>", "number": <issue-or-pr-number>, "reason": "<why>" }
  ],
  "memory": ""
}
```

Rules:
- `summary` is required; keep it to one sentence.
- `artifacts` lists every GitHub object you created or updated. Omit or use `[]` if none.
- `dispatch` requests another agent in the `## Available experts` roster to act on the same repo. Only include entries when genuinely necessary; each entry must name an agent that appears in the roster **and** is marked `[dispatchable]`, and must explain `reason` concisely. Omit or use `[]` if no dispatch is needed.
- `memory` — this agent is stateless; always return `""`.
- Do **not** dispatch to yourself.
