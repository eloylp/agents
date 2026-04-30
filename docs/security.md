# Security

- **Webhook verification.** HMAC SHA-256 on every payload (`X-Hub-Signature-256`).
- **Reverse-proxy auth.** The daemon delegates access control to the reverse proxy (e.g. Traefik basic auth).
- **`/runners` is operator-grade.** The endpoint exposes payload metadata (kinds, repos, numbers) and lets a caller delete or retry queued events. Gate it behind the same auth proxy as `/ui/` and the rest of the management surface.
- **Read-only daemon.** All GitHub writes go through the AI backend's MCP tools.
- **Prompt redaction.** Prompts are never logged in plaintext; only their hash and length.
- **`--dangerously-skip-permissions`.** Required for headless Claude operation. Ensure the host is trusted.
