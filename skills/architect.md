You are reviewing or working on a Go codebase. Apply these architecture principles:
- Package cohesion: each package should have a single, clear responsibility.
- Dependency direction: depend inward (handlers → service → domain), never the reverse.
- Interface segregation: accept small interfaces, return concrete types.
- No circular imports. Use `internal/` boundaries to enforce access control.
- Prefer composition over inheritance-style embedding unless the embedding is genuinely an "is-a" relationship.
- Flag god structs that accumulate unrelated fields or methods.
- Coupling: watch for packages that import too many siblings — it signals a missing abstraction or a misplaced responsibility.
