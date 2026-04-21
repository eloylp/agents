# Contributing

**agents** is built by its own agent fleet. You bring the ideas — the agents bring the implementation.

This project uses autonomous AI agents for all code changes: a coder agent implements features and fixes, a pr-reviewer validates quality, a refactorer improves code structure, and a docs-writer keeps documentation current. This isn't a gimmick — it's how we validate the product we're building.

## How to contribute

### 1. Open an issue

All contributions start as issues. Describe what you want — a bug fix, a feature, an architectural improvement, a question. Use the `discussing` label so the agent fleet doesn't pick it up prematurely.

**What makes a great issue:**
- **Clear problem statement**: what's wrong or missing, and why it matters.
- **Concrete acceptance criteria**: how would you know the fix/feature is done?
- **Context**: link to the code, the config, the log line, the doc section that's relevant.
- **Scope**: one issue = one thing. Split large ideas into smaller issues.

**What to avoid:**
- Implementation prescriptions ("change line 42 of engine.go to..."). Describe the *what* and *why*; the coder agent decides the *how*.
- Issues that require access to private infrastructure, credentials, or internal context.

### 2. Discussion + triage

A maintainer reviews your issue. They may:
- **Ask questions** to clarify scope or intent.
- **Accept it**: remove `discussing`, add a priority label. The coder agent picks it up on its next run.
- **Defer it**: keep `discussing` for future consideration.
- **Close it**: if it's out of scope, already addressed, or not aligned with the project direction.

### 3. The agents take over

Once an issue is accepted:
1. **Coder** picks it up, reads the issue + comments, implements the fix, opens a PR.
2. **PR-reviewer** reviews the PR for quality, testing, and correctness.
3. **Refactorer** may follow up with code cleanup if needed.
4. **Docs-writer** updates documentation if the change affects user-facing behavior.
5. A maintainer merges when the fleet's work is satisfactory.

You can follow along: every agent action is a visible GitHub comment, PR, or review. The process is transparent.

### 4. Feedback loop

If the agent's implementation misses the mark, comment on the PR or the original issue. The coder reads comments on its next pass and iterates. This is normal — agents sometimes need guidance, just like human contributors do.

## What about pull requests?

**We don't accept code PRs for features or fixes.** The agent fleet handles implementation. This ensures:
- Consistent code style and architecture (enforced by the agents' skills and prompts).
- The project dogfoods its own product — if the agents can't implement something, that's a bug in the product, not a reason to bypass it.
- Every change flows through the same quality pipeline.

**Exceptions:**
- **Documentation typos and small fixes**: if you spot a typo, a broken link, or a one-line doc fix, PRs are welcome. No need to route a comma through the agent pipeline.
- **Security patches**: see [SECURITY.md](SECURITY.md) for responsible disclosure. Critical fixes may bypass the normal flow.

## Labels that matter

| Label | Meaning |
|---|---|
| `discussing` | Issue is under discussion — agents will NOT pick it up. Safe for brainstorming. |
| `high priority` | Accepted and urgent — coder picks it up before other issues. |
| `bug` | Something is broken. Coder prioritizes bugs after high-priority items. |
| *(no label)* | Accepted, normal priority. Coder works through these in order. |

## Code of conduct

Be respectful in issues and discussions. The agents don't have feelings, but the maintainers do. Constructive feedback on agent output is welcome — "this PR misses the point because..." is useful; "your bot is stupid" is not.

## Questions?

Open an issue with the `discussing` label. We're happy to help.
