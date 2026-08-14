package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/eloylp/agents/internal/catalog"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func (s *Service) UpsertSkill(name string, sk fleet.Skill) error {
	if strings.TrimSpace(name) == "" && strings.TrimSpace(sk.Name) == "" {
		return &store.ErrValidation{Msg: "name is required"}
	}
	return s.withTx("upsert skill", func(tx *sql.Tx) error {
		if err := rejectCatalogDelegatedTx(tx); err != nil {
			return err
		}
		return store.UpsertSkillTx(tx, name, sk)
	})
}

func (s *Service) DeleteSkill(name string) error {
	return s.withDeleteTx("delete skill", func(tx *sql.Tx) error {
		if err := rejectCatalogDelegatedTx(tx); err != nil {
			return err
		}
		return store.DeleteSkillTx(tx, name)
	})
}

func (s *Service) UpsertPrompt(p fleet.Prompt) (fleet.Prompt, error) {
	var saved fleet.Prompt
	err := s.withTx("upsert prompt", func(tx *sql.Tx) error {
		if err := rejectCatalogDelegatedTx(tx); err != nil {
			return err
		}
		var err error
		saved, err = store.UpsertPromptTx(tx, p)
		return err
	})
	return saved, err
}

func (s *Service) DeletePrompt(ref string) error {
	return s.withDeleteTx("delete prompt", func(tx *sql.Tx) error {
		if err := rejectCatalogDelegatedTx(tx); err != nil {
			return err
		}
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
		if err := rejectCatalogDelegatedTx(tx); err != nil {
			return err
		}
		return store.UpsertGuardrailTx(tx, g)
	})
}

func (s *Service) DeleteGuardrail(name string) error {
	return s.withTx("delete guardrail", func(tx *sql.Tx) error {
		if err := rejectCatalogDelegatedTx(tx); err != nil {
			return err
		}
		return store.DeleteGuardrailTx(tx, name)
	})
}

func (s *Service) ResetGuardrail(name string) error {
	return s.withTx("reset guardrail", func(tx *sql.Tx) error {
		if err := rejectCatalogDelegatedTx(tx); err != nil {
			return err
		}
		return store.ResetGuardrailTx(tx, name)
	})
}

func rejectCatalogDelegatedTx(tx *sql.Tx) error {
	cfg, err := store.ReadCatalogDelegationConfigTx(tx)
	if err != nil {
		return err
	}
	if cfg.Enabled {
		return &store.ErrCatalogDelegated{}
	}
	return nil
}

func (s *Service) ApplyDelegatedCatalog(file catalog.File, commitSHA string) error {
	if err := catalog.Validate(file); err != nil {
		return err
	}
	return s.withRawTx("apply delegated catalog", func(tx *sql.Tx) error {
		if err := replaceDelegatedCatalogTx(tx, file); err != nil {
			return err
		}
		status := "synced"
		sha := strings.TrimSpace(commitSHA)
		syncedAt := time.Now().UTC().Format(time.RFC3339)
		_, err := store.PatchCatalogDelegationConfigTx(tx, store.CatalogDelegationPatch{
			LastSyncedCommit:     &sha,
			LastSuccessfulSyncAt: &syncedAt,
			LastSyncStatus:       &status,
			LastSyncError:        ptrString(""),
		})
		return err
	})
}

const (
	catalogDelegationResumeFromRepo       = "resume_from_repo"
	catalogDelegationOverwriteRepoFromSQL = "overwrite_repo_from_sqlite"
)

