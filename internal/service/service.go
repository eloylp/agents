// Package service owns mutable fleet/config use cases.
//
// REST and MCP handlers should decode wire shapes, call this package, then map
// typed errors back to their transport response. The service keeps mutation
// orchestration out of handlers while the store package remains the SQLite
// persistence boundary.
package service

import (
	"strings"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// Service coordinates mutable fleet/config operations against the store.
type Service struct {
	store *store.Store
}

// New constructs a service layer backed by st.
func New(st *store.Store) *Service {
	return &Service{store: st}
}

func (s *Service) UpsertAgent(a fleet.Agent) error {
	if strings.TrimSpace(a.Name) == "" {
		return &store.ErrValidation{Msg: "name is required"}
	}
	if strings.TrimSpace(a.PromptID) != "" {
		// prompt_id is the persisted reference. prompt_ref/prompt_scope are
		// selectors accepted at the service boundary and resolved by store.
		a.PromptRef = ""
		a.PromptScope = ""
	}
	if strings.TrimSpace(a.PromptRef) == "" && strings.TrimSpace(a.PromptID) == "" {
		return &store.ErrValidation{Msg: "prompt_id or prompt_ref is required"}
	}
	return s.store.UpsertAgent(a)
}
func (s *Service) DeleteWorkspaceAgent(workspace, name string) error {
	return s.store.DeleteWorkspaceAgent(workspace, name)
}
func (s *Service) DeleteWorkspaceAgentCascade(workspace, name string) error {
	return s.store.DeleteWorkspaceAgentCascade(workspace, name)
}

func (s *Service) UpsertSkill(name string, sk fleet.Skill) error {
	if strings.TrimSpace(name) == "" && strings.TrimSpace(sk.Name) == "" {
		return &store.ErrValidation{Msg: "name is required"}
	}
	return s.store.UpsertSkill(name, sk)
}
func (s *Service) DeleteSkill(name string) error { return s.store.DeleteSkill(name) }

func (s *Service) UpsertPrompt(p fleet.Prompt) (fleet.Prompt, error) {
	return s.store.UpsertPrompt(p)
}
func (s *Service) DeletePrompt(ref string) error { return s.store.DeletePrompt(ref) }

func (s *Service) UpsertBackend(name string, b fleet.Backend) error {
	if strings.TrimSpace(name) == "" {
		return &store.ErrValidation{Msg: "name is required"}
	}
	return s.store.UpsertBackend(name, b)
}
func (s *Service) DeleteBackend(name string) error { return s.store.DeleteBackend(name) }

func (s *Service) UpsertGuardrail(g fleet.Guardrail) error {
	if strings.TrimSpace(g.Name) == "" {
		return &store.ErrValidation{Msg: "name is required"}
	}
	return s.store.UpsertGuardrail(g)
}
func (s *Service) DeleteGuardrail(name string) error { return s.store.DeleteGuardrail(name) }
func (s *Service) ResetGuardrail(name string) error  { return s.store.ResetGuardrail(name) }

func (s *Service) UpsertWorkspace(w fleet.Workspace) (fleet.Workspace, error) {
	return s.store.UpsertWorkspace(w)
}
func (s *Service) DeleteWorkspace(workspace string) error {
	return s.store.DeleteWorkspace(workspace)
}
func (s *Service) SetWorkspaceRunnerImage(workspace, image string) (fleet.Workspace, error) {
	return s.store.SetWorkspaceRunnerImage(workspace, image)
}
func (s *Service) ReplaceWorkspaceGuardrails(workspace string, refs []fleet.WorkspaceGuardrailRef) ([]fleet.WorkspaceGuardrailRef, error) {
	return s.store.ReplaceWorkspaceGuardrails(workspace, refs)
}

func (s *Service) UpsertRepo(r fleet.Repo) error {
	if err := fleet.ValidateRepoName(r.Name); err != nil {
		return &store.ErrValidation{Msg: err.Error()}
	}
	return s.store.UpsertRepo(r)
}
func (s *Service) EnableWorkspaceRepo(workspace, repo string, enabled bool) error {
	return s.store.EnableWorkspaceRepo(workspace, repo, enabled)
}
func (s *Service) DeleteWorkspaceRepo(workspace, repo string) error {
	return s.store.DeleteWorkspaceRepo(workspace, repo)
}
func (s *Service) CreateWorkspaceBinding(workspace, repo string, b fleet.Binding) (int64, fleet.Binding, error) {
	return s.store.CreateWorkspaceBinding(workspace, repo, b)
}
func (s *Service) UpdateBinding(id int64, b fleet.Binding) (fleet.Binding, error) {
	return s.store.UpdateBinding(id, b)
}
func (s *Service) DeleteBinding(id int64) error { return s.store.DeleteBinding(id) }

func (s *Service) WriteRuntimeSettings(settings fleet.RuntimeSettings) (fleet.RuntimeSettings, error) {
	return s.store.WriteRuntimeSettings(settings)
}
func (s *Service) PatchRuntimeSettings(patch store.RuntimeSettingsPatch) (fleet.RuntimeSettings, error) {
	return s.store.PatchRuntimeSettings(patch)
}
func (s *Service) ImportConfig(cfg *config.Config, budgets []store.TokenBudget) error {
	return s.store.ImportConfig(cfg, budgets)
}
func (s *Service) ReplaceConfig(cfg *config.Config, budgets []store.TokenBudget) error {
	return s.store.ReplaceConfig(cfg, budgets)
}

func (s *Service) CreateTokenBudget(b store.TokenBudget) (store.TokenBudget, error) {
	return s.store.CreateTokenBudget(b)
}
func (s *Service) PatchTokenBudget(id int64, patch store.TokenBudgetPatch) (store.TokenBudget, error) {
	return s.store.PatchTokenBudget(id, patch)
}
func (s *Service) DeleteTokenBudget(id int64) error { return s.store.DeleteTokenBudget(id) }
