# Local models

Run your agent fleet against a self-hosted or OpenAI-compatible LLM backend, without changing how you write agents, prompts, or routing rules.

**What this unlocks:**

- **Swap models by changing one config line.** Ollama today, vLLM tomorrow, hosted Qwen on Together next week. Nothing in your agents changes.
- **Predictable cost.** A rented RTX 5090 runs at roughly `$0.40–0.80/hr`. For a fleet doing tens of runs per hour that's break-even at low traffic and considerably cheaper than per-token pricing once you're at meaningful volume.
- **Run on infrastructure you control.** Regulated, air-gapped, or privacy-conscious setups can keep the whole pipeline on hardware you own.
- **Keep the Claude Code tool surface.** The daemon routes the existing `claude` CLI through its built-in Anthropic-to-OpenAI translation proxy, so you inherit Claude Code's full tool stack (bash, file ops, MCP) on top of whatever model you point it at.

This doc walks through the complete setup: the architecture, a working recipe against `llama.cpp`, model choice by VRAM budget, recommended llama.cpp tuning, cost math, and honest caveats from our own Phase 0.5 validation.

---

## Architecture

```
┌──────────────────┐                                  
│ agents daemon    │   (your repo, runs cron + webhook routing)
│                  │                                  
│ ┌──────────────┐ │                                  
│ │ claude CLI   │ │   spawned per agent run, sends prompt on stdin
│ └──────┬───────┘ │                                  
│        │ ANTHROPIC_BASE_URL=http://localhost:8080
│        ▼                                            
│ ┌──────────────┐ │                                  
│ │  /v1/messages│ │   built-in Anthropic↔OpenAI proxy (Go, single binary)
│ │  /v1/models  │ │, translates tool_use, system, streaming
│ └──────┬───────┘ │                                  
└────────┼─────────┘                                  
         │                                            
         ▼                                            
┌──────────────────┐                                  
│ llama.cpp /      │                                  
│ vLLM / Ollama /  │   OpenAI-compatible /v1/chat/completions
│ hosted Qwen /    │                                  
│ anything else    │                                  
└──────────────────┘                                  
```

Two moving pieces inside the daemon:

1. **The proxy** (`internal/anthropic_proxy/`), an HTTP handler mounted on the daemon's existing server at `/v1/messages` and `/v1/models`. Accepts Anthropic Messages format, translates to OpenAI Chat Completions, forwards to your configured upstream, translates the response back. Text, system messages, tool-use / tool-result round-trips, streaming (fake-streaming via SSE, token-by-token streaming coming). Unauthenticated access is loopback-only for backend subprocesses; remote callers need daemon auth. Covered by unit tests.

2. **Per-backend `local_model_url`** (`AIBackendConfig.local_model_url`), set this on a backend entry and the daemon injects `ANTHROPIC_BASE_URL=<url>` into the subprocess environment, routing that backend's `claude` CLI through the local proxy. You can have two backends that both run `claude`, one hitting hosted Anthropic (no `local_model_url`), one hitting the proxy. You pick per-agent.

Nothing else changes. Same agents, same prompts, same config.

---

## Quick recipe, `llama.cpp` + Qwen 3.5 on a single box

