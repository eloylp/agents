package config

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/eloylp/agents/internal/fleet"
)

const InlineAgentPromptUnsupported = "agent inline prompt bodies are unsupported; create a prompt catalog entry and use prompt_ref or prompt_id"

func rejectInlineAgentPrompts(data []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	doc := root.Content[0]
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "agents" || doc.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for _, agent := range doc.Content[i+1].Content {
			if agent.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(agent.Content); j += 2 {
				if agent.Content[j].Value == "prompt" {
					return errors.New(InlineAgentPromptUnsupported)
				}
			}
		}
	}
	return nil
}

// PromptContentForAgent resolves the prompt catalog content selected by an
// agent without attaching prompt bodies to the agent domain model.
func (c *Config) PromptContentForAgent(agent fleet.Agent, repo string) (string, error) {
	prompt, err := c.PromptForAgent(agent, repo)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(prompt.Content) == "" {
		return "", fmt.Errorf("prompt %q is empty", prompt.Name)
	}
	return prompt.Content, nil
}

// PromptForAgent resolves the prompt catalog row visible to agent. PromptID is
// authoritative. Otherwise PromptRef is resolved against optional PromptScope
// or the visible catalog scopes: global, same workspace, and same repo.
func (c *Config) PromptForAgent(agent fleet.Agent, repo string) (fleet.Prompt, error) {
	workspaceID := fleet.NormalizeWorkspaceID(agent.WorkspaceID)
	repo = fleet.NormalizeRepoName(repo)
	if agent.ScopeType == "repo" && strings.TrimSpace(agent.ScopeRepo) != "" {
		repo = fleet.NormalizeRepoName(agent.ScopeRepo)
	}
	if id := strings.TrimSpace(agent.PromptID); id != "" {
		for _, p := range c.Prompts {
			if p.ID == id && promptVisibleToAgent(p, workspaceID, repo) {
				return p, nil
			}
		}
		return fleet.Prompt{}, fmt.Errorf("references unknown prompt_id %q", id)
	}
	ref := fleet.NormalizePromptName(agent.PromptRef)
	if ref == "" {
		return fleet.Prompt{}, errors.New("prompt_id or prompt_ref is required")
	}
	if scopeWorkspace, scopeRepo, explicit := fleet.ParseCatalogScopePath(agent.PromptScope); explicit {
		for _, p := range c.Prompts {
			if fleet.NormalizePromptName(p.Name) == ref &&
				fleet.NormalizeWorkspaceID(p.WorkspaceID) == fleet.NormalizeWorkspaceID(scopeWorkspace) &&
				fleet.NormalizeRepoName(p.Repo) == fleet.NormalizeRepoName(scopeRepo) &&
				promptVisibleToAgent(p, workspaceID, repo) {
				return p, nil
			}
		}
		return fleet.Prompt{}, fmt.Errorf("references unknown prompt_ref %q", ref)
	}
	var matches []fleet.Prompt
	for _, p := range c.Prompts {
		if fleet.NormalizePromptName(p.Name) == ref && promptVisibleToAgent(p, workspaceID, repo) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		return fleet.Prompt{}, fmt.Errorf("references unknown prompt_ref %q", ref)
	}
	if len(matches) > 1 {
		return fleet.Prompt{}, fmt.Errorf("ambiguous prompt_ref %q in workspace %q; use prompt_id", ref, workspaceID)
	}
	return matches[0], nil
}

func promptVisibleToAgent(p fleet.Prompt, workspaceID, repo string) bool {
	pWorkspace := strings.TrimSpace(p.WorkspaceID)
	if pWorkspace == "" {
		return strings.TrimSpace(p.Repo) == ""
	}
	if fleet.NormalizeWorkspaceID(pWorkspace) != workspaceID {
		return false
	}
	pRepo := fleet.NormalizeRepoName(p.Repo)
	return pRepo == "" || pRepo == repo
}