func (s *Service) ActivateCatalogDelegation(ctx context.Context, patch store.CatalogDelegationPatch, reenableMode string) (fleet.CatalogDelegationConfig, error) {
	disabled := false
	pending := "pending"
	clear := ""
	patch.Enabled = &disabled
	patch.LastSyncStatus = &pending
	patch.LastSyncError = &clear

	var cfg fleet.CatalogDelegationConfig
	var token string
	if err := s.withRawTx("prepare catalog delegation", func(tx *sql.Tx) error {
		var err error
		cfg, err = store.PatchCatalogDelegationConfigTx(tx, patch)
		if err != nil {
			return err
		}
		token, err = store.ReadCatalogDelegationCredentialTx(tx)
		return err
	}); err != nil {
		return fleet.CatalogDelegationConfig{}, err
	}
	if strings.TrimSpace(token) == "" {
		err := &store.ErrValidation{Msg: "catalog delegation credential is required for activation"}
		_ = s.markCatalogDelegationSyncError(err)
		return fleet.CatalogDelegationConfig{}, err
	}

	ghCfg := catalogGitHubConfig{
		Repo:        cfg.Repo,
		Branch:      cfg.Branch,
		CatalogPath: cfg.CatalogPath,
		Token:       token,
	}
	head, err := s.github.BranchHead(ctx, ghCfg)
	if err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return fleet.CatalogDelegationConfig{}, err
	}
	if cfg.LastSyncedCommit != "" && head != cfg.LastSyncedCommit {
		switch reenableMode {
		case catalogDelegationResumeFromRepo:
			return s.resumeCatalogDelegationFromRepo(ctx, ghCfg, head)
		case catalogDelegationOverwriteRepoFromSQL:
		default:
			err := &store.ErrValidation{Msg: "catalog delegation re-enable requires resume_from_repo or overwrite_repo_from_sqlite because GitHub changed while disabled"}
			_ = s.markCatalogDelegationSyncError(err)
			return fleet.CatalogDelegationConfig{}, err
		}
	}
	file, err := s.currentCatalogFile()
	if err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return fleet.CatalogDelegationConfig{}, err
	}
	content, err := catalog.Marshal(file)
	if err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return fleet.CatalogDelegationConfig{}, err
	}
	_, blobSHA, err := s.github.ReadFile(ctx, ghCfg, cfg.Branch)
	if err != nil && !isGitHubNotFound(err) {
		_ = s.markCatalogDelegationSyncError(err)
		return fleet.CatalogDelegationConfig{}, err
	}
	commitSHA, err := s.github.WriteFile(ctx, ghCfg, "Export agents intelligence catalog", content, blobSHA)
	if err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return fleet.CatalogDelegationConfig{}, err
	}

	enabled := true
	status := "synced"
	syncedAt := time.Now().UTC().Format(time.RFC3339)
	if err := s.withRawTx("enable catalog delegation", func(tx *sql.Tx) error {
		var err error
		cfg, err = store.PatchCatalogDelegationConfigTx(tx, store.CatalogDelegationPatch{
			Enabled:              &enabled,
			LastSyncedCommit:     &commitSHA,
			LastSuccessfulSyncAt: &syncedAt,
			LastSyncStatus:       &status,
			LastSyncError:        &clear,
			DisabledAt:           &clear,
		})
		return err
	}); err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return fleet.CatalogDelegationConfig{}, err
	}
	return cfg, nil
}

func (s *Service) resumeCatalogDelegationFromRepo(ctx context.Context, ghCfg catalogGitHubConfig, head string) (fleet.CatalogDelegationConfig, error) {
	content, _, err := s.github.ReadFile(ctx, ghCfg, head)
	if err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return fleet.CatalogDelegationConfig{}, err
	}
	file, err := catalog.Parse(content)
	if err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return fleet.CatalogDelegationConfig{}, err
	}
	if err := s.ApplyDelegatedCatalog(file, head); err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return fleet.CatalogDelegationConfig{}, err
	}
	enabled := true
	clear := ""
	status := "synced"
	syncedAt := time.Now().UTC().Format(time.RFC3339)
	var cfg fleet.CatalogDelegationConfig
	if err := s.withRawTx("enable resumed catalog delegation", func(tx *sql.Tx) error {
		var err error
		cfg, err = store.PatchCatalogDelegationConfigTx(tx, store.CatalogDelegationPatch{
			Enabled:              &enabled,
			LastSuccessfulSyncAt: &syncedAt,
			LastSyncStatus:       &status,
			LastSyncError:        &clear,
			DisabledAt:           &clear,
		})
		return err
	}); err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return fleet.CatalogDelegationConfig{}, err
	}
	return cfg, nil
}

func (s *Service) SyncDelegatedCatalog(ctx context.Context) (fleet.CatalogDelegationConfig, error) {
	cfg, token, err := s.readDelegationConfigAndCredential()
	if err != nil {
		return fleet.CatalogDelegationConfig{}, err
	}
	if !cfg.Enabled {
		return cfg, nil
	}
	ghCfg := catalogGitHubConfig{
		Repo:        cfg.Repo,
		Branch:      cfg.Branch,
		CatalogPath: cfg.CatalogPath,
		Token:       token,
	}
	head, err := s.github.BranchHead(ctx, ghCfg)
	if err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return cfg, err
	}
	if head == cfg.LastSyncedCommit {
		return cfg, nil
	}
	content, _, err := s.github.ReadFile(ctx, ghCfg, head)
	if err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return cfg, err
	}
	file, err := catalog.Parse(content)
	if err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return cfg, err
	}
	if err := s.ApplyDelegatedCatalog(file, head); err != nil {
		_ = s.markCatalogDelegationSyncError(err)
		return cfg, err
	}
	updated, _, err := s.readDelegationConfigAndCredential()
	if err != nil {
		return fleet.CatalogDelegationConfig{}, err
	}
	return updated, nil
}

func (s *Service) PatchCatalogDelegationConfig(patch store.CatalogDelegationPatch) (fleet.CatalogDelegationConfig, error) {
	var cfg fleet.CatalogDelegationConfig
	err := s.withRawTx("patch catalog delegation", func(tx *sql.Tx) error {
		var err error
		if patch.Enabled != nil && !*patch.Enabled {
			disabledAt := time.Now().UTC().Format(time.RFC3339)
			status := "disabled"
			clear := ""
			patch.DisabledAt = &disabledAt
			patch.LastSyncStatus = &status
			patch.LastSyncError = &clear
		}
		cfg, err = store.PatchCatalogDelegationConfigTx(tx, patch)
		return err
	})
	return cfg, err
}

