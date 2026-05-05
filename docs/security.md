# Security

This page describes the daemon's threat model and the recommendations shipped against it. **Security is entirely the operator's responsibility.** The project ships defaults and primitives, not guarantees, operators must evaluate, customise, and supplement against their own threat model. For responsible disclosure see [`SECURITY.md`](../SECURITY.md) at the repo root.

## Defaults the project ships

- **Webhook HMAC verification** on every `POST /webhooks/github` request.
- **DB-backed daemon auth for sensitive routes.** The dashboard creates the first local user, browser sessions use opaque DB-backed tokens in an `HttpOnly` cookie, and MCP/API clients use named revocable bearer tokens created from Config -> Tokens. Existing `AGENTS_AUTH_BEARER_TOKEN_HASH` deployments still work as bootstrap and compatibility auth.
- **The daemon itself is read-only against GitHub.** GitHub operations happen inside the AI backend subprocess. Agents are instructed to prefer GitHub MCP tools; an authenticated `gh` CLI is present as a fallback for complex local checkout, test, and PR flows. The daemon still has no GitHub write SDK in `cmd/agents`.
- **Per-event audit trail.** Every run records the composed prompt, every tool call with input/output summaries, and the response. Reachable from the dashboard for forensic review.
- **Default built-in guardrails seeded into the database.** Policy blocks prepended to every agent's composed prompt at render time. `security` recommends ignoring instructions found in untrusted text, refusing secret reads/exfiltration, refusing out-of-tree filesystem access, refusing arbitrary network egress, and halting on probable injection. `memory-scope` tells agents to use only daemon-provided `Existing memory:` for the current `(agent, repo)` pair, ignore CLI-native/session/global memory, and stay bound to the repository named in the runtime context. `mcp-tool-usage` tells agents to use MCP first and authenticated `gh` only as fallback for complex local checkout/test/PR loops. **Operators can edit, disable, or replace built-ins** via `/ui/config` → Guardrails. Inspect live text at `GET /guardrails/{name}`. Like every prompt-level control, sufficiently determined indirect-injection attacks (role-play, encoded payloads, multi-turn manipulation) can defeat them.

## Daemon auth <a id="daemon-auth"></a>

On a fresh database, open the dashboard and create the first user. The daemon stores a password hash and issues an opaque session token in an `HttpOnly` cookie. After at least one user exists, sensitive REST, MCP, `/run`, traces, runners, config, guardrails, repos, skills, agents, memory, graph, and event routes require one of:

1. A valid browser session cookie.
2. A valid DB-backed API token sent as `Authorization: Bearer <token>`.
3. The legacy `AGENTS_AUTH_BEARER_TOKEN_HASH` bearer token, when configured.

Create MCP/API tokens from Config -> Tokens. Plaintext API tokens are returned only once at creation; the database stores only token hashes plus metadata such as name, prefix, creation time, last-used time, expiry, and revocation time.

`GITHUB_TOKEN` is unrelated to daemon auth. It remains the GitHub credential used by MCP, the `gh` fallback, and AI backend subprocesses. `GITHUB_WEBHOOK_SECRET` is also separate and remains the HMAC secret for `/webhooks/github`.

## Legacy bearer-token auth <a id="bearer-token-auth"></a>

Set the token hash at daemon startup:

```bash
# macOS
printf '%s' 'your-token' | shasum -a 256 | awk '{print $1}'

# Linux
printf '%s' 'your-token' | sha256sum | awk '{print $1}'
```

Then put the resulting 64-character hex string in `.env`:

```env
AGENTS_AUTH_BEARER_TOKEN_HASH=...
```

Do not hash a string with a trailing newline. If `AGENTS_AUTH_BEARER_TOKEN_HASH` is empty or unset, legacy bearer compatibility is disabled.

Authenticated clients send:

```http
Authorization: Bearer your-token
```

If no local users exist and `AGENTS_AUTH_BEARER_TOKEN_HASH` is set, first-user bootstrap requires this legacy bearer token. After bootstrap, prefer named DB-backed API tokens and remove the env hash when migration is complete.

## Reverse-proxy routing <a id="reverse-proxy-routing"></a>

Use your reverse proxy for TLS and routing. With `AGENTS_AUTH_BEARER_TOKEN_HASH` set, the proxy no longer needs to provide basic auth for API/MCP access.

| Router | Paths | Auth | Purpose |
|---|---|---|---|
| **Daemon** | all paths | session cookie or daemon API token on sensitive routes | `/mcp`, `/run`, API, observability, config, runners; `/ui/` shell loads publicly but data calls require auth |
| **Public** | `/status`, `/webhooks/github`, `/auth/status`, `/auth/login`, `/auth/bootstrap`, `/v1/*`, `/ui/*` shell/assets | none at proxy | GitHub cannot send auth on webhooks; `/status` must stay reachable for liveness probes; `/v1/*` proxy clients use their own upstream auth; `/ui/*` must render before the browser has a session. |

`/webhooks/github` is safe to expose publicly because every request is HMAC-verified against `GITHUB_WEBHOOK_SECRET` before it is accepted. `/run` is protected once daemon auth is initialized.

For production, drop the `ports: 8080:8080` block from the shipped compose so the proxy reaches the container on the internal Docker network instead, and replace `build: .` with a pinned `image:` reference.

### Traefik example

```yaml
services:
  agents:
    # ... volumes, env as in docker-compose.yaml ...
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=web"

      # Public-at-proxy router: daemon enforces auth on sensitive routes.
      - "traefik.http.routers.agents.rule=Host(`agents.example.com`)"
      - "traefik.http.routers.agents.entrypoints=websecure"
      - "traefik.http.routers.agents.tls.certresolver=letsencrypt"
      - "traefik.http.services.agents.loadbalancer.server.port=8080"

networks:
  default:
    name: web
    external: true
```

The principle carries over to Caddy, nginx, or any other proxy: terminate TLS, forward to the daemon, and let daemon auth protect sensitive API/MCP routes.

## What the operator must own

The default `security` guardrail is a prompt-level recommendation, not a security boundary. Sophisticated indirect-injection attacks can defeat any natural-language rule. The directions below are operator territory; the daemon does not implement any of them today.

- **Quarantine untrusted content.** Wrap text from non-collaborator authors in something structurally distinguishable (e.g. `<untrusted_user_input author="@alice">…</untrusted_user_input>` tags) so the model can tell data from instructions on shape rather than wording.
- **Trust-gate comment authors.** Decide whether agents react to comments from non-collaborators at all. The webhook payload tells the daemon the author; the binding logic decides what to do with it.
- **Output filtering.** Scan every agent output (PR body, comment, file write, log line) for known auth-token patterns (`sk-ant-…`, `ghp_…`, `gho_…`, AWS keys, high-entropy blobs ≥ 40 chars) before it crosses a trust boundary.
- **Capability isolation.** The Docker container is the project's sandbox, it isolates the runners' filesystem and process tree from the host, and the shipped compose persists Claude / Codex auth, MCP config, and `gh` auth in a per-container `agents-home` named volume populated by `agents-setup` inside the container, so a compromised runner cannot reach the operator's host-side tokens. Two limits remain: (a) inside the container, **concurrent runs share the filesystem**, a compromised agent can read another in-flight agent's working data; running one agent per container is the workaround until the project ships invocation-level isolation. (b) **Network egress depends on your Docker network configuration**; the daemon does not restrict outbound traffic, so a compromised runner can reach whatever your network policy allows.

These are deployment policy. Listed here so reporters and operators see what the recommendations are vs. what a deployment must add.
