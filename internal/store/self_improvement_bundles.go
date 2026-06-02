package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

const (
	ProposalBundleStatusPending   = "pending"
	ProposalBundleStatusPublished = "published"
	ProposalBundleStatusDiscarded = "discarded"

	ProposalBundleOperationUpdateExisting = "update_existing"
	ProposalBundleOperationCreateNew      = "create_new"

	ProposalBundleDecisionPending        = "pending"
	ProposalBundleDecisionAccepted       = "accepted"
	ProposalBundleDecisionRejected       = "rejected"
	ProposalBundleDecisionLinkedExisting = "linked_existing"
	ProposalBundleDecisionPublished      = "published"
	ProposalBundleDecisionDiscarded      = "discarded"
)

type SelfImprovementProposalBundle struct {
	ID                              string                         `json:"id"`
	WorkspaceID                     string                         `json:"workspace"`
	RecommendationID                string                         `json:"recommendation_id"`
	RecommendationUpdatedAtSnapshot string                         `json:"recommendation_updated_at_snapshot"`
	RecommendationSnapshotHash      string                         `json:"recommendation_snapshot_hash"`
	RecommendationChanged           bool                           `json:"recommendation_changed"`
	Status                          string                         `json:"status"`
	CreatedAt                       string                         `json:"created_at"`
	UpdatedAt                       string                         `json:"updated_at"`
	Recommendation                  *SelfImprovementRecommendation `json:"recommendation,omitempty"`
	Items                           []SelfImprovementBundleItem    `json:"items"`
}

type SelfImprovementBundleItem struct {
	ID                  string                `json:"id"`
	BundleID            string                `json:"bundle_id"`
	Operation           string                `json:"operation"`
	AssetType           string                `json:"asset_type"`
	AssetID             string                `json:"asset_id,omitempty"`
	BaseVersionID       string                `json:"base_version_id,omitempty"`
	ProposedRef         string                `json:"proposed_ref,omitempty"`
	ProposedName        string                `json:"proposed_name,omitempty"`
	ProposedScope       string                `json:"proposed_scope,omitempty"`
	ProposedBody        string                `json:"proposed_body"`
	ProposedDescription string                `json:"proposed_description,omitempty"`
	ProposedEnabled     bool                  `json:"proposed_enabled"`
	ProposedPosition    int                   `json:"proposed_position"`
	AnalystProposedBody string                `json:"analyst_proposed_body"`
	DuplicateRisk       string                `json:"duplicate_risk,omitempty"`
	Rationale           string                `json:"rationale,omitempty"`
	Decision            string                `json:"decision"`
	DecisionReason      string                `json:"decision_reason,omitempty"`
	PublishedVersionID  string                `json:"published_version_id,omitempty"`
	CreatedAt           string                `json:"created_at"`
	UpdatedAt           string                `json:"updated_at"`
	BaseVersion         *fleet.CatalogVersion `json:"base_version,omitempty"`
	CurrentVersionID    string                `json:"current_version_id,omitempty"`
	Stale               bool                  `json:"stale"`
}

type SelfImprovementBundleItemInput struct {
	Operation           string `json:"operation"`
	AssetType           string `json:"asset_type"`
	AssetID             string `json:"asset_id"`
	BaseVersionID       string `json:"base_version_id"`
	ProposedRef         string `json:"proposed_ref"`
	ProposedName        string `json:"proposed_name"`
	ProposedScope       string `json:"proposed_scope"`
	ProposedBody        string `json:"proposed_body"`
	ProposedDescription string `json:"proposed_description"`
	ProposedEnabled     *bool  `json:"proposed_enabled"`
	ProposedPosition    int    `json:"proposed_position"`
	DuplicateRisk       string `json:"duplicate_risk"`
	Rationale           string `json:"rationale"`
}

type SelfImprovementBundleItemUpdate struct {
	ProposedRef         *string `json:"proposed_ref"`
	ProposedName        *string `json:"proposed_name"`
	ProposedScope       *string `json:"proposed_scope"`
	ProposedBody        string  `json:"proposed_body"`
	ProposedDescription *string `json:"proposed_description"`
	ProposedEnabled     *bool   `json:"proposed_enabled"`
	ProposedPosition    *int    `json:"proposed_position"`
}

