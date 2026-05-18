// Package service owns mutable fleet/config use cases.
//
// REST and MCP handlers should decode wire shapes, call this package, then map
// typed errors back to their transport response. The service keeps mutation
// orchestration out of handlers while the store package remains the SQLite
// persistence boundary.
package service

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/robfig/cron/v3"

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
	return s.withTx("upsert agent", func(tx *sql.Tx) error {
		return store.UpsertAgentTx(tx, a)
	})
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
	return s.withTx("upsert repo", func(tx *sql.Tx) error {
		return store.UpsertRepoTx(tx, r)
	})
}
func (s *Service) EnableWorkspaceRepo(workspace, repo string, enabled bool) error {
	return s.store.EnableWorkspaceRepo(workspace, repo, enabled)
}
func (s *Service) DeleteWorkspaceRepo(workspace, repo string) error {
	return s.store.DeleteWorkspaceRepo(workspace, repo)
}
func (s *Service) CreateWorkspaceBinding(workspace, repo string, b fleet.Binding) (int64, fleet.Binding, error) {
	if err := validateBindingShape(b); err != nil {
		return 0, fleet.Binding{}, err
	}
	tx, err := s.store.DB().Begin()
	if err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("service: create binding: begin: %w", err)
	}
	defer tx.Rollback()
	id, created, err := store.CreateWorkspaceBindingTx(tx, workspace, repo, b)
	if err != nil {
		return 0, fleet.Binding{}, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("service: create binding: commit: %w", err)
	}
	return id, created, nil
}
func (s *Service) UpdateBinding(id int64, b fleet.Binding) (fleet.Binding, error) {
	if err := validateBindingShape(b); err != nil {
		return fleet.Binding{}, err
	}
	tx, err := s.store.DB().Begin()
	if err != nil {
		return fleet.Binding{}, fmt.Errorf("service: update binding: begin: %w", err)
	}
	defer tx.Rollback()
	updated, err := store.UpdateBindingTx(tx, id, b)
	if err != nil {
		return fleet.Binding{}, err
	}
	if err := tx.Commit(); err != nil {
		return fleet.Binding{}, fmt.Errorf("service: update binding: commit: %w", err)
	}
	return updated, nil
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

func (s *Service) withTx(op string, fn func(*sql.Tx) error) error {
	tx, err := s.store.DB().Begin()
	if err != nil {
		return fmt.Errorf("service: %s: begin: %w", op, err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("service: %s: commit: %w", op, err)
	}
	return nil
}

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func validateBindingShape(b fleet.Binding) error {
	if strings.TrimSpace(b.Agent) == "" {
		return &store.ErrValidation{Msg: "agent is required"}
	}
	n := b.TriggerCount()
	if n == 0 {
		return &store.ErrValidation{Msg: "binding has no trigger (set cron, labels, or events)"}
	}
	if n > 1 {
		return &store.ErrValidation{Msg: "binding mixes multiple trigger types (labels, events, cron); each binding must use exactly one trigger"}
	}
	if b.IsCron() {
		if _, err := cronParser.Parse(b.Cron); err != nil {
			return &store.ErrValidation{Msg: fmt.Sprintf("invalid cron expression %q: %v", b.Cron, err)}
		}
	}
	return nil
}
