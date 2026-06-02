package store

import (
	"database/sql"
	"encoding/json"
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
	ProposalBundleOperationLinkExisting   = "link_existing"

	ProposalBundleDecisionPending        = "pending"
	ProposalBundleDecisionAccepted       = "accepted"
	ProposalBundleDecisionRejected       = "rejected"
	ProposalBundleDecisionLinkedExisting = "linked_existing"
	ProposalBundleDecisionPublished      = "published"
	ProposalBundleDecisionDiscarded      = "discarded"
)

type SelfImprovementProposalBundle struct {
	ID               string                         `json:"id"`
	WorkspaceID      string                         `json:"workspace"`
	RecommendationID string                         `json:"recommendation_id"`
	Status           string                         `json:"status"`
	CreatedAt        string                         `json:"created_at"`
	UpdatedAt        string                         `json:"updated_at"`
	Recommendation   *SelfImprovementRecommendation `json:"recommendation,omitempty"`
	Items            []SelfImprovementBundleItem    `json:"items"`
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

func (s *Store) CreateSelfImprovementProposalBundle(id string) (SelfImprovementProposalBundle, error) {
	return CreateSelfImprovementProposalBundle(s.db, id)
}

func (s *Store) GetSelfImprovementProposalBundle(id string) (SelfImprovementProposalBundle, error) {
	return GetSelfImprovementProposalBundle(s.db, id)
}

func (s *Store) UpdateSelfImprovementProposalBundleItem(bundleID, itemID string, in SelfImprovementBundleItemUpdate) (SelfImprovementProposalBundle, error) {
	return UpdateSelfImprovementProposalBundleItem(s.db, bundleID, itemID, in)
}

func (s *Store) RejectSelfImprovementProposalBundleItem(bundleID, itemID, reason string) (SelfImprovementProposalBundle, error) {
	return RejectSelfImprovementProposalBundleItem(s.db, bundleID, itemID, reason)
}

func (s *Store) LinkSelfImprovementProposalBundleItem(bundleID, itemID, assetID, reason string) (SelfImprovementProposalBundle, error) {
	return LinkSelfImprovementProposalBundleItem(s.db, bundleID, itemID, assetID, reason)
}

func (s *Store) PublishSelfImprovementProposalBundle(bundleID string) (SelfImprovementProposalBundle, error) {
	return PublishSelfImprovementProposalBundle(s.db, bundleID)
}

func (s *Store) DiscardSelfImprovementProposalBundle(bundleID string) (SelfImprovementProposalBundle, error) {
	return DiscardSelfImprovementProposalBundle(s.db, bundleID)
}

func (s *Store) ListSelfImprovementRecommendationsWithBundles(workspace string, limit int) ([]SelfImprovementRecommendation, error) {
	return ListSelfImprovementRecommendationsWithBundles(s.db, workspace, limit)
}

func CreateSelfImprovementProposalBundle(db *sql.DB, id string) (SelfImprovementProposalBundle, error) {
	rec, err := GetSelfImprovementRecommendation(db, id)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	if rec.Status != RecommendationStatusAccepted {
		return SelfImprovementProposalBundle{}, &ErrValidation{Msg: "recommendation must be accepted before creating a proposal bundle"}
	}
	if existing, err := GetSelfImprovementProposalBundle(db, rec.ID); err == nil {
		return existing, nil
	} else {
		var nf *ErrNotFound
		if !errors.As(err, &nf) {
			return SelfImprovementProposalBundle{}, err
		}
	}
	items, err := recommendationBundleItems(rec)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: create self-improvement proposal bundle: begin: %w", err)
	}
	defer tx.Rollback()
	bundleID := "bundle_" + randomHexID()
	if _, err := tx.Exec(`
		INSERT INTO self_improvement_proposal_bundles (id, workspace_id, recommendation_id)
		VALUES (?, ?, ?)`, bundleID, rec.WorkspaceID, rec.ID); err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: create self-improvement proposal bundle: %w", err)
	}
	for _, item := range items {
		if err := validateBundleItemForCreate(tx, item); err != nil {
			return SelfImprovementProposalBundle{}, err
		}
		item, err = hydrateBundleItemMetadata(tx, item)
		if err != nil {
			return SelfImprovementProposalBundle{}, err
		}
		if _, err := tx.Exec(`
			INSERT INTO self_improvement_proposal_bundle_items (
				id, bundle_id, operation, asset_type, asset_id, base_version_id, proposed_ref, proposed_name,
				proposed_scope, proposed_body, proposed_description, proposed_enabled, proposed_position,
				analyst_proposed_body, duplicate_risk, rationale, decision
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"bundleitem_"+randomHexID(), bundleID, item.Operation, item.AssetType, item.AssetID, item.BaseVersionID,
			item.ProposedRef, item.ProposedName, item.ProposedScope, item.ProposedBody, item.ProposedDescription,
			boolToInt(bundleItemInputEnabled(item)), normalizedBundlePosition(item.ProposedPosition), item.ProposedBody,
			item.DuplicateRisk, item.Rationale, ProposalBundleDecisionAccepted,
		); err != nil {
			return SelfImprovementProposalBundle{}, fmt.Errorf("store: create self-improvement proposal bundle item: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: create self-improvement proposal bundle: commit: %w", err)
	}
	return GetSelfImprovementProposalBundle(db, rec.ID)
}

func GetSelfImprovementProposalBundle(db *sql.DB, id string) (SelfImprovementProposalBundle, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return SelfImprovementProposalBundle{}, &ErrValidation{Msg: "proposal bundle id or recommendation id is required"}
	}
	var bundle SelfImprovementProposalBundle
	err := db.QueryRow(`
		SELECT id, workspace_id, recommendation_id, status, created_at, updated_at
		FROM self_improvement_proposal_bundles
		WHERE id=? OR recommendation_id=?`, id, id).
		Scan(&bundle.ID, &bundle.WorkspaceID, &bundle.RecommendationID, &bundle.Status, &bundle.CreatedAt, &bundle.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SelfImprovementProposalBundle{}, &ErrNotFound{Msg: fmt.Sprintf("proposal bundle %q not found", id)}
	}
	if err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: get self-improvement proposal bundle: %w", err)
	}
	rec, err := GetSelfImprovementRecommendation(db, bundle.RecommendationID)
	if err == nil {
		bundle.Recommendation = &rec
	}
	items, err := listSelfImprovementProposalBundleItems(db, bundle.ID)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	bundle.Items = items
	return bundle, nil
}

func UpdateSelfImprovementProposalBundleItem(db *sql.DB, bundleID, itemID string, in SelfImprovementBundleItemUpdate) (SelfImprovementProposalBundle, error) {
	bundle, item, err := getBundleAndItem(db, bundleID, itemID)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	if bundle.Status != ProposalBundleStatusPending {
		return SelfImprovementProposalBundle{}, &ErrValidation{Msg: "only pending proposal bundle items can be edited"}
	}
	body := strings.TrimSpace(in.ProposedBody)
	if body == "" {
		return SelfImprovementProposalBundle{}, &ErrValidation{Msg: "proposal bundle item body is required"}
	}
	ref, name, scope := item.ProposedRef, item.ProposedName, item.ProposedScope
	description, enabled, position := item.ProposedDescription, item.ProposedEnabled, item.ProposedPosition
	if item.Operation == ProposalBundleOperationCreateNew {
		if in.ProposedRef != nil {
			ref = strings.TrimSpace(*in.ProposedRef)
		}
		if in.ProposedName != nil {
			name = strings.TrimSpace(*in.ProposedName)
		}
		if in.ProposedScope != nil {
			scope = strings.TrimSpace(*in.ProposedScope)
		}
	}
	if item.AssetType == "guardrail" {
		if in.ProposedDescription != nil {
			description = strings.TrimSpace(*in.ProposedDescription)
		}
		if in.ProposedEnabled != nil {
			enabled = *in.ProposedEnabled
		}
		if in.ProposedPosition != nil {
			position = normalizedBundlePosition(*in.ProposedPosition)
		}
	}
	if item.Operation == ProposalBundleOperationCreateNew {
		if err := validateBundleCreateNew(SelfImprovementBundleItemInput{
			Operation:           item.Operation,
			AssetType:           item.AssetType,
			ProposedRef:         ref,
			ProposedName:        name,
			ProposedScope:       scope,
			ProposedBody:        body,
			ProposedDescription: description,
			ProposedPosition:    position,
		}); err != nil {
			return SelfImprovementProposalBundle{}, err
		}
	}
	_, err = db.Exec(`
		UPDATE self_improvement_proposal_bundle_items
		SET proposed_ref=?, proposed_name=?, proposed_scope=?, proposed_body=?,
		    proposed_description=?, proposed_enabled=?, proposed_position=?, updated_at=datetime('now')
		WHERE id=? AND bundle_id=?`, ref, name, scope, body, description, boolToInt(enabled), position, itemID, bundle.ID)
	if err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: update proposal bundle item: %w", err)
	}
	return GetSelfImprovementProposalBundle(db, bundle.ID)
}

func RejectSelfImprovementProposalBundleItem(db *sql.DB, bundleID, itemID, reason string) (SelfImprovementProposalBundle, error) {
	return decideSelfImprovementProposalBundleItem(db, bundleID, itemID, ProposalBundleDecisionRejected, "", reason)
}

func LinkSelfImprovementProposalBundleItem(db *sql.DB, bundleID, itemID, assetID, reason string) (SelfImprovementProposalBundle, error) {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return SelfImprovementProposalBundle{}, &ErrValidation{Msg: "linked asset id is required"}
	}
	return decideSelfImprovementProposalBundleItem(db, bundleID, itemID, ProposalBundleDecisionLinkedExisting, assetID, reason)
}

func PublishSelfImprovementProposalBundle(db *sql.DB, bundleID string) (SelfImprovementProposalBundle, error) {
	bundle, err := GetSelfImprovementProposalBundle(db, bundleID)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	if bundle.Status != ProposalBundleStatusPending {
		return SelfImprovementProposalBundle{}, &ErrValidation{Msg: "only pending proposal bundles can be published"}
	}
	tx, err := db.Begin()
	if err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: publish proposal bundle: begin: %w", err)
	}
	defer tx.Rollback()
	for _, item := range bundle.Items {
		switch item.Decision {
		case ProposalBundleDecisionRejected:
			continue
		case ProposalBundleDecisionLinkedExisting:
			continue
		case ProposalBundleDecisionAccepted, ProposalBundleDecisionPending:
		default:
			return SelfImprovementProposalBundle{}, &ErrValidation{Msg: fmt.Sprintf("unsupported proposal bundle item decision %q", item.Decision)}
		}
		versionID, err := publishBundleCatalogItem(tx, item, bundle.RecommendationID)
		if err != nil {
			return SelfImprovementProposalBundle{}, err
		}
		if _, err := tx.Exec(`
			UPDATE self_improvement_proposal_bundle_items
			SET decision=?, published_version_id=?, updated_at=datetime('now')
			WHERE id=?`, ProposalBundleDecisionPublished, versionID, item.ID); err != nil {
			return SelfImprovementProposalBundle{}, fmt.Errorf("store: mark proposal bundle item published: %w", err)
		}
	}
	if _, err := tx.Exec(`
		UPDATE self_improvement_proposal_bundles
		SET status=?, updated_at=datetime('now')
		WHERE id=?`, ProposalBundleStatusPublished, bundle.ID); err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: mark proposal bundle published: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: publish proposal bundle: commit: %w", err)
	}
	return GetSelfImprovementProposalBundle(db, bundle.ID)
}

func DiscardSelfImprovementProposalBundle(db *sql.DB, bundleID string) (SelfImprovementProposalBundle, error) {
	bundle, err := GetSelfImprovementProposalBundle(db, bundleID)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	if bundle.Status != ProposalBundleStatusPending {
		return SelfImprovementProposalBundle{}, &ErrValidation{Msg: "only pending proposal bundles can be discarded"}
	}
	tx, err := db.Begin()
	if err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: discard proposal bundle: begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE self_improvement_proposal_bundles SET status=?, updated_at=datetime('now') WHERE id=?`, ProposalBundleStatusDiscarded, bundle.ID); err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: discard proposal bundle: %w", err)
	}
	if _, err := tx.Exec(`UPDATE self_improvement_proposal_bundle_items SET decision=?, updated_at=datetime('now') WHERE bundle_id=? AND decision IN ('pending', 'accepted')`, ProposalBundleDecisionDiscarded, bundle.ID); err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: discard proposal bundle items: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: discard proposal bundle: commit: %w", err)
	}
	return GetSelfImprovementProposalBundle(db, bundle.ID)
}

