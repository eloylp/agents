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
	return s.withDeleteTx("delete agent", func(tx *sql.Tx) error {
		return store.DeleteWorkspaceAgentTx(tx, workspace, name, false)
	})
}
func (s *Service) DeleteWorkspaceAgentCascade(workspace, name string) error {
	return s.withDeleteTx("delete agent cascade", func(tx *sql.Tx) error {
		return store.DeleteWorkspaceAgentTx(tx, workspace, name, true)
	})
}

func (s *Service) UpsertSkill(name string, sk fleet.Skill) error {
	if strings.TrimSpace(name) == "" && strings.TrimSpace(sk.Name) == "" {
		return &store.ErrValidation{Msg: "name is required"}
	}
	return s.withTx("upsert skill", func(tx *sql.Tx) error {
		return store.UpsertSkillTx(tx, name, sk)
	})
}
func (s *Service) DeleteSkill(name string) error {
	return s.withDeleteTx("delete skill", func(tx *sql.Tx) error {
		return store.DeleteSkillTx(tx, name)
	})
}

func (s *Service) UpsertPrompt(p fleet.Prompt) (fleet.Prompt, error) {
	var saved fleet.Prompt
	err := s.withTx("upsert prompt", func(tx *sql.Tx) error {
		var err error
		saved, err = store.UpsertPromptTx(tx, p)
		return err
	})
	return saved, err
}
func (s *Service) DeletePrompt(ref string) error {
	return s.withDeleteTx("delete prompt", func(tx *sql.Tx) error {
		return store.DeletePromptTx(tx, ref)
	})
}

func (s *Service) UpsertBackend(name string, b fleet.Backend) error {
	if strings.TrimSpace(name) == "" {
		return &store.ErrValidation{Msg: "name is required"}
	}
	return s.withTx("upsert backend", func(tx *sql.Tx) error {
		return store.UpsertBackendTx(tx, name, b)
	})
}
func (s *Service) DeleteBackend(name string) error {
	return s.withDeleteTx("delete backend", func(tx *sql.Tx) error {
		return store.DeleteBackendTx(tx, name)
	})
}

func (s *Service) UpsertGuardrail(g fleet.Guardrail) error {
	if strings.TrimSpace(g.Name) == "" {
		return &store.ErrValidation{Msg: "name is required"}
	}
	return s.withTx("upsert guardrail", func(tx *sql.Tx) error {
		return store.UpsertGuardrailTx(tx, g)
	})
}
func (s *Service) DeleteGuardrail(name string) error {
	return s.withTx("delete guardrail", func(tx *sql.Tx) error {
		return store.DeleteGuardrailTx(tx, name)
	})
}
func (s *Service) ResetGuardrail(name string) error {
	return s.withTx("reset guardrail", func(tx *sql.Tx) error {
		return store.ResetGuardrailTx(tx, name)
	})
}