func (s *Store) GetSelfImprovementProposalBundle(id string) (SelfImprovementProposalBundle, error) {
	return GetSelfImprovementProposalBundle(s.db, id)
}

func (s *Store) ListSelfImprovementRecommendationsWithBundles(workspace string, limit int) ([]SelfImprovementRecommendation, error) {
	return ListSelfImprovementRecommendationsWithBundles(s.db, workspace, limit)
}

func GetSelfImprovementProposalBundle(db *sql.DB, id string) (SelfImprovementProposalBundle, error) {
	return getSelfImprovementProposalBundle(db, id)
}

func getSelfImprovementProposalBundle(q querier, id string) (SelfImprovementProposalBundle, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return SelfImprovementProposalBundle{}, &ErrValidation{Msg: "proposal bundle id or recommendation id is required"}
	}
	var bundle SelfImprovementProposalBundle
	err := q.QueryRow(`
		SELECT id, workspace_id, recommendation_id, recommendation_updated_at_snapshot,
		       recommendation_snapshot_hash, status, created_at, updated_at
		FROM self_improvement_proposal_bundles
		WHERE id=? OR recommendation_id=?`, id, id).
		Scan(&bundle.ID, &bundle.WorkspaceID, &bundle.RecommendationID, &bundle.RecommendationUpdatedAtSnapshot,
			&bundle.RecommendationSnapshotHash, &bundle.Status, &bundle.CreatedAt, &bundle.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SelfImprovementProposalBundle{}, &ErrNotFound{Msg: fmt.Sprintf("proposal bundle %q not found", id)}
	}
	if err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: get self-improvement proposal bundle: %w", err)
	}
	items, err := listSelfImprovementProposalBundleItems(q, bundle.ID)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	bundle.Items = items
	return bundle, nil
}

func listSelfImprovementProposalBundleItems(q querier, bundleID string) ([]SelfImprovementBundleItem, error) {
	rows, err := q.Query(`
		SELECT id, bundle_id, operation, asset_type, asset_id, base_version_id, proposed_ref, proposed_name,
		       proposed_scope, proposed_body, proposed_description, proposed_enabled, proposed_position,
		       analyst_proposed_body, duplicate_risk, rationale, decision,
		       decision_reason, published_version_id, created_at, updated_at
		FROM self_improvement_proposal_bundle_items
		WHERE bundle_id=?
		ORDER BY asset_type, id`, bundleID)
	if err != nil {
		return nil, fmt.Errorf("store: list proposal bundle items: %w", err)
	}
	defer rows.Close()
	var out []SelfImprovementBundleItem
	for rows.Next() {
		var item SelfImprovementBundleItem
		var enabled int
		if err := rows.Scan(
			&item.ID, &item.BundleID, &item.Operation, &item.AssetType, &item.AssetID, &item.BaseVersionID,
			&item.ProposedRef, &item.ProposedName, &item.ProposedScope, &item.ProposedBody,
			&item.ProposedDescription, &enabled, &item.ProposedPosition, &item.AnalystProposedBody,
			&item.DuplicateRisk, &item.Rationale, &item.Decision, &item.DecisionReason, &item.PublishedVersionID,
			&item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scan proposal bundle item: %w", err)
		}
		item.ProposedEnabled = enabled != 0
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func ListSelfImprovementRecommendationsWithBundles(db *sql.DB, workspace string, limit int) ([]SelfImprovementRecommendation, error) {
	recs, err := ListSelfImprovementRecommendations(db, workspace, "", limit)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		bundle, err := GetSelfImprovementProposalBundle(db, recs[i].ID)
		if err == nil {
			recs[i].ProposalBundle = &bundle
			continue
		}
		var nf *ErrNotFound
		if !errors.As(err, &nf) {
			return nil, err
		}
	}
	return recs, nil
}
