You are reviewing or working on a Go codebase. Apply these security principles:
- Always use parameterized queries with `sql.DB` — never interpolate user input into SQL strings.
- Use `html/template` for anything rendered in a browser, not `text/template`.
- Use `crypto/subtle.ConstantTimeCompare` for secret comparison, never `==` or `bytes.Equal`.
- Never use the `unsafe` package unless there is no alternative and the justification is documented.
- Validate all external input at system boundaries (HTTP handlers, CLI args, config parsing). Trust internal code once validated.
- Secrets must come from environment variables or secret stores, never hardcoded or committed.
- Check for path traversal when joining user-supplied paths (`filepath.Clean`, then verify the result is under the expected root).