func ListSelfImprovementRecommendationsWithBundles(db *sql.DB, workspace string, limit int) ([]SelfImprovementRecommendation, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	workspaceID := fleet.NormalizeWorkspaceID(workspace)
	rows, err := db.Query(recommendationSelectSQL()+`
		WHERE r.workspace_id=? AND EXISTS (
			SELECT 1 FROM self_improvement_proposal_bundles b WHERE b.recommendation_id=r.id
		)
		ORDER BY r.updated_at DESC, r.id DESC LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SelfImprovementRecommendation
	for rows.Next() {
		rec, err := scanSelfImprovementRecommendation(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func recommendationBundleItems(rec SelfImprovementRecommendation) ([]SelfImprovementBundleItemInput, error) {
	var items []SelfImprovementBundleItemInput
	if raw, ok := rec.StructuredOutput["changes"]; ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, &ErrValidation{Msg: fmt.Sprintf("proposal bundle changes: %v", err)}
		}
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, &ErrValidation{Msg: fmt.Sprintf("proposal bundle changes: %v", err)}
		}
	}
	if len(items) == 0 {
		if nonConvertibleRecommendationType(rec.Type) {
			return nil, &ErrValidation{Msg: fmt.Sprintf("recommendation type %q is not proposal-convertible", rec.Type)}
		}
		items = []SelfImprovementBundleItemInput{{
			Operation:     ProposalBundleOperationUpdateExisting,
			AssetType:     rec.TargetAssetType,
			AssetID:       rec.TargetAssetID,
			BaseVersionID: rec.TargetBaseVersionID,
			ProposedBody:  rec.ProposedNewBody,
			Rationale:     recommendationProposalChangelog(rec),
		}}
	}
	return items, nil
}

func validateBundleItemForCreate(q querier, item SelfImprovementBundleItemInput) error {
	if strings.TrimSpace(item.ProposedBody) == "" {
		return &ErrValidation{Msg: "proposal bundle item body is required"}
	}
	switch strings.TrimSpace(item.AssetType) {
	case "prompt", "skill", "guardrail":
	default:
		return &ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", item.AssetType)}
	}
	switch strings.TrimSpace(item.Operation) {
	case ProposalBundleOperationUpdateExisting:
		return validateBundleUpdateExisting(q, item)
	case ProposalBundleOperationCreateNew:
		return validateBundleCreateNew(item)
	default:
		return &ErrValidation{Msg: fmt.Sprintf("proposal bundle operation %q is unsupported", item.Operation)}
	}
}

func validateBundleUpdateExisting(q querier, item SelfImprovementBundleItemInput) error {
	if strings.TrimSpace(item.AssetID) == "" {
		return &ErrValidation{Msg: "proposal bundle update item asset id is required"}
	}
	if strings.TrimSpace(item.BaseVersionID) == "" {
		return &ErrValidation{Msg: "proposal bundle update item base version is required"}
	}
	current, err := currentCatalogVersionID(q, item.AssetType, item.AssetID)
	if err != nil {
		return err
	}
	if item.BaseVersionID != current {
		return &ErrValidation{Msg: "proposal bundle update item base version is stale; re-analyze feedback before creating a bundle"}
	}
	return nil
}

func validateBundleCreateNew(item SelfImprovementBundleItemInput) error {
	if strings.TrimSpace(item.ProposedRef) == "" || strings.TrimSpace(item.ProposedName) == "" {
		return &ErrValidation{Msg: "proposal bundle create-new item requires proposed ref and name"}
	}
	if strings.TrimSpace(item.ProposedScope) == "" {
		return &ErrValidation{Msg: "proposal bundle create-new item requires proposed scope"}
	}
	return nil
}

func hydrateBundleItemMetadata(q querier, item SelfImprovementBundleItemInput) (SelfImprovementBundleItemInput, error) {
	if item.AssetType != "guardrail" || item.Operation != ProposalBundleOperationUpdateExisting {
		return item, nil
	}
	guardrail, err := GetGuardrailFrom(q, item.AssetID)
	if err != nil {
		return item, err
	}
	if strings.TrimSpace(item.ProposedDescription) == "" {
		item.ProposedDescription = guardrail.Description
	}
	if item.ProposedEnabled == nil {
		item.ProposedEnabled = &guardrail.Enabled
	}
	if item.ProposedPosition == 0 {
		item.ProposedPosition = guardrail.Position
	}
	return item, nil
}

func listSelfImprovementProposalBundleItems(db *sql.DB, bundleID string) ([]SelfImprovementBundleItem, error) {
	rows, err := db.Query(`
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
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("store: close proposal bundle items: %w", err)
	}
	for i := range out {
		if out[i].BaseVersionID == "" {
			continue
		}
		base, err := readSelfImprovementProposalBaseVersion(db, out[i].AssetType, out[i].BaseVersionID)
		if err != nil {
			return nil, err
		}
		out[i].BaseVersion = &base
		if current, err := currentCatalogVersionID(db, out[i].AssetType, out[i].AssetID); err == nil {
			out[i].CurrentVersionID = current
			out[i].Stale = current != out[i].BaseVersionID
		}
	}
	return out, nil
}

