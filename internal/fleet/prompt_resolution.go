package fleet

import (
	"errors"
	"fmt"
	"strings"
)

// ResolvePromptContentForAgent resolves the catalog prompt content selected by
// an agent without attaching prompt bodies to the agent domain model.
func ResolvePromptContentForAgent(prompts []Prompt, agent Agent, repo string) (string, error) {
	prompt, err := ResolvePromptForAgent(prompts, agent, repo)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(prompt.Content) == "" {
		return "", fmt.Errorf("prompt %q is empty", prompt.Name)
	}
	return prompt.Content, nil
}

// ResolvePromptForAgent resolves the catalog row visible to agent. PromptID is
// authoritative. Otherwise PromptRef is resolved against optional PromptScope
// or the visible catalog scopes: global, same workspace, and same repo.
func ResolvePromptForAgent(prompts []Prompt, agent Agent, repo string) (Prompt, error) {
	workspaceID := NormalizeWorkspaceID(agent.WorkspaceID)
	repo = NormalizeRepoName(repo)
	if agent.ScopeType == "repo" && strings.TrimSpace(agent.ScopeRepo) != "" {
		repo = NormalizeRepoName(agent.ScopeRepo)
	}
	if id := strings.TrimSpace(agent.PromptID); id != "" {
		for _, p := range prompts {
			if p.ID == id && promptVisibleToAgent(p, workspaceID, repo) {
				return p, nil
			}
		}
		return Prompt{}, fmt.Errorf("references unknown prompt_id %q", id)
	}
	ref := NormalizePromptName(agent.PromptRef)
	if ref == "" {
		return Prompt{}, errors.New("prompt_id or prompt_ref is required")
	}
	if scopeWorkspace, scopeRepo, explicit := ParseCatalogScopePath(agent.PromptScope); explicit {
		for _, p := range prompts {
			if NormalizePromptName(p.Name) == ref &&
				NormalizeWorkspaceID(p.WorkspaceID) == NormalizeWorkspaceID(scopeWorkspace) &&
				NormalizeRepoName(p.Repo) == NormalizeRepoName(scopeRepo) &&
				promptVisibleToAgent(p, workspaceID, repo) {
				return p, nil
			}
		}
		return Prompt{}, fmt.Errorf("references unknown prompt_ref %q", ref)
	}
	var matches []Prompt
	for _, p := range prompts {
		if NormalizePromptName(p.Name) == ref && promptVisibleToAgent(p, workspaceID, repo) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		return Prompt{}, fmt.Errorf("references unknown prompt_ref %q", ref)
	}
	if len(matches) > 1 {
		return Prompt{}, fmt.Errorf("ambiguous prompt_ref %q in workspace %q; use prompt_id", ref, workspaceID)
	}
	return matches[0], nil
}

func promptVisibleToAgent(p Prompt, workspaceID, repo string) bool {
	pWorkspace := strings.TrimSpace(p.WorkspaceID)
	if pWorkspace == "" {
		return strings.TrimSpace(p.Repo) == ""
	}
	if NormalizeWorkspaceID(pWorkspace) != workspaceID {
		return false
	}
	pRepo := NormalizeRepoName(p.Repo)
	return pRepo == "" || pRepo == repo
}
