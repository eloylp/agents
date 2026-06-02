package selfimprovement

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

type SelfImprovementProposal = store.SelfImprovementProposal

func CreateSelfImprovementProposal(db *sql.DB, id string) (SelfImprovementProposal, error) {
	rec, err := store.GetSelfImprovementRecommendation(db, id)
	if err != nil {
		return SelfImprovementProposal{}, err
	}
	if rec.Status != store.RecommendationStatusAccepted {
		return SelfImprovementProposal{}, &store.ErrValidation{Msg: "recommendation must be accepted before creating a proposal"}
	}
	if nonConvertibleRecommendationType(rec.Type) {
		return SelfImprovementProposal{}, &store.ErrValidation{Msg: fmt.Sprintf("recommendation type %q is not proposal-convertible", rec.Type)}
	}
	if existing, err := store.ListSelfImprovementProposals(db, rec.ID); err != nil {
		return SelfImprovementProposal{}, err
	} else if len(existing) > 0 {
		return existing[0], nil
	}
	targetType := strings.TrimSpace(rec.TargetAssetType)
	targetID := strings.TrimSpace(rec.TargetAssetID)
	if targetID == "" {
		return SelfImprovementProposal{}, &store.ErrValidation{Msg: "recommendation has no target catalog asset"}
	}
	if strings.TrimSpace(rec.ProposedNewBody) == "" {
		return SelfImprovementProposal{}, &store.ErrValidation{Msg: "recommendation has no proposed catalog body"}
	}
	meta := fleet.CatalogVersionMetadata{
		State:      "proposal",
		SourceType: "feedback_recommendation",
		SourceRef:  rec.ID,
		Author:     "agents-assistant",
		Changelog:  recommendationProposalChangelog(rec),
	}
	var version fleet.CatalogVersion
	switch targetType {
	case "prompt":
		prompt, err := readPromptFrom(db, targetID)
		if err != nil {
			return SelfImprovementProposal{}, err
		}
		if err := ensureRecommendationBaseVersion(rec, prompt.VersionID); err != nil {
			return SelfImprovementProposal{}, err
		}
		tx, err := db.Begin()
		if err != nil {
			return SelfImprovementProposal{}, fmt.Errorf("selfimprovement: create proposal: begin: %w", err)
		}
		defer tx.Rollback()
		version, err = store.CreatePromptDraftTx(tx, prompt.ID, prompt.Description, rec.ProposedNewBody, meta)
		if err != nil {
			return SelfImprovementProposal{}, err
		}
		if err := tx.Commit(); err != nil {
			return SelfImprovementProposal{}, fmt.Errorf("selfimprovement: create proposal: commit: %w", err)
		}
	case "skill":
		skill, err := readSkill(db, targetID)
		if err != nil {
			return SelfImprovementProposal{}, err
		}
		if err := ensureRecommendationBaseVersion(rec, skill.VersionID); err != nil {
			return SelfImprovementProposal{}, err
		}
		tx, err := db.Begin()
		if err != nil {
			return SelfImprovementProposal{}, fmt.Errorf("selfimprovement: create proposal: begin: %w", err)
		}
		defer tx.Rollback()
		version, err = store.CreateSkillDraftTx(tx, skill.ID, rec.ProposedNewBody, meta)
		if err != nil {
			return SelfImprovementProposal{}, err
		}
		if err := tx.Commit(); err != nil {
			return SelfImprovementProposal{}, fmt.Errorf("selfimprovement: create proposal: commit: %w", err)
		}
	case "guardrail":
		guardrail, err := getGuardrailFrom(db, targetID)
		if err != nil {
			return SelfImprovementProposal{}, err
		}
		if err := ensureRecommendationBaseVersion(rec, guardrail.VersionID); err != nil {
			return SelfImprovementProposal{}, err
		}
		guardrail.Content = rec.ProposedNewBody
		tx, err := db.Begin()
		if err != nil {
			return SelfImprovementProposal{}, fmt.Errorf("selfimprovement: create proposal: begin: %w", err)
		}
		defer tx.Rollback()
		version, err = store.CreateGuardrailDraftTx(tx, guardrail.ID, guardrail, meta)
		if err != nil {
			return SelfImprovementProposal{}, err
		}
		if err := tx.Commit(); err != nil {
			return SelfImprovementProposal{}, fmt.Errorf("selfimprovement: create proposal: commit: %w", err)
		}
	default:
		return SelfImprovementProposal{}, &store.ErrValidation{Msg: fmt.Sprintf("recommendation target type %q is not proposal-convertible", targetType)}
	}
	return SelfImprovementProposal{
		RecommendationID: rec.ID,
		TargetAssetType:  targetType,
		TargetAssetID:    targetID,
		BaseVersionID:    version.BaseVersionID,
		Version:          version,
	}, nil
}

func ensureRecommendationBaseVersion(rec store.SelfImprovementRecommendation, currentVersionID string) error {
	if rec.TargetBaseVersionID == "" {
		return &store.ErrValidation{Msg: "recommendation base version is required; re-analyze feedback before creating a proposal"}
	}
	if rec.TargetBaseVersionID != currentVersionID {
		return &store.ErrValidation{Msg: "recommendation base version is stale; re-analyze feedback before creating a proposal"}
	}
	return nil
}