func decideSelfImprovementProposalBundleItem(db *sql.DB, bundleID, itemID, decision, linkedAssetID, reason string) (SelfImprovementProposalBundle, error) {
	bundle, item, err := getBundleAndItem(db, bundleID, itemID)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	if bundle.Status != ProposalBundleStatusPending {
		return SelfImprovementProposalBundle{}, &ErrValidation{Msg: "only pending proposal bundle items can be changed"}
	}
	if decision == ProposalBundleDecisionLinkedExisting {
		if item.Operation != ProposalBundleOperationCreateNew {
			return SelfImprovementProposalBundle{}, &ErrValidation{Msg: "only create-new proposal bundle items can link existing assets"}
		}
		if _, err := currentCatalogVersionID(db, item.AssetType, linkedAssetID); err != nil {
			return SelfImprovementProposalBundle{}, err
		}
	}
	if _, err := db.Exec(`
		UPDATE self_improvement_proposal_bundle_items
		SET decision=?, asset_id=CASE WHEN ? <> '' THEN ? ELSE asset_id END, decision_reason=?, updated_at=datetime('now')
		WHERE id=? AND bundle_id=?`, decision, linkedAssetID, linkedAssetID, strings.TrimSpace(reason), item.ID, bundle.ID); err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: decide proposal bundle item: %w", err)
	}
	return GetSelfImprovementProposalBundle(db, bundle.ID)
}

