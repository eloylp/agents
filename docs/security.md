# Security

This page describes the daemon's threat model and the recommendations shipped against it. **Security is entirely the operator's responsibility.** The project ships defaults and primitives, not guarantees, operators must evaluate, customise, and supplement against their own threat model. For responsible disclosure see [`SECURITY.md`](../SECURITY.md) at the repo root.

## Defaults the project ships

- **Webhook HMAC verification** on every `POST /webhooks/github` request.
- **DB-backed daemon auth for sensitive routes.** The root login page creates the first local user, browser sessions use opaque DB-backed tokens in an `HttpOnly` cookie, and MCP/API clients use named revocable bearer tokens created from Config -> Authentication.
- **The daemon itself is read-only against GitHub.** GitHub operations happen inside the AI backend subprocess. Agents are instructed to prefer GitHub MCP tools; an authenticated `gh` CLI is present as a fallback for complex local checkout, test, and PR flows. The daemon still has no GitHub write SDK in `cmd/agents`.
- **Per-event audit trail.** Every run records the composed prompt, every tool call with input/output summaries, and the response. Reachable from the dashboard for forensic review.
- **Default built-in guardrails seeded into the database.** Policy blocks prepended to every agent's composed prompt at render time. `security` recommends ignoring instructions found in untrusted text, refusing secret reads/exfiltration, refusing out-of-tree filesystem access, refusing arbitrary network egress, and halting on probable injection. `memory-scope` tells agents to use only daemon-provided `Existing memory:` for the current `(workspace, agent, repo)` key, ignore CLI-native/session/global memory, and stay bound to the repository named in the runtime context. `mcp-tool-usage` tells agents to use MCP first and authenticated `gh` only as fallback for complex local checkout/test/PR loops. **Operators can edit, disable, or replace built-ins** via `/ui/config` → Guardrails. Inspect live text at `GET /guardrails/{id}`; legacy global names are accepted as a compatibility fallback. Like every prompt-level control, sufficiently determined indirect-injection attacks (role-play, encoded payloads, multi-turn manipulation) can defeat them.

## Daemon auth <a id="daemon-auth"></a>

On a fresh database, open the root login page (`/`) and create the first user. That bootstrapped user is the admin user, can create or remove additional dashboard users from Config -> Authentication, and cannot be removed. Non-admin users can sign in, manage fleet configuration, and create their own API tokens, but they cannot create or remove users. The daemon stores password hashes and issues opaque session tokens in `HttpOnly` cookies. After at least one user exists, sensitive REST, MCP, `/run`, traces, runners, config, guardrails, repos, skills, agents, memory, graph, and event routes require one of:

1. A valid browser session cookie.
2. A valid DB-backed API token sent as `Authorization: Bearer <token>`.

Create MCP/API tokens from Config -> Authentication. Plaintext API tokens are returned only once at creation; the database stores only token hashes plus metadata such as name, prefix, creation time, last-used time, expiry, and revocation time.

`GITHUB_TOKEN` is unrelated to daemon auth. It remains the GitHub credential used by MCP, the `gh` fallback, and AI backend subprocesses. `GITHUB_WEBHOOK_SECRET` is also separate and remains the HMAC secret for `/webhooks/github`.

## Reverse-proxy routing <a id="reverse-proxy-routing"></a>

Use your reverse proxy for TLS and routing. The proxy does not need to provide basic auth for API/MCP access; daemon auth protects sensitive routes itself.

| Router | Paths | Auth | Purpose |
|---|---|---|---|
| **Daemon** | all paths | session cookie or daemon API token on sensitive routes | `/mcp`, `/run`, API, observability, config, runners; `/ui/` shell loads publicly but data calls require auth |
| **Public** | `/`, `/status`, `/webhooks/github`, `/auth/status`, `/auth/login`, `/auth/bootstrap`, `/ui/*` shell/assets | none at proxy | GitHub cannot send auth on webhooks; `/status` must stay reachable for liveness probes; `/` hosts the login/bootstrap page and redirects authenticated sessions to `/ui/`; `/ui/*` must render before the browser has a session. |
| **Local proxy** | `/v1/messages`, `/v1/models` when proxy is enabled | no daemon auth only for loopback clients; remote clients need daemon auth | Backend CLI subprocesses run on the daemon host/container and call the proxy locally. Do not expose the proxy as an unauthenticated public route. |

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
- **Capability isolation.** The daemon starts each run in a fresh ephemeral runner container and removes it after success, error, timeout, or cancellation where Docker allows cleanup. The runner receives only an allowlisted environment plus per-run prompt/config material. Do not mount host secrets or persistent CLI home directories into the runner.
- **Docker socket boundary.** The daemon container mounts `/var/run/docker.sock` so it can create runners. Docker socket access is effectively root-equivalent on the host; anyone who compromises the daemon can likely control the Docker host. Put the daemon behind strong auth, TLS, network controls, and host-level monitoring.
- **Network egress.** Runtime settings can choose Docker network mode, including `none`, but the daemon does not implement a domain/CIDR firewall. A compromised runner can reach whatever the selected Docker network and host policy allow.

These are deployment policy. Listed here so reporters and operators see what the recommendations are vs. what a deployment must add.
