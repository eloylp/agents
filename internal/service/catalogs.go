package service

import (
	"database/sql"
	"fmt"
	"strings"

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
		_, err := store.PatchCatalogDelegationConfigTx(tx, store.CatalogDelegationPatch{
			LastSyncedCommit: &sha,
			LastSyncStatus:   &status,
			LastSyncError:    ptrString(""),
		})
		return err
	})
}

func (s *Service) PatchCatalogDelegationConfig(patch store.CatalogDelegationPatch) (fleet.CatalogDelegationConfig, error) {
	var cfg fleet.CatalogDelegationConfig
	err := s.withRawTx("patch catalog delegation", func(tx *sql.Tx) error {
		var err error
		cfg, err = store.PatchCatalogDelegationConfigTx(tx, patch)
		return err
	})
	return cfg, err
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
				Name:        asset.Name,
				Description: asset.Description,
				Content:     asset.Body,
			}); err != nil {
				return err
			}
		case "skill":
			seenSkills[asset.ID] = struct{}{}
			if err := store.UpsertSkillTx(tx, asset.ID, fleet.Skill{
				ID:     asset.ID,
				Name:   asset.Name,
				Prompt: asset.Body,
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
	rows, err := tx.Query("SELECT ref FROM " + table + " WHERE workspace_id IS NULL AND repo IS NULL ORDER BY ref")
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
	rows, err := tx.Query("SELECT ref FROM guardrails WHERE workspace_id IS NULL AND is_builtin=0 ORDER BY ref")
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