func getBundleAndItem(db *sql.DB, bundleID, itemID string) (SelfImprovementProposalBundle, SelfImprovementBundleItem, error) {
	bundle, err := GetSelfImprovementProposalBundle(db, bundleID)
	if err != nil {
		return SelfImprovementProposalBundle{}, SelfImprovementBundleItem{}, err
	}
	for _, item := range bundle.Items {
		if item.ID == itemID {
			return bundle, item, nil
		}
	}
	return SelfImprovementProposalBundle{}, SelfImprovementBundleItem{}, &ErrNotFound{Msg: fmt.Sprintf("proposal bundle item %q not found", itemID)}
}

func publishBundleCatalogItem(tx *sql.Tx, item SelfImprovementBundleItem, recommendationID string) (string, error) {
	meta := fleet.CatalogVersionMetadata{
		State:      "draft",
		SourceType: "feedback_recommendation",
		SourceRef:  recommendationID,
		Author:     "agents-assistant",
		Changelog:  item.Rationale,
	}
	if meta.Changelog == "" {
		meta.Changelog = "Self-improvement proposal bundle " + item.BundleID
	}
	switch item.Operation {
	case ProposalBundleOperationUpdateExisting:
		current, err := currentCatalogVersionID(tx, item.AssetType, item.AssetID)
		if err != nil {
			return "", err
		}
		if current != item.BaseVersionID {
			return "", &ErrValidation{Msg: "proposal bundle item base version is stale; re-analyze feedback before publishing"}
		}
		return publishBundleUpdateExisting(tx, item, meta)
	case ProposalBundleOperationCreateNew:
		return publishBundleCreateNew(tx, item)
	default:
		return "", &ErrValidation{Msg: fmt.Sprintf("unsupported proposal bundle operation %q", item.Operation)}
	}
}