The setup tested in [Phase 0.5](https://github.com/eloylp/agents/issues/78) of the local-models research. Adjust for your hardware.

### 1. Start the model server

```bash
# Build or download llama.cpp with CUDA support (or Vulkan, or CPU-only).
# Prebuilt releases: https://github.com/ggml-org/llama.cpp/releases

llama-server \
  -hf unsloth/Qwen3.5-35B-A3B-GGUF:Q4_K_M \  # or point at a local .gguf
  --host 0.0.0.0 --port 18000 \
  --ctx-size 49152 \                          # 48k; comfy for agent prompts
  --cache-type-k q8_0 \                       # halve KV cache memory
  --cache-type-v q8_0 \
  --batch-size 2048 \
  --ubatch-size 2048 \
  --cache-reuse 256 \                         # prefix-cache reuse, huge win
  --parallel 1 \
  --jinja \
  --flash-attn on
```

Key flags explained:

| Flag | What it does |
|---|---|
| `-ctx-size 49152` | 48k context. Enough for 5–12k agent prompts + output. Lower than the model's native 262k saves KV memory for more speed. |
| `--cache-type-k/v q8_0` | Quantize KV cache to 8-bit. Halves KV memory with negligible quality loss. Essential at long contexts. |
| `--batch-size / --ubatch-size 2048` | Prompt-processing chunk size. Larger = faster prefill, more GPU buffer memory. 2048 is a good default on any decent GPU. |
| `--cache-reuse 256` | Reuse KV cache across requests when at least 256 tokens of prefix match. Our agent prompts share ~90% prefix (skills + body) across runs. With this on, subsequent runs skip prefilling thousands of tokens. |
| `--jinja` | Use the Jinja chat template embedded in the GGUF. Required for correct tool-use and reasoning-block handling on Qwen 3.5. |
| `--parallel 1` | One concurrent request per worker. Bump up if you're running many agents in parallel and have spare VRAM. |
| `--flash-attn on` | Explicit FlashAttention. Usually on by default on recent builds. |

### 2. Configure daemon proxy env and fleet backends

The proxy is daemon runtime configuration, so set it through environment variables:

```env
AGENTS_PROXY_ENABLED=true
AGENTS_PROXY_PATH=/v1/messages
AGENTS_PROXY_UPSTREAM_URL=http://localhost:18000/v1
AGENTS_PROXY_UPSTREAM_MODEL=qwen
AGENTS_PROXY_UPSTREAM_TIMEOUT_SECONDS=3600
```

Then define the backends in your fleet YAML:

```yaml
backends:
  claude:                                 # default: hosted Anthropic
    command: claude
    timeout_seconds: 3600
    max_prompt_chars: 12000

  claude_local:                           # same binary, routed through the daemon proxy
    command: claude
    local_model_url: http://localhost:8080      # daemon injects ANTHROPIC_BASE_URL
    timeout_seconds: 3600
    max_prompt_chars: 12000
```

### 3. Pick which agents use local

```yaml
agents:
  - name: pr-reviewer
    backend: claude_local                 # Qwen-backed
    skills: [architect, testing, security]
    prompt: |
      Review the pull request for correctness, regressions, and risky behavior.

  - name: coder
    backend: claude                       # hosted Anthropic (see caveats below)
    skills: [architect, testing]
    prompt: |
      Implement the requested change end-to-end and open a pull request when ready.
    allow_prs: true
```

Restart the daemon. Done.

### 4. Verify

```bash
# From inside the agents container/network, or directly on the daemon host:
curl -sS http://localhost:8080/v1/models

# Expected: {"object":"list","data":[{"id":"qwen","object":"model","owned_by":"proxy"}]}
```

You'll see per-request observability in the daemon log:

```
proxy upstream ok  body_bytes=1471  client_stream=true  msgs=1  tools=32
```

`msgs` and `tools` grow through multi-turn tool loops, that's how you know real work is flowing.

---

## Picking a model by VRAM budget

All numbers assume Q4_K_M quantization unless stated. Larger quants (Q5, Q6, Q8) give slightly better quality at proportionally more memory. Smaller quants (Q3, IQ2) sacrifice quality for memory.

| VRAM | What fits fully on GPU | What runs with partial offload | Realistic pick |
|---|---|---|---|
| **6 GB** | 2–4B, 9B dense (tight) | 14B dense (half-offload) | Small reviewer-class tasks only. CPU-only is often more stable. |
| **8 GB** | up to 14B dense | 27B dense | 14B dense for reviewers; not enough for 35B MoE. |
| **12 GB** | 14B dense with context, 27B Q3 | 27B Q4, 35B-A3B MoE | 14B dense comfortably; 27B if accepting Q3 compromise. |
| **16 GB** | 14B Q5, 27B Q3 | 27B Q4, 35B-A3B MoE | 27B Q4 borderline; 35B-A3B MoE runs well with partial. |
| **24 GB** | 27B Q4, 35B-A3B MoE tight | 70B Q3 | **Sweet spot for coder-class work.** |
| **32 GB** (tested) | 35B-A3B MoE Q5 with headroom | 70B Q4 | Measured on RTX 5090. See numbers below. |
| **48 GB+** | 70B dense, 35B MoE at higher quant | much bigger | Diminishing returns until you hit flagship MoE sizes. |

**Notes on architecture:**

- **Qwen 3.5 uses a hybrid attention** (Gated Delta Net linear-attention + softmax). The fused-chunked GDN path needs a ~525 MB compute buffer that cannot be shrunk by context/batch/KV-precision tuning. On 6–8 GB cards this buffer plus model + KV exceeds VRAM, forcing CPU-only. Expect GPU speedup only at 12 GB+.
- **MoE models** (e.g. 35B-A3B: 35B total, 3B active per token) need the full model in memory but only compute against active experts per token. Generation speed tracks the active size, so 35B-A3B on CPU is closer to dense-3B-speed than dense-35B-speed. Excellent fit for workstations with plenty of RAM but modest GPU.
- **Avoid Unsloth "UD" dynamic quant variants** for 35B+ MoE models with llama.cpp build b8804, tensor load can take 15–25 min single-threaded (vs seconds for standard Q4_K_M). Standard quants load fast.

---

## Measured numbers

From our Phase 0.5 test: Qwen3.5-35B-A3B-UD-Q5_K_XL on NVIDIA RTX 5090 (32 GB VRAM), CUDA backend, llama.cpp b8804, default per-request settings (no `--cache-reuse`, default batch).

| Metric | Number |
|---|---|
| Prefill on a 3518-token prompt | **5157 tok/s** (0.68 s total) |
| Decode (sustained) | **~62–76 tok/s** |
| Per-request fixed overhead | ~500 ms (DERP relay + HTTP + server) |
| Wall time for a 6k-input / 500-output run | ~7–10 s |
| Tool-loop round-trips completed in one coder run | **90+** |
| Translation errors through the proxy | **0** in 200+ round-trips |

**Projected with full tuning** (`--cache-reuse 256` + `--batch-size 2048`): per-agent wall time drops 30–50% on cron runs that share prompt prefix with the previous run.

**Comparison context**: hosted Claude Sonnet decode is typically 60–100 tok/s, GPT-4o 80–120 tok/s. Qwen 3.5 on a local 5090 is in the same ballpark, not a downgrade.

---

## Honest caveats

Not everything is sunshine. Things we learned the hard way:

### Structured output enforcement

Both backends enforce a JSON response schema at the CLI level. The daemon embeds the schema (`internal/ai/response-schema.json`) in the binary and appends the appropriate flags automatically, no config or file mounts needed:

- **Claude** (including `claude_local`): `--output-format stream-json --json-schema '<schema>'` appended automatically. Claude emits a stream-JSON response and the daemon extracts the final top-level JSON object from it.
- **Codex**: `--output-schema <temp-file>` appended automatically. Model output is schema-constrained directly.
- **Local models via proxy**: structured output enforcement works the same as hosted Claude (the `claude_local` backend gets the flags automatically). The proxy passes the response through; what matters is whether the local model respects the schema.

### Capability gap on action-taking agents

We validated Qwen 3.5 driving `pr-reviewer` and `scout`-class agents cleanly. When we flipped `coder`, an action-heavy agent that edits code, commits, posts comments, two failure modes appeared:

1. **Conservative disposition.** Qwen reads and analyses thoroughly but is reluctant to invoke write tools even when the prompt asks for action. Prompting heavily with imperative verbs and "silence is not an option" framing fixes this partially.
2. **Fact hallucination in populated templates.** Given a "post a status comment in this format" instruction, Qwen will populate the template fields with **fabricated** values (non-existent commit SHAs, false merge states) when the real facts aren't easily derived. This can put misleading text on real PRs.

Neither is a proxy bug. Both are Qwen-family disposition issues vs Claude. The gap narrows every model generation but is real today.

**Guideline until further notice:**

- **Reviewer-class agents** (pr-reviewer, scouts, specialist reviewers, product-strategist): local Qwen works fine.
- **Acting agents** (coder, refactorer): default them to hosted Claude. Graduate to local only after you've measured their output against GitHub reality over a meaningful time window.

**The shipped `mcp-tool-usage` guardrail** (seeded by migration 012, position 10, enabled by default) directly addresses the conservative-disposition issue. It tells every agent to use GitHub MCP tools first, fetch surrounding context (PR description, diff, prior comments, linked issue), and fall back to authenticated `gh` only when MCP is insufficient or a safe local checkout/test/PR loop is required. Hosted Claude reaches for tools without prompting; local Qwen-class models benefit visibly from the explicit reminder. Edit, disable, or replace it in the dashboard's Guardrails tab if you want a different shape, hosted-only fleets can disable it with no harm.

### Tailscale DERP relay adds ~500 ms

If your model runs on a separate box reached via Tailscale, userspace-networking mode relays through DERP and adds ~500 ms per request. Fine for agent workloads (the run is 5–30 s end-to-end anyway) but noticeable if you compare raw latency to local loopback.

### `OAuth in ~/.claude.json` does NOT override `ANTHROPIC_BASE_URL`

We initially thought we needed `--bare` to skip OAuth and force env-based routing. Empirically, the current claude CLI honours `ANTHROPIC_BASE_URL` cleanly even when OAuth credentials are present in the mounted `~/.claude.json`. You get the full tool surface (32 tools) without `--bare`. Keep your arg list clean.

### Long sub-agent tool loops can hit the proxy timeout

The `claude` CLI's Task tool can spawn sub-agents whose conversations build up to 30k+ tokens. If the upstream request takes longer than `AGENTS_PROXY_UPSTREAM_TIMEOUT_SECONDS`, the proxy returns 502 mid-stream. **Bump to 3600 seconds** (matching the claude backend timeout) in any serious setup.

### Prompt caching is stripped at translation

The Anthropic `cache_control: {type: "ephemeral"}` markers the `claude` CLI emits on system blocks (and on tools, when present) get dropped at translation time, the OpenAI Chat Completions schema has no equivalent. There's no Anthropic-compatible prompt cache when routing through the proxy. Recover the prefix-share benefit at the LLM-server layer with `--cache-reuse 256` on `llama.cpp` (already in the Quick recipe). The cache token columns on traces (`cache_read_tokens`, `cache_write_tokens`) report `0` for any local-model run, that's expected, not a missed-cache bug. See [#402](https://github.com/eloylp/agents/issues/402).

### Not in scope yet

- **True pipe-through streaming.** The current implementation fakes streaming by emitting the whole response as a canonical SSE sequence at the end of generation. The CLI parses it correctly, but you don't see token-by-token output mid-generation. True streaming is tracked at [#401](https://github.com/eloylp/agents/issues/401).
- **Codex backend routing via the proxy.** Codex speaks OpenAI's Responses API (`/v1/responses`), not Chat Completions. Our proxy doesn't translate that. If you want `pr-reviewer` on codex against a local endpoint, that's [#146](https://github.com/eloylp/agents/issues/146).

---

## Troubleshooting

### "Invalid API key" on every run

`ANTHROPIC_BASE_URL` is not reaching the subprocess. Check that `local_model_url` is set on the `claude_local` backend in your fleet config, the daemon injects `ANTHROPIC_BASE_URL` from that field. `ANTHROPIC_API_KEY` must still be present in the container environment (any non-empty value works; the local endpoint ignores it). Set it in your `.env` file or compose `environment:` block.

Verify with:

```bash
docker compose exec agents env | grep ANTHROPIC_
```

### Model responds in `<think>...</think>` blocks and burns all tokens before producing output

Qwen 3.5's reasoning mode ate your `max_tokens`. Configure your upstream server to disable reasoning mode where supported, then restart it.

### llama-server on 6 GB GPU fails with `failed to allocate compute pp buffers`

Qwen 3.5's hybrid-attention fused-chunk buffer doesn't fit on 6 GB cards alongside the model + KV. Either:
- Run CPU-only (move `libggml-vulkan.so` out of the way or use a CPU-only build), or
- Use a smaller model (9B dense), or
- Use a non-hybrid Qwen (Qwen3-Coder-30B) for GPU acceleration.

### Agent runs take very long and eventually fail with "Unable to connect"

Your `AGENTS_PROXY_UPSTREAM_TIMEOUT_SECONDS` is too low for deep tool loops. Bump to 3600.

### llama-server takes 15+ minutes to load a 35B model

You picked an Unsloth "UD" Dynamic quant variant. Switch to standard Q4_K_M.

---

## Production deployment considerations

- **Run the model server on a dedicated host.** The agents daemon is tiny; your GPU host can be anything from a home workstation to a rented RTX 5090. Connect via Tailscale or a trusted LAN. Don't expose the llama-server port publicly.
- **Set `--api-key`** on llama-server if the network is not fully trusted.
- **Monitor token use** via the `proxy upstream ok body_bytes=… msgs=… tools=…` log line. Increasing `msgs` across runs means agents are converging through tool loops; flat `msgs` counts may indicate the model is bailing without real work.
- **Pre-download GGUFs.** `llama-server -hf ...` auto-downloads on first run but can be slow on throttled networks. Pre-fetch to a local path and pass `-m /path/to/file.gguf` for reliable startup.
- **Restart the daemon after changing backend or proxy env**, the daemon reads runtime env on startup.

---

## Related

- [#78](https://github.com/eloylp/agents/issues/78), full research log including Phase 0.5 validation numbers, hardware findings, and all the mistakes we made getting here.
- [#137](https://github.com/eloylp/agents/issues/137) / [#138](https://github.com/eloylp/agents/pull/138), the in-daemon Anthropic↔OpenAI proxy design and implementation.
- [#146](https://github.com/eloylp/agents/issues/146), follow-up: routing the codex CLI through a local backend (OpenAI Responses API translation).
- [#401](https://github.com/eloylp/agents/issues/401), follow-up: real token-by-token streaming through the proxy (today fake-streamed).
- [#402](https://github.com/eloylp/agents/issues/402), the prompt-caching translation gap and zero cache-token columns on local-model traces.
