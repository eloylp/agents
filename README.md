# Agents

![agents](docs/agents.jpg)

**Your personal, provider-agnostic tool for building reusable, event-driven agentic workflows.**

Define your agents once. Wire them to repos with labels, cron schedules, or event subscriptions. The daemon dispatches them via AI CLIs ([Claude Code](https://docs.anthropic.com/en/docs/claude-code), [Codex](https://github.com/openai/codex)) and lets them work through native GitHub primitives: issues, PRs, reviews, comments.

## Features

- **[Quick start](docs/quickstart.md)**: get the daemon running on a repo in under five minutes.
- **[Self-hosted](docs/docker.md)**: your code and prompts stay on your infrastructure. No SaaS dependency.
- **[Multi-backend](docs/configuration.md)**: Claude, Codex, and named local backends. Mix backends per agent.
- **[Discovery and diagnostics](docs/configuration.md)**: the daemon detects backends and tools, validates CLI health, and persists discovery snapshots.
- **[Local-model support](docs/local-models.md)**: a built-in Anthropic-to-OpenAI translation proxy routes the fleet through `llama.cpp`, Ollama, vLLM, or any OpenAI-compatible endpoint. Zero vendor lock-in.
- **[One agent model, many triggers](docs/events.md)**: label events, cron schedules, GitHub event subscriptions, on-demand API calls. Same agent, wired however you want.
- **[Composable skills](docs/configuration.md)**: reusable guidance blocks (architecture, security, testing, DX, ...) composed into any agent.
- **[Reactive inter-agent dispatch](docs/dispatch.md)**: agents invoke each other at runtime with depth, fanout, and dedup safety limits.
- **[SQLite config store](docs/api.md)**: manage the fleet over a CRUD API and a built-in dashboard instead of editing YAML. Import / export between the two.
- **[Built-in web dashboard](docs/ui.md)**: live event firehose, agent traces with tool-loop transcripts, dispatch graph, memory viewer, fleet management.
- **Transparent**: every agent action is a GitHub comment, issue, or PR. Reviewable. Revertable.

## Contributing

Both human and agent contributions are welcome: issues, PRs, doc fixes, prompts, ideas. The autonomous fleet picks up issues and PRs labeled `ai ready` (the maintainer's opt-in signal); everything else is reviewed and merged by humans. See [CONTRIBUTING.md](CONTRIBUTING.md) for the full flow.
