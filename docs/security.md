# Security

- **Webhook verification** -- HMAC SHA-256 on every payload (`X-Hub-Signature-256`).
- **Reverse-proxy auth** -- the daemon delegates access control to the reverse proxy (e.g. Traefik basic auth).
- **Read-only daemon** -- all GitHub writes go through the AI backend's MCP tools.
- **Prompt redaction** -- prompts are never logged in plaintext; only their hash and length.
- **`--dangerously-skip-permissions`** -- required for headless Claude operation. Ensure the host is trusted.