func (s *Service) UpsertWorkspace(w fleet.Workspace) (fleet.Workspace, error) {
	var saved fleet.Workspace
	err := s.withRawTx("upsert workspace", func(tx *sql.Tx) error {
		var err error
		saved, err = store.UpsertWorkspaceTx(tx, w)
		return err
	})
	return saved, err
}
func (s *Service) DeleteWorkspace(workspace string) error {
	return s.withRawTx("delete workspace", func(tx *sql.Tx) error {
		return store.DeleteWorkspaceTx(tx, workspace)
	})
}
func (s *Service) SetWorkspaceRunnerImage(workspace, image string) (fleet.Workspace, error) {
	var saved fleet.Workspace
	err := s.withRawTx("set workspace runner image", func(tx *sql.Tx) error {
		if err := store.SetWorkspaceRunnerImageTx(tx, workspace, image); err != nil {
			return err
		}
		var err error
		saved, err = store.ReadWorkspace(tx, workspace)
		return err
	})
	return saved, err
}
func (s *Service) ReplaceWorkspaceGuardrails(workspace string, refs []fleet.WorkspaceGuardrailRef) ([]fleet.WorkspaceGuardrailRef, error) {
	var saved []fleet.WorkspaceGuardrailRef
	err := s.withRawTx("replace workspace guardrails", func(tx *sql.Tx) error {
		var err error
		saved, err = store.ReplaceWorkspaceGuardrailsTx(tx, workspace, refs)
		return err
	})
	return saved, err
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
	return s.withTx("enable repo", func(tx *sql.Tx) error {
		return store.EnableWorkspaceRepoTx(tx, workspace, repo, enabled)
	})
}
func (s *Service) DeleteWorkspaceRepo(workspace, repo string) error {
	return s.withDeleteTx("delete repo", func(tx *sql.Tx) error {
		return store.DeleteWorkspaceRepoTx(tx, workspace, repo)
	})
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
	if err := validateFleetTx(tx); err != nil {
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
	if err := validateFleetTx(tx); err != nil {
		return fleet.Binding{}, err
	}
	if err := tx.Commit(); err != nil {
		return fleet.Binding{}, fmt.Errorf("service: update binding: commit: %w", err)
	}
	return updated, nil
}
func (s *Service) DeleteBinding(id int64) error {
	return s.withDeleteTx("delete binding", func(tx *sql.Tx) error {
		return store.DeleteBindingTx(tx, id)
	})
}

func (s *Service) WriteRuntimeSettings(settings fleet.RuntimeSettings) (fleet.RuntimeSettings, error) {
	var saved fleet.RuntimeSettings
	err := s.withRawTx("write runtime settings", func(tx *sql.Tx) error {
		var err error
		saved, err = store.WriteRuntimeSettingsTx(tx, settings)
		return err
	})
	return saved, err
}
func (s *Service) PatchRuntimeSettings(patch store.RuntimeSettingsPatch) (fleet.RuntimeSettings, error) {
	var saved fleet.RuntimeSettings
	err := s.withRawTx("patch runtime settings", func(tx *sql.Tx) error {
		var err error
		saved, err = store.PatchRuntimeSettingsTx(tx, patch)
		return err
	})
	return saved, err
}
func (s *Service) ImportConfig(cfg *config.Config, budgets []store.TokenBudget) error {
	return s.withRawTx("import config", func(tx *sql.Tx) error {
		if err := store.ImportConfigTx(tx, cfg, budgets); err != nil {
			return err
		}
		return validateFleetForCompleteConfigTx(tx)
	})
}
func (s *Service) ReplaceConfig(cfg *config.Config, budgets []store.TokenBudget) error {
	return s.withRawTx("replace config", func(tx *sql.Tx) error {
		if err := store.ReplaceConfigTx(tx, cfg, budgets); err != nil {
			return err
		}
		return validateFleetForCompleteConfigTx(tx)
	})
}

func (s *Service) CreateTokenBudget(b store.TokenBudget) (store.TokenBudget, error) {
	var saved store.TokenBudget
	err := s.withRawTx("create token budget", func(tx *sql.Tx) error {
		var err error
		saved, err = store.CreateTokenBudgetTx(tx, b)
		return err
	})
	return saved, err
}
func (s *Service) PatchTokenBudget(id int64, patch store.TokenBudgetPatch) (store.TokenBudget, error) {
	var saved store.TokenBudget
	err := s.withRawTx("patch token budget", func(tx *sql.Tx) error {
		var err error
		saved, err = store.PatchTokenBudgetTx(tx, id, patch)
		return err
	})
	return saved, err
}
func (s *Service) DeleteTokenBudget(id int64) error {
	return s.withRawTx("delete token budget", func(tx *sql.Tx) error {
		return store.DeleteTokenBudgetTx(tx, id)
	})
}

func (s *Service) withTx(op string, fn func(*sql.Tx) error) error {
	return s.withRawTx(op, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return validateFleetTx(tx)
	})
}

func (s *Service) withDeleteTx(op string, fn func(*sql.Tx) error) error {
	return s.withRawTx(op, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if err := validateFleetTx(tx); err != nil {
			return &store.ErrConflict{Msg: err.Error()}
		}
		if err := validateFleetMinimumsTx(tx); err != nil {
			return &store.ErrConflict{Msg: err.Error()}
		}
		return nil
	})
}

func (s *Service) withRawTx(op string, fn func(*sql.Tx) error) error {
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

func validateFleetMinimumsTx(tx *sql.Tx) error {
	cfg, err := store.LoadFleetConfigTx(tx)
	if err != nil {
		return err
	}
	if len(cfg.Agents) == 0 {
		return &store.ErrValidation{Msg: "service: validate fleet: config: at least one agent is required"}
	}
	backends := cfg.Daemon.AIBackends
	if len(backends) == 0 {
		return &store.ErrValidation{Msg: "service: validate fleet: config: at least one backend entry is required"}
	}
	return nil
}

func validateFleetForCompleteConfigTx(tx *sql.Tx) error {
	if err := validateFleetTx(tx); err != nil {
		return err
	}
	return validateFleetMinimumsTx(tx)
}

func validateFleetTx(tx *sql.Tx) error {
	cfg, err := store.LoadFleetConfigTx(tx)
	if err != nil {
		return err
	}
	backends := cfg.Daemon.AIBackends
	if backends == nil {
		backends = map[string]fleet.Backend{}
	}
	skills := cfg.Skills
	if skills == nil {
		skills = map[string]fleet.Skill{}
	}
	if err := config.ValidateEntities(cfg.Agents, cfg.Repos, skills, backends); err != nil {
		return &store.ErrValidation{Msg: fmt.Sprintf("service: validate fleet: %v", err)}
	}
	if err := config.ValidateAgentCatalogVisibility(cfg.Agents, skills); err != nil {
		return &store.ErrValidation{Msg: fmt.Sprintf("service: validate fleet: %v", err)}
	}
	if err := fleet.ValidateRepoCronExpressions(cfg.Repos); err != nil {
		return &store.ErrValidation{Msg: fmt.Sprintf("service: validate fleet: %v", err)}
	}
	return nil
}

func validateBindingShape(b fleet.Binding) error {
	if err := fleet.ValidateBindingShape(b); err != nil {
		return &store.ErrValidation{Msg: err.Error()}
	}
	return nil
}
