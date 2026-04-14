You are reviewing or working on a Go codebase. Apply these operational principles:
- Structured logging (zerolog): always attach contextual fields (component, repo, id) rather than formatting them into the message string.
- Graceful shutdown: respect context cancellation, drain in-flight work within a deadline, log when the deadline is exceeded.
- Health checks should reflect actual readiness (can the service accept work?), not just "process is alive".
- Containers: produce a static binary with no CGO, use multi-stage builds, run as non-root, include only CA certs in the final image.
- Configuration: fail fast on invalid config at startup. Never silently fall back to a default that changes behavior in surprising ways.
- Metrics and observability: prefer counters and histograms over log-line grepping. If the service has no metrics yet, flag it.
