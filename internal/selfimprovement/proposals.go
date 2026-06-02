package selfimprovement

import (
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

type SelfImprovementProposal = store.SelfImprovementProposal

func createSelfImprovementProposal(st *store.Store, id string) (SelfImprovementProposal, error) {
	rec, err := st.GetSelfImprovementRecommendation(id)
	if err != nil {
		return SelfImprovementProposal{}, err
	}
	if rec.Status != RecommendationStatusAccepted {
		return SelfImprovementProposal{}, &store.ErrValidation{Msg: "recommendation must be accepted before creating a proposal"}
	}
	if nonConvertibleRecommendationType(rec.Type) {
		return SelfImprovementProposal{}, &store.ErrValidation{Msg: fmt.Sprintf("recommendation type %q is not proposal-convertible", rec.Type)}
	}
	if existing, err := st.ListSelfImprovementProposals(rec.ID); err != nil {
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
		if err := st.Transact(func(tx *store.Tx) error {
			prompt, err := store.ReadSelfImprovementPrompt(tx, targetID)
			if err != nil {
				return err
			}
			if err := ensureRecommendationBaseVersion(rec, prompt.VersionID); err != nil {
				return err
			}
			version, err = store.CreatePromptDraftTx(tx, prompt.ID, prompt.Description, rec.ProposedNewBody, meta)
			return err
		}); err != nil {
			return SelfImprovementProposal{}, err
		}
	case "skill":
		if err := st.Transact(func(tx *store.Tx) error {
			skill, err := store.ReadSelfImprovementSkill(tx, targetID)
			if err != nil {
				return err
			}
			if err := ensureRecommendationBaseVersion(rec, skill.VersionID); err != nil {
				return err
			}
			version, err = store.CreateSkillDraftTx(tx, skill.ID, rec.ProposedNewBody, meta)
			return err
		}); err != nil {
			return SelfImprovementProposal{}, err
		}
	case "guardrail":
		if err := st.Transact(func(tx *store.Tx) error {
			guardrail, err := store.ReadSelfImprovementGuardrail(tx, targetID)
			if err != nil {
				return err
			}
			if err := ensureRecommendationBaseVersion(rec, guardrail.VersionID); err != nil {
				return err
			}
			guardrail.Content = rec.ProposedNewBody
			version, err = store.CreateGuardrailDraftTx(tx, guardrail.ID, guardrail, meta)
			return err
		}); err != nil {
			return SelfImprovementProposal{}, err
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