func (s *Service) currentCatalogFile() (catalog.File, error) {
	prompts, err := store.ReadPrompts(s.store.DB())
	if err != nil {
		return catalog.File{}, err
	}
	skills, err := store.ReadSkills(s.store.DB())
	if err != nil {
		return catalog.File{}, err
	}
	guardrails, err := store.ReadAllGuardrails(s.store.DB())
	if err != nil {
		return catalog.File{}, err
	}
	return catalog.ToFile(prompts, skills, guardrails), nil
}

func (s *Service) readDelegationConfigAndCredential() (fleet.CatalogDelegationConfig, string, error) {
	var cfg fleet.CatalogDelegationConfig
	var token string
	err := s.withRawTx("read catalog delegation", func(tx *sql.Tx) error {
		var err error
		cfg, err = store.ReadCatalogDelegationConfigTx(tx)
		if err != nil {
			return err
		}
		token, err = store.ReadCatalogDelegationCredentialTx(tx)
		return err
	})
	return cfg, token, err
}

func (s *Service) markCatalogDelegationSyncError(syncErr error) error {
	status := "error"
	msg := syncErr.Error()
	return s.withRawTx("mark catalog delegation sync error", func(tx *sql.Tx) error {
		_, err := store.PatchCatalogDelegationConfigTx(tx, store.CatalogDelegationPatch{
			LastSyncStatus: &status,
			LastSyncError:  &msg,
		})
		return err
	})
}

func replaceDelegatedCatalogTx(tx *sql.Tx, file catalog.File) error {
	seenPrompts := map[string]struct{}{}
	seenSkills := map[string]struct{}{}
	seenGuardrails := map[string]struct{}{}
	for _, asset := range file.Assets {
		switch asset.Kind {
		case "prompt":
			seenPrompts[asset.ID] = struct{}{}
			if _, err := store.UpsertPromptTx(tx, fleet.Prompt{
				ID:          asset.ID,
				WorkspaceID: asset.WorkspaceID,
				Repo:        asset.Repo,
				Name:        asset.Name,
				Description: asset.Description,
				Content:     asset.Body,
			}); err != nil {
				return err
			}
		case "skill":
			seenSkills[asset.ID] = struct{}{}
			if err := store.UpsertSkillTx(tx, asset.ID, fleet.Skill{
				ID:          asset.ID,
				WorkspaceID: asset.WorkspaceID,
				Repo:        asset.Repo,
				Name:        asset.Name,
				Prompt:      asset.Body,
			}); err != nil {
				return err
			}
		case "guardrail":
			seenGuardrails[asset.ID] = struct{}{}
			enabled := false
			if asset.Enabled != nil {
				enabled = *asset.Enabled
			}
			position := 0
			if asset.Position != nil {
				position = *asset.Position
			}
			if err := store.UpsertGuardrailTx(tx, fleet.Guardrail{
				ID:          asset.ID,
				WorkspaceID: asset.WorkspaceID,
				Name:        asset.Name,
				Description: asset.Description,
				Content:     asset.Body,
				Enabled:     enabled,
				Position:    position,
			}); err != nil {
				return err
			}
		default:
			return &store.ErrValidation{Msg: fmt.Sprintf("unsupported catalog asset kind %q", asset.Kind)}
		}
	}
	if err := deleteMissingCatalogRefsTx(tx, "prompts", seenPrompts, store.DeletePromptTx); err != nil {
		return err
	}
	if err := deleteMissingCatalogRefsTx(tx, "skills", seenSkills, store.DeleteSkillTx); err != nil {
		return err
	}
	if err := deleteMissingGuardrailsTx(tx, seenGuardrails); err != nil {
		return err
	}
	return validateFleetTx(tx)
}

func deleteMissingCatalogRefsTx(tx *sql.Tx, table string, keep map[string]struct{}, deleteFn func(*sql.Tx, string) error) error {
	rows, err := tx.Query("SELECT ref FROM " + table + " ORDER BY ref")
	if err != nil {
		return fmt.Errorf("service: list delegated %s: %w", table, err)
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return fmt.Errorf("service: scan delegated %s: %w", table, err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("service: scan delegated %s: %w", table, err)
	}
	for _, ref := range refs {
		if _, ok := keep[ref]; !ok {
			if err := deleteFn(tx, ref); err != nil {
				return err
			}
		}
	}
	return nil
}

func deleteMissingGuardrailsTx(tx *sql.Tx, keep map[string]struct{}) error {
	rows, err := tx.Query("SELECT ref FROM guardrails WHERE is_builtin=0 ORDER BY ref")
	if err != nil {
		return fmt.Errorf("service: list delegated guardrails: %w", err)
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return fmt.Errorf("service: scan delegated guardrails: %w", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("service: scan delegated guardrails: %w", err)
	}
	for _, ref := range refs {
		if _, ok := keep[ref]; !ok {
			if err := store.DeleteGuardrailTx(tx, ref); err != nil {
				return err
			}
		}
	}
	return nil
}

func ptrString(s string) *string { return &s }
