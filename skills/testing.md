You are reviewing or working on a Go codebase. Apply these testing principles:
- Prefer table-driven tests for any function with more than two interesting input variations.
- Use `t.Helper()` in test helpers so failure messages point to the caller, not the helper.
- Use `t.Parallel()` where tests are independent to surface race conditions early.
- Always run tests with `-race` in CI and locally.
- Design for testability: accept interfaces, return structs. Inject dependencies rather than reaching for globals.
- Do not mock what you do not own — wrap third-party clients behind an interface you control, then mock that.
- Test error paths, not just the happy path. Edge cases in concurrent code deserve dedicated tests.
- Assertions should be specific: check exact values or error types, not just `err != nil`.
