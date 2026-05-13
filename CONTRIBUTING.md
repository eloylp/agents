# Contributing

Contributions of any kind are welcome: issues, pull requests, doc fixes, prompts, ideas. **agents** is unusual in that an autonomous AI fleet runs alongside human contributors and can take on triaged work, but the project does not require you to route through that fleet. You can bring code yourself.

## How it works in one paragraph

Anyone can open an issue or a pull request. Nothing happens automatically: by default, AI agents do not touch your contribution. A maintainer triages each issue and PR. If the maintainer decides the agent fleet should help, they apply the `ai ready` label. Without that label, your contribution is reviewed and merged by humans like any other open-source project.

## Labels that drive the flow

| Label | Meaning |
|---|---|
| `ai ready` | Maintainer signal: the agent fleet may work on this issue or PR. Without it, agents skip the item entirely. Only repo admins can apply this label. |
| `human review ready` | The pr-reviewer agent has approved an AI-implemented PR. The PR is ready for a human merge decision. |
| `high priority` | Accepted and urgent. The coder agent picks it up before other `ai ready` issues. |
| `bug` | Something is broken. The coder agent prioritises bugs after high-priority items. |

## Issue path: bring an idea

1. Open an issue describing what you want: a bug fix, a feature, an architectural improvement, a question.
2. A maintainer reads it. They may ask clarifying questions, request scope changes, or close it as out of scope.
3. If accepted for the agent path, the maintainer applies the `ai ready` label (optionally with `high priority` or `bug`). The coder agent picks it up on its next run.
4. The coder opens a PR that closes your issue. The pr-reviewer agent reviews it. On approval, pr-reviewer applies `human review ready`.
5. A maintainer merges when the AI's work is satisfactory.

If you'd rather implement it yourself, just say so in the issue or open a PR directly (see below).

### What makes a great issue

- **Clear problem statement**: what's wrong or missing, and why it matters.
- **Concrete acceptance criteria**: how would you know the fix or feature is done?
- **Context**: link to the code, the config, the log line, or the doc section that's relevant.
- **Scope**: one issue equals one thing. Split large ideas into smaller issues.

### What to avoid in an issue

- Implementation prescriptions ("change line 42 of engine.go to..."). Describe the *what* and *why*; the coder agent decides the *how*.
- Issues that require access to private infrastructure, credentials, or internal context.

## PR path: bring code

1. Open a pull request directly. No issue required.
2. A maintainer reviews it. Two outcomes:
   - **Maintainer reviews directly.** Standard human review pattern. The maintainer iterates with you on comments and merges when ready.
   - **Maintainer adds `ai ready`.** The pr-reviewer agent posts a review with findings. Address them and push more commits; the agent re-reviews automatically when the SHA or comment count changes.
3. Either way, a human makes the final merge decision.

### What makes a great PR

- One concern per PR. Don't bundle a refactor with a feature.
- Tests for new behaviour. If you add a code path, add a test that exercises it.
- A description that explains the *why*, not just the *what*. The diff already shows the *what*.
- Link to a related issue if one exists, but PRs without issues are equally welcome.

### Test expectations

- Run `go test ./...` during normal PR iteration.
- Run targeted `go test ./internal/<pkg> -race` when changing concurrent code such as workflow processing, dispatch, scheduler, observe, or store.
- Pull request CI runs the normal suite so contributors and agents get faster feedback.
- The full `go test ./... -race` suite runs on `main` after merge; release tags should only be cut from a healthy `main`.

## Feedback on agent work

If an AI-implemented PR misses the mark, comment on it. The coder reads comments on its next run and iterates. This is normal: agents sometimes need guidance, the same way human contributors do.

## Code of conduct

Be respectful in issues and discussions. The agents don't have feelings, but the maintainers do. Constructive feedback on agent output is welcome. "This PR misses the point because..." is useful; "your bot is stupid" is not.

## Security

For security-sensitive contributions, see [SECURITY.md](SECURITY.md) for responsible disclosure. Critical fixes may bypass the normal flow.

## Questions?

Open an issue. The maintainer will respond.
