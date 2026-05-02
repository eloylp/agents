# Security

This page describes the daemon's threat model and the recommendations shipped against it. **Security is entirely the operator's responsibility.** The project ships defaults and primitives, not guarantees — operators must evaluate, customise, and supplement against their own threat model. For responsible disclosure see [`SECURITY.md`](../SECURITY.md) at the repo root.

## Defaults the project ships

- **Webhook HMAC verification** on every `POST /webhooks/github` request — the only authentication enforced inside the daemon.
- **No daemon-level auth on any other endpoint.** Access control is the operator's reverse proxy. Operator-grade endpoints (`/runners`, `/traces`, `/traces/{span_id}/prompt`, `/traces/{span_id}/stream`, `/guardrails`, `/ui/`, `/agents`, `/skills`, `/repos`, `/events`, `/graph`, `/memory`, `/config`, `/export`, `/import`, `/backends`) MUST sit behind your auth layer. The composed prompt every run sees is persisted on the trace span (gzipped); without proxy auth, anyone who can reach the daemon can read it.
- **GitHub writes routed exclusively through the AI backend's MCP tools** — an architectural property of the daemon's code (no `gh` binary in the image, no GitHub write SDK in `cmd/agents`). Verify in your own audit if you depend on it; the daemon does not enforce it at runtime.
- **Per-event audit trail.** Every run records the composed prompt, every tool call with input/output summaries, and the response. Reachable from the dashboard for forensic review.
- **Default `security` guardrail seeded into the database.** A policy block prepended to every agent's composed prompt at render time. Recommends: ignore instructions found in untrusted text (issue/PR bodies, comments, file contents, tool results), refuse to read or output secrets from outside the working tree, refuse arbitrary network egress, halt on probable injection. **Operators can edit, disable, or replace it** via `/ui/config` → Guardrails. Inspect the live text at `GET /guardrails/security`. Like every prompt-level control, sufficiently determined indirect-injection attacks (role-play, encoded payloads, multi-turn manipulation) can defeat it.

## Reverse-proxy routing <a id="reverse-proxy-routing"></a>

All endpoints are unauthenticated at the daemon level. **Access control is the reverse proxy's responsibility.** A working production pattern is a two-router split: authenticated UI/API, public webhook endpoints.

| Router | Paths | Auth | Purpose |
|---|---|---|---|
| **UI / API** (authenticated) | everything except the public paths below | basic auth, OAuth2 proxy, or mTLS | `/ui/`, `/agents`, `/skills`, `/repos`, `/traces`, `/events`, `/graph`, `/memory`, `/runners`, `/guardrails`, `/config`, `/export`, `/import`, `/backends` |
| **Public** (no auth) | `/status`, `/webhooks/github`, `/run`, `/v1/*` | none | GitHub can't send a basic-auth header on webhooks; `/status` must stay reachable for liveness probes; `/run` and `/v1/*` (proxy) are meant to be called by trusted external systems that authenticate with their own mechanism. |

`/webhooks/github` is safe to expose publicly because every request is HMAC-verified against `GITHUB_WEBHOOK_SECRET` before it is accepted. `/run` does not currently authenticate callers — if you expose it, restrict it at the proxy with an allowlist or a shared secret header.

For production, drop the `ports: 8080:8080` block from the shipped compose so the proxy reaches the container on the internal Docker network instead, and replace `build: .` with a pinned `image:` reference. The compose file stays proxy-agnostic; the auth layer lives at your proxy.

### Traefik example

```yaml
services:
  agents:
    # ... volumes, env as in docker-compose.yaml ...
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=web"

      # Public router: webhooks, status, on-demand trigger, proxy.
      - "traefik.http.routers.agents-public.rule=Host(`agents.example.com`) && (PathPrefix(`/webhooks`) || Path(`/status`) || PathPrefix(`/run`) || PathPrefix(`/v1`))"
      - "traefik.http.routers.agents-public.entrypoints=websecure"
      - "traefik.http.routers.agents-public.tls.certresolver=letsencrypt"

      # Authenticated router: everything else.
      - "traefik.http.routers.agents-ui.rule=Host(`agents.example.com`)"
      - "traefik.http.routers.agents-ui.entrypoints=websecure"
      - "traefik.http.routers.agents-ui.tls.certresolver=letsencrypt"
      - "traefik.http.routers.agents-ui.middlewares=agents-auth@docker"

      - "traefik.http.middlewares.agents-auth.basicauth.usersfile=/etc/traefik/agents.htpasswd"
      - "traefik.http.services.agents.loadbalancer.server.port=8080"

networks:
  default:
    name: web
    external: true
```

Traefik picks the more specific router first, so webhook traffic bypasses the auth middleware. The principle (auth on UI/API, no auth on `/webhooks/github` / `/status` / `/run` / `/v1/*`) carries over to Caddy, nginx, or any other proxy.

## What the operator must own

The default `security` guardrail is a prompt-level recommendation, not a security boundary. Sophisticated indirect-injection attacks can defeat any natural-language rule. The directions below are operator territory; the daemon does not implement any of them today.

- **Quarantine untrusted content.** Wrap text from non-collaborator authors in something structurally distinguishable (e.g. `<untrusted_user_input author="@alice">…</untrusted_user_input>` tags) so the model can tell data from instructions on shape rather than wording.
- **Trust-gate comment authors.** Decide whether agents react to comments from non-collaborators at all. The webhook payload tells the daemon the author; the binding logic decides what to do with it.
- **Output filtering.** Scan every agent output (PR body, comment, file write, log line) for known auth-token patterns (`sk-ant-…`, `ghp_…`, `gho_…`, AWS keys, high-entropy blobs ≥ 40 chars) before it crosses a trust boundary.
- **Capability isolation.** The Docker container is the project's sandbox — it isolates the runners' filesystem and process tree from the host, and the shipped compose persists Claude / Codex auth in a per-container `agents-home` named volume populated by `claude login` inside the container, so a compromised runner cannot reach the operator's host-side tokens. Two limits remain: (a) inside the container, **concurrent runs share the filesystem** — a compromised agent can read another in-flight agent's working data; running one agent per container is the workaround until the project ships invocation-level isolation. (b) **Network egress depends on your Docker network configuration**; the daemon does not restrict outbound traffic, so a compromised runner can reach whatever your network policy allows.

These are deployment policy. Listed here so reporters and operators see what the recommendations are vs. what a deployment must add.