func publishBundleUpdateExisting(tx *sql.Tx, item SelfImprovementBundleItem, meta fleet.CatalogVersionMetadata) (string, error) {
	meta.State = "proposal"
	var version fleet.CatalogVersion
	var err error
	switch item.AssetType {
	case "prompt":
		prompt, err := readPromptFrom(tx, item.AssetID)
		if err != nil {
			return "", err
		}
		version, err = CreatePromptDraftTx(tx, prompt.ID, prompt.Description, item.ProposedBody, meta)
	case "skill":
		version, err = CreateSkillDraftTx(tx, item.AssetID, item.ProposedBody, meta)
	case "guardrail":
		guardrail, err := GetGuardrailFrom(tx, item.AssetID)
		if err != nil {
			return "", err
		}
		guardrail.Description = item.ProposedDescription
		guardrail.Content = item.ProposedBody
		guardrail.Enabled = item.ProposedEnabled
		guardrail.Position = normalizedBundlePosition(item.ProposedPosition)
		version, err = CreateGuardrailDraftTx(tx, guardrail.ID, guardrail, meta)
	default:
		return "", &ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", item.AssetType)}
	}
	if err != nil {
		return "", err
	}
	switch item.AssetType {
	case "prompt":
		_, err = PublishPromptVersionTx(tx, version.ID)
	case "skill":
		_, _, err = PublishSkillVersionTx(tx, version.ID)
	case "guardrail":
		_, err = PublishGuardrailVersionTx(tx, version.ID)
	}
	return version.ID, err
}

