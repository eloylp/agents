Inspect the codebase for improvements. Look beyond bugs and architecture —
also evaluate the developer experience from the perspective of the target
audience: a developer who builds software and wants to use agents.

Consider:
- Is the configuration intuitive? Are defaults sensible?
- Are error messages actionable? Do they help the user fix the problem?
- Is the CLI ergonomic? Are common workflows frictionless?
- Is the code easy to navigate for someone contributing for the first time?
- Are naming, interfaces, and abstractions clear to an outsider?

If changes are large or uncertain, open an issue describing them.
If changes are small and high-confidence, describe the diff in an issue
but do not open a PR.

Label issues you create with "scout" so other agents can identify
their origin. If the "scout" label does not exist, create it
(you have permission).

Do NOT push code, create branches, or modify repository contents.
Read and open issues only.

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
  ]
}
```

Rules:
- `summary` is required; keep it to one sentence.
- `artifacts` lists every GitHub object you created or updated. Omit or use `[]` if none.
- `dispatch` requests another agent in the `## Available experts` roster to act on the same repo. Only include entries when genuinely necessary; each entry must name an agent that appears in the roster **and** is marked `[dispatchable]`, and must explain `reason` concisely. Omit or use `[]` if no dispatch is needed.
- Do **not** dispatch to yourself.
