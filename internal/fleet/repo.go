package fleet

import (
	"fmt"
	"strings"
)

// Repo describes a single GitHub repository the daemon operates on and the
// agent bindings declared for it.
type Repo struct {
	Name    string    `yaml:"name"`
	Enabled bool      `yaml:"enabled"`
	Use     []Binding `yaml:"use"`
}

// ValidateRepoName checks that a repo name follows the canonical
// "owner/repo" shape: exactly one slash with non-empty halves, no
// embedded whitespace. The HTTP item routes (`/repos/{owner}/{repo}`)
// require both segments, and a malformed single-segment name leaks
// past the routes (DELETE /repos/foo returns 404 because gorilla/mux
// can't bind it). Rejecting at the write boundary keeps every
// subsequent CRUD path addressable.
func ValidateRepoName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("repo name is required")
	}
	if strings.ContainsAny(trimmed, " \t\r\n") {
		return fmt.Errorf("repo name %q contains whitespace; expected owner/repo", trimmed)
	}
	owner, repo, ok := strings.Cut(trimmed, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return fmt.Errorf("repo name %q must be in owner/repo form", trimmed)
	}
	return nil
}
