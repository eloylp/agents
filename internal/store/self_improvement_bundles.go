package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

type SelfImprovementProposalBundleRow struct {
	ID                              string                            `json:"id"`
	WorkspaceID                     string                            `json:"workspace"`
	RecommendationID                string                            `json:"recommendation_id"`
	RecommendationUpdatedAtSnapshot string                            `json:"recommendation_updated_at_snapshot"`
	RecommendationSnapshotHash      string                            `json:"recommendation_snapshot_hash"`
	RecommendationChanged           bool                              `json:"recommendation_changed"`
	Status                          string                            `json:"status"`
	CreatedAt                       string                            `json:"created_at"`
	UpdatedAt                       string                            `json:"updated_at"`
	Recommendation                  *SelfImprovementRecommendationRow `json:"recommendation,omitempty"`
	Items                           []SelfImprovementBundleItemRow    `json:"items"`
}

type SelfImprovementBundleItemRow struct {
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

type SelfImprovementBundleItemInputRow struct {
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

type SelfImprovementBundleItemUpdateRow struct {
	ProposedRef         *string `json:"proposed_ref"`
	ProposedName        *string `json:"proposed_name"`
	ProposedScope       *string `json:"proposed_scope"`
	ProposedBody        string  `json:"proposed_body"`
	ProposedDescription *string `json:"proposed_description"`
	ProposedEnabled     *bool   `json:"proposed_enabled"`
	ProposedPosition    *int    `json:"proposed_position"`
}

type SelfImprovementProposalBundleItemEventRow struct {
	BundleID   string
	ItemID     string
	EventType  string
	Actor      string
	Reason     string
	BeforeJSON string
	AfterJSON  string
}

func InsertSelfImprovementProposalBundleRow(tx *Tx, bundle SelfImprovementProposalBundleRow) error {
	if _, err := tx.Exec(`
		INSERT INTO self_improvement_proposal_bundles (
			id, workspace_id, recommendation_id, recommendation_updated_at_snapshot, recommendation_snapshot_hash
		) VALUES (?, ?, ?, ?, ?)`, bundle.ID, bundle.WorkspaceID, bundle.RecommendationID, bundle.RecommendationUpdatedAtSnapshot, bundle.RecommendationSnapshotHash); err != nil {
		return fmt.Errorf("store: create proposal bundle: %w", err)
	}
	return nil
}

func InsertSelfImprovementProposalBundleItemRow(tx *Tx, item SelfImprovementBundleItemRow) error {
	if _, err := tx.Exec(`
		INSERT INTO self_improvement_proposal_bundle_items (
			id, bundle_id, operation, asset_type, asset_id, base_version_id, proposed_ref, proposed_name,
			proposed_scope, proposed_body, proposed_description, proposed_enabled, proposed_position,
			analyst_proposed_body, duplicate_risk, rationale, decision
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.BundleID, item.Operation, item.AssetType, item.AssetID, item.BaseVersionID,
		item.ProposedRef, item.ProposedName, item.ProposedScope, item.ProposedBody, item.ProposedDescription,
		boolInt(item.ProposedEnabled), item.ProposedPosition, item.AnalystProposedBody,
		item.DuplicateRisk, item.Rationale, item.Decision,
	); err != nil {
		return fmt.Errorf("store: create proposal bundle item: %w", err)
	}
	return nil
}

func UpdateSelfImprovementProposalBundleItemDraftRow(tx *Tx, bundleID string, item SelfImprovementBundleItemRow) error {
	if _, err := tx.Exec(`
		UPDATE self_improvement_proposal_bundle_items
		SET proposed_ref=?, proposed_name=?, proposed_scope=?, proposed_body=?,
		    proposed_description=?, proposed_enabled=?, proposed_position=?, updated_at=datetime('now')
		WHERE id=? AND bundle_id=?`, item.ProposedRef, item.ProposedName, item.ProposedScope, item.ProposedBody,
		item.ProposedDescription, boolInt(item.ProposedEnabled), item.ProposedPosition, item.ID, bundleID); err != nil {
		return fmt.Errorf("store: update proposal bundle item: %w", err)
	}
	return nil
}

func UpdateSelfImprovementProposalBundleItemDecisionRow(tx *Tx, bundleID string, item SelfImprovementBundleItemRow) error {
	if _, err := tx.Exec(`
		UPDATE self_improvement_proposal_bundle_items
		SET decision=?, asset_id=CASE WHEN ? <> '' THEN ? ELSE asset_id END, decision_reason=?, updated_at=datetime('now')
		WHERE id=? AND bundle_id=?`, item.Decision, item.AssetID, item.AssetID, item.DecisionReason, item.ID, bundleID); err != nil {
		return fmt.Errorf("store: decide proposal bundle item: %w", err)
	}
	return nil
}

func MarkSelfImprovementProposalBundleItemPublishedRow(tx *Tx, itemID, versionID, decision string) error {
	if _, err := tx.Exec(`
		UPDATE self_improvement_proposal_bundle_items
		SET decision=?, published_version_id=?, updated_at=datetime('now')
		WHERE id=?`, decision, versionID, itemID); err != nil {
		return fmt.Errorf("store: mark proposal bundle item published: %w", err)
	}
	return nil
}

func UpdateSelfImprovementProposalBundleStatusRow(tx *Tx, bundleID, status string) error {
	if _, err := tx.Exec(`UPDATE self_improvement_proposal_bundles SET status=?, updated_at=datetime('now') WHERE id=?`, status, bundleID); err != nil {
		return fmt.Errorf("store: update proposal bundle status: %w", err)
	}
	return nil
}

func DiscardPendingSelfImprovementProposalBundleItemRows(tx *Tx, bundleID, decision string) error {
	if _, err := tx.Exec(`UPDATE self_improvement_proposal_bundle_items SET decision=?, updated_at=datetime('now') WHERE bundle_id=? AND decision IN ('pending', 'accepted')`, decision, bundleID); err != nil {
		return fmt.Errorf("store: discard proposal bundle items: %w", err)
	}
	return nil
}

func InsertSelfImprovementProposalBundleItemEventRow(tx *Tx, event SelfImprovementProposalBundleItemEventRow) error {
	if _, err := tx.Exec(`
		INSERT INTO self_improvement_proposal_bundle_item_events (
			id, bundle_id, item_id, event_type, actor, reason, before_json, after_json
		) VALUES ('bundleevent_' || lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?)`,
		event.BundleID, event.ItemID, event.EventType, event.Actor, event.Reason, event.BeforeJSON, event.AfterJSON,
	); err != nil {
		return fmt.Errorf("store: insert proposal bundle item event: %w", err)
	}
	return nil
}

func UpdatePublishedCatalogVersionProvenanceTx(tx *Tx, assetType, versionID, sourceType, sourceRef, author, changelog string) error {
	table := ""
	switch assetType {
	case "prompt":
		table = "prompt_versions"
	case "skill":
		table = "skill_versions"
	case "guardrail":
		table = "guardrail_versions"
	default:
		return &ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", assetType)}
	}
	res, err := tx.Exec(
		fmt.Sprintf(`UPDATE %s SET source_type=?, source_ref=?, author=?, changelog=? WHERE id=? AND state='published'`, table),
		sourceType, sourceRef, author, changelog, versionID,
	)
	if err != nil {
		return fmt.Errorf("store: update published catalog version metadata: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return &ErrValidation{Msg: "proposal bundle published version metadata was not updated"}
	}
	return nil
}

func (s *Store) GetSelfImprovementProposalBundleRow(id string) (SelfImprovementProposalBundleRow, error) {
	return GetSelfImprovementProposalBundleRow(s.db, id)
}

func GetSelfImprovementProposalBundleRowTx(tx *Tx, id string) (SelfImprovementProposalBundleRow, error) {
	return getSelfImprovementProposalBundleRow(tx, id)
}

func (s *Store) ListSelfImprovementRecommendationsWithBundles(workspace string, limit int) ([]SelfImprovementRecommendationRow, error) {
	return ListSelfImprovementRecommendationsWithBundles(s.db, workspace, limit)
}

func GetSelfImprovementProposalBundleRow(db *sql.DB, id string) (SelfImprovementProposalBundleRow, error) {
	return getSelfImprovementProposalBundleRow(db, id)
}

func getSelfImprovementProposalBundleRow(q querier, id string) (SelfImprovementProposalBundleRow, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return SelfImprovementProposalBundleRow{}, &ErrValidation{Msg: "proposal bundle id or recommendation id is required"}
	}
	var bundle SelfImprovementProposalBundleRow
	err := q.QueryRow(`
		SELECT id, workspace_id, recommendation_id, recommendation_updated_at_snapshot,
		       recommendation_snapshot_hash, status, created_at, updated_at
		FROM self_improvement_proposal_bundles
		WHERE id=? OR recommendation_id=?`, id, id).
		Scan(&bundle.ID, &bundle.WorkspaceID, &bundle.RecommendationID, &bundle.RecommendationUpdatedAtSnapshot,
			&bundle.RecommendationSnapshotHash, &bundle.Status, &bundle.CreatedAt, &bundle.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SelfImprovementProposalBundleRow{}, &ErrNotFound{Msg: fmt.Sprintf("proposal bundle %q not found", id)}
	}
	if err != nil {
		return SelfImprovementProposalBundleRow{}, fmt.Errorf("store: get self-improvement proposal bundle: %w", err)
	}
	items, err := listSelfImprovementProposalBundleRowItems(q, bundle.ID)
	if err != nil {
		return SelfImprovementProposalBundleRow{}, err
	}
	bundle.Items = items
	return bundle, nil
}

func listSelfImprovementProposalBundleRowItems(q querier, bundleID string) ([]SelfImprovementBundleItemRow, error) {
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
	var out []SelfImprovementBundleItemRow
	for rows.Next() {
		var item SelfImprovementBundleItemRow
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

func ListSelfImprovementRecommendationsWithBundles(db *sql.DB, workspace string, limit int) ([]SelfImprovementRecommendationRow, error) {
	recs, err := ListSelfImprovementRecommendations(db, workspace, "", limit)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		bundle, err := GetSelfImprovementProposalBundleRow(db, recs[i].ID)
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
