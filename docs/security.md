# Security

- **Webhook verification.** HMAC SHA-256 on every payload (`X-Hub-Signature-256`).
- **Reverse-proxy auth.** The daemon delegates access control to the reverse proxy (e.g. Traefik basic auth).
- **Operator-grade endpoints behind auth.** `/runners`, `/traces`, `/traces/{span_id}/prompt`, `/traces/{span_id}/stream`, `/ui/` and the rest of the management surface must sit behind your auth proxy. The composed prompt every run sees is persisted on the trace span (gzipped) so operators can inspect what the agent saw — that data is gated by your proxy, not by the daemon. The live-stream endpoint exposes the same content in real time and must be gated identically.
- **Read-only daemon.** All GitHub writes go through the AI backend's MCP tools.
- **Prompt logging.** Prompts are not written to logs in any form; the trace span on disk is the single audit record. Logs carry only the prompt's character count for correlation.
- **`--dangerously-skip-permissions`.** Required for headless Claude operation. Ensure the host is trusted.