func publishBundleCreateNew(tx *sql.Tx, item SelfImprovementBundleItem) (string, error) {
	scope, repo := parseBundleScope(item.ProposedScope)
	if err := ensureBundleCreateNewRefAvailable(tx, item.AssetType, item.ProposedRef); err != nil {
		return "", err
	}
	switch item.AssetType {
	case "prompt":
		prompt, err := UpsertPromptTx(tx, fleet.Prompt{ID: item.ProposedRef, Name: item.ProposedName, WorkspaceID: scope, Repo: repo, Content: item.ProposedBody})
		if err != nil {
			return "", err
		}
		return prompt.VersionID, nil
	case "skill":
		if err := UpsertSkillTx(tx, item.ProposedRef, fleet.Skill{Name: item.ProposedName, WorkspaceID: scope, Repo: repo, Prompt: item.ProposedBody}); err != nil {
			return "", err
		}
		skill, err := readSkill(tx, item.ProposedRef)
		if err != nil {
			return "", err
		}
		return skill.VersionID, nil
	case "guardrail":
		if err := UpsertGuardrailTx(tx, fleet.Guardrail{
			ID: item.ProposedRef, Name: item.ProposedName, WorkspaceID: scope,
			Description: item.ProposedDescription, Content: item.ProposedBody,
			Enabled: item.ProposedEnabled, Position: normalizedBundlePosition(item.ProposedPosition),
		}); err != nil {
			return "", err
		}
		guardrail, err := GetGuardrailFrom(tx, item.ProposedRef)
		if err != nil {
			return "", err
		}
		return guardrail.VersionID, nil
	default:
		return "", &ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", item.AssetType)}
	}
}

