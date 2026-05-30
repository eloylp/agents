package service

import (
	"database/sql"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func (s *Service) UpsertSkill(name string, sk fleet.Skill) error {
	if strings.TrimSpace(name) == "" && strings.TrimSpace(sk.Name) == "" {
		return &store.ErrValidation{Msg: "name is required"}
	}
	return s.withTx("upsert skill", func(tx *sql.Tx) error {
		return store.UpsertSkillTx(tx, name, sk)
	})
}

func (s *Service) CreateSkillDraft(ref string, sk fleet.Skill) (fleet.CatalogVersion, error) {
	var version fleet.CatalogVersion
	err := s.withTx("create skill draft", func(tx *sql.Tx) error {
		var err error
		version, err = store.CreateSkillDraftTx(tx, ref, sk.Prompt)
		return err
	})
	return version, err
}

func (s *Service) PublishSkillVersion(versionID string) (string, fleet.Skill, error) {
	var ref string
	var skill fleet.Skill
	err := s.withTx("publish skill version", func(tx *sql.Tx) error {
		var err error
		ref, skill, err = store.PublishSkillVersionTx(tx, versionID)
		return err
	})
	return ref, skill, err
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

func (s *Service) CreatePromptDraft(ref string, p fleet.Prompt) (fleet.CatalogVersion, error) {
	var version fleet.CatalogVersion
	err := s.withTx("create prompt draft", func(tx *sql.Tx) error {
		var err error
		version, err = store.CreatePromptDraftTx(tx, ref, p.Description, p.Content)
		return err
	})
	return version, err
}

func (s *Service) PublishPromptVersion(versionID string) (fleet.Prompt, error) {
	var prompt fleet.Prompt
	err := s.withTx("publish prompt version", func(tx *sql.Tx) error {
		var err error
		prompt, err = store.PublishPromptVersionTx(tx, versionID)
		return err
	})
	return prompt, err
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

func (s *Service) CreateGuardrailDraft(ref string, g fleet.Guardrail) (fleet.CatalogVersion, error) {
	var version fleet.CatalogVersion
	err := s.withTx("create guardrail draft", func(tx *sql.Tx) error {
		var err error
		version, err = store.CreateGuardrailDraftTx(tx, ref, g)
		return err
	})
	return version, err
}

func (s *Service) PublishGuardrailVersion(versionID string) (fleet.Guardrail, error) {
	var guardrail fleet.Guardrail
	err := s.withTx("publish guardrail version", func(tx *sql.Tx) error {
		var err error
		guardrail, err = store.PublishGuardrailVersionTx(tx, versionID)
		return err
	})
	return guardrail, err
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
