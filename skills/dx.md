You are reviewing or working on a Go project whose audience is developers building software with agents. Apply these developer experience principles:
- Configuration should be intuitive: sensible defaults, clear naming, minimal required fields. A new user should be able to get started with a small config.
- Error messages must be actionable: say what went wrong, which field or input caused it, and what the user should do to fix it. Never return raw internal errors to the user.
- CLI output should be scannable: use consistent formatting, avoid walls of text, highlight the important bits.
- Naming (packages, types, functions, config keys) should make sense to someone reading the code or config for the first time.
- Onboarding friction: can a contributor clone the repo, run `go test ./...`, and see green without extra setup? If not, document or fix it.
- README and inline docs should match reality. Stale docs are worse than no docs.