func normalizedBundlePosition(position int) int {
	if position == 0 {
		return 100
	}
	return position
}

func bundleItemInputEnabled(item SelfImprovementBundleItemInput) bool {
	if item.ProposedEnabled == nil {
		return true
	}
	return *item.ProposedEnabled
}

func ensureBundleCreateNewRefAvailable(q querier, assetType, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return &ErrValidation{Msg: "proposal bundle create-new item ref is required"}
	}
	var exists bool
	var err error
	switch assetType {
	case "prompt":
		err = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM prompts WHERE ref=?)`, ref).Scan(&exists)
	case "skill":
		err = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM skills WHERE ref=?)`, ref).Scan(&exists)
	case "guardrail":
		err = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM guardrails WHERE ref=?)`, ref).Scan(&exists)
	default:
		return &ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", assetType)}
	}
	if err != nil {
		return fmt.Errorf("store: check proposal bundle create-new ref: %w", err)
	}
	if exists {
		return &ErrConflict{Msg: fmt.Sprintf("%s %q already exists", assetType, ref)}
	}
	return nil
}

func currentCatalogVersionID(q querier, assetType, assetID string) (string, error) {
	var id string
	var err error
	switch strings.TrimSpace(assetType) {
	case "prompt":
		err = q.QueryRow(`SELECT COALESCE(current_version_id, '') FROM prompts WHERE id=? OR ref=?`, assetID, assetID).Scan(&id)
	case "skill":
		err = q.QueryRow(`SELECT COALESCE(current_version_id, '') FROM skills WHERE id=? OR ref=? OR name=?`, assetID, assetID, fleet.NormalizeSkillName(assetID)).Scan(&id)
	case "guardrail":
		err = q.QueryRow(`SELECT COALESCE(current_version_id, '') FROM guardrails WHERE id=? OR ref=? OR name=?`, assetID, assetID, fleet.NormalizeGuardrailName(assetID)).Scan(&id)
	default:
		return "", &ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", assetType)}
	}
	if err != nil {
		return "", catalogReadErr(assetType, assetID, err)
	}
	if id == "" {
		return "", &ErrValidation{Msg: fmt.Sprintf("%s %q has no current version", assetType, assetID)}
	}
	return id, nil
}

func readPromptFrom(q querier, ref string) (fleet.Prompt, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fleet.Prompt{}, &ErrValidation{Msg: "prompt id is required"}
	}
	var p fleet.Prompt
	err := q.QueryRow(`
		SELECT p.ref, COALESCE(p.workspace_id, ''), COALESCE(p.repo, ''), p.name, p.description, p.content,
		       COALESCE(pv.id, ''), COALESCE(pv.version_number, 0)
		FROM prompts p
		LEFT JOIN prompt_versions pv ON pv.id = p.current_version_id
		WHERE p.id=? OR p.ref=?`, ref, ref).
		Scan(&p.ID, &p.WorkspaceID, &p.Repo, &p.Name, &p.Description, &p.Content, &p.VersionID, &p.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Prompt{}, &ErrNotFound{Msg: fmt.Sprintf("prompt %q not found", ref)}
	}
	if err != nil {
		return fleet.Prompt{}, fmt.Errorf("store: read prompt %s: %w", ref, err)
	}
	return p, nil
}

func parseBundleScope(raw string) (workspace, repo string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "global" {
		return "", ""
	}
	if raw == "workspace" {
		return fleet.DefaultWorkspaceID, ""
	}
	parts := strings.Split(raw, "/")
	if len(parts) >= 3 {
		return fleet.NormalizeWorkspaceID(parts[0]), fleet.NormalizeRepoName(strings.Join(parts[1:], "/"))
	}
	return fleet.NormalizeWorkspaceID(raw), ""
}

func randomHexID() string {
	id, err := newCatalogInternalID("")
	if err != nil {
		return "fallback"
	}
	return strings.TrimPrefix(id, "_")
}
