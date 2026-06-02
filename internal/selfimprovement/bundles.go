package selfimprovement

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

const (
	ProposalBundleStatusPending   = store.ProposalBundleStatusPending
	ProposalBundleStatusPublished = store.ProposalBundleStatusPublished
	ProposalBundleStatusDiscarded = store.ProposalBundleStatusDiscarded

	ProposalBundleOperationUpdateExisting = store.ProposalBundleOperationUpdateExisting
	ProposalBundleOperationCreateNew      = store.ProposalBundleOperationCreateNew

	ProposalBundleDecisionPending        = store.ProposalBundleDecisionPending
	ProposalBundleDecisionAccepted       = store.ProposalBundleDecisionAccepted
	ProposalBundleDecisionRejected       = store.ProposalBundleDecisionRejected
	ProposalBundleDecisionLinkedExisting = store.ProposalBundleDecisionLinkedExisting
	ProposalBundleDecisionPublished      = store.ProposalBundleDecisionPublished
	ProposalBundleDecisionDiscarded      = store.ProposalBundleDecisionDiscarded
)

type SelfImprovementProposalBundle = store.SelfImprovementProposalBundle
type SelfImprovementBundleItem = store.SelfImprovementBundleItem
type SelfImprovementBundleItemInput = store.SelfImprovementBundleItemInput
type SelfImprovementBundleItemUpdate = store.SelfImprovementBundleItemUpdate
type SelfImprovementRecommendation = store.SelfImprovementRecommendation

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func createSelfImprovementProposalBundle(st *store.Store, id string) (SelfImprovementProposalBundle, error) {
	rec, err := st.GetSelfImprovementRecommendation(id)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	if rec.Status != store.RecommendationStatusAccepted {
		return SelfImprovementProposalBundle{}, &store.ErrValidation{Msg: "recommendation must be accepted before creating a proposal bundle"}
	}
	if existing, err := getSelfImprovementProposalBundleFromStore(st, rec.ID); err == nil {
		return existing, nil
	} else {
		var nf *store.ErrNotFound
		if !errors.As(err, &nf) {
			return SelfImprovementProposalBundle{}, err
		}
	}
	items, err := recommendationBundleItems(rec)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	snapshotHash, err := recommendationSnapshotHash(rec)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	bundleID := "bundle_" + randomHexID()
	if err := st.Transact(func(tx *store.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO self_improvement_proposal_bundles (
				id, workspace_id, recommendation_id, recommendation_updated_at_snapshot, recommendation_snapshot_hash
			) VALUES (?, ?, ?, ?, ?)`, bundleID, rec.WorkspaceID, rec.ID, rec.UpdatedAt, snapshotHash); err != nil {
			return fmt.Errorf("selfimprovement: create proposal bundle: %w", err)
		}
		for _, item := range items {
			if err := validateBundleItemForCreate(tx, rec.WorkspaceID, item); err != nil {
				return err
			}
			item, err = hydrateBundleItemMetadata(tx, item)
			if err != nil {
				return err
			}
			itemID := "bundleitem_" + randomHexID()
			if _, err := tx.Exec(`
				INSERT INTO self_improvement_proposal_bundle_items (
					id, bundle_id, operation, asset_type, asset_id, base_version_id, proposed_ref, proposed_name,
					proposed_scope, proposed_body, proposed_description, proposed_enabled, proposed_position,
					analyst_proposed_body, duplicate_risk, rationale, decision
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				itemID, bundleID, item.Operation, item.AssetType, item.AssetID, item.BaseVersionID,
				item.ProposedRef, item.ProposedName, item.ProposedScope, item.ProposedBody, item.ProposedDescription,
				boolToInt(bundleItemInputEnabled(item)), normalizedBundlePosition(item.ProposedPosition), item.ProposedBody,
				item.DuplicateRisk, item.Rationale, ProposalBundleDecisionAccepted,
			); err != nil {
				return fmt.Errorf("selfimprovement: create proposal bundle item: %w", err)
			}
			after := bundleItemInputAuditSnapshot(bundleID, itemID, item)
			if err := insertBundleItemEvent(tx, bundleID, itemID, "created", "system", "", nil, after); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	return getSelfImprovementProposalBundleFromStore(st, rec.ID)
}

func getSelfImprovementProposalBundleFromStore(st *store.Store, id string) (SelfImprovementProposalBundle, error) {
	bundle, err := st.GetSelfImprovementProposalBundle(id)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	rec, err := st.GetSelfImprovementRecommendation(bundle.RecommendationID)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	hash, err := recommendationSnapshotHash(rec)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	bundle.RecommendationChanged = rec.UpdatedAt != bundle.RecommendationUpdatedAtSnapshot || hash != bundle.RecommendationSnapshotHash
	bundle.Recommendation = &rec
	if err := st.Transact(func(tx *store.Tx) error {
		return hydrateProposalBundleReadState(tx, &bundle)
	}); err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	return bundle, nil
}

func getSelfImprovementProposalBundle(q querier, id string) (SelfImprovementProposalBundle, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return SelfImprovementProposalBundle{}, &store.ErrValidation{Msg: "proposal bundle id or recommendation id is required"}
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
		return SelfImprovementProposalBundle{}, &store.ErrNotFound{Msg: fmt.Sprintf("proposal bundle %q not found", id)}
	}
	if err != nil {
		return SelfImprovementProposalBundle{}, fmt.Errorf("store: get self-improvement proposal bundle: %w", err)
	}
	rec, err := getSelfImprovementRecommendation(q, bundle.RecommendationID)
	if err == nil {
		hash, hashErr := recommendationSnapshotHash(rec)
		if hashErr == nil {
			bundle.RecommendationChanged = rec.UpdatedAt != bundle.RecommendationUpdatedAtSnapshot || hash != bundle.RecommendationSnapshotHash
		}
		bundle.Recommendation = &rec
	}
	items, err := listSelfImprovementProposalBundleItems(q, bundle.ID)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	bundle.Items = items
	return bundle, nil
}

func updateSelfImprovementProposalBundleItemWithActor(st *store.Store, bundleID, itemID string, in SelfImprovementBundleItemUpdate, actor string) (SelfImprovementProposalBundle, error) {
	bundle, item, err := getBundleAndItem(st, bundleID, itemID)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	if bundle.Status != ProposalBundleStatusPending {
		return SelfImprovementProposalBundle{}, &store.ErrValidation{Msg: "only pending proposal bundle items can be edited"}
	}
	body := strings.TrimSpace(in.ProposedBody)
	if body == "" {
		return SelfImprovementProposalBundle{}, &store.ErrValidation{Msg: "proposal bundle item body is required"}
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
	after := item
	after.ProposedRef, after.ProposedName, after.ProposedScope = ref, name, scope
	after.ProposedBody, after.ProposedDescription, after.ProposedEnabled, after.ProposedPosition = body, description, enabled, position
	if err := st.Transact(func(tx *store.Tx) error {
		if item.Operation == ProposalBundleOperationCreateNew {
			if err := validateBundleCreateNew(tx, bundle.WorkspaceID, SelfImprovementBundleItemInput{
				Operation:           item.Operation,
				AssetType:           item.AssetType,
				ProposedRef:         ref,
				ProposedName:        name,
				ProposedScope:       scope,
				ProposedBody:        body,
				ProposedDescription: description,
				ProposedPosition:    position,
			}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`
			UPDATE self_improvement_proposal_bundle_items
			SET proposed_ref=?, proposed_name=?, proposed_scope=?, proposed_body=?,
			    proposed_description=?, proposed_enabled=?, proposed_position=?, updated_at=datetime('now')
			WHERE id=? AND bundle_id=?`, ref, name, scope, body, description, boolToInt(enabled), position, itemID, bundle.ID); err != nil {
			return fmt.Errorf("selfimprovement: update proposal bundle item: %w", err)
		}
		return insertBundleItemEvent(tx, bundle.ID, item.ID, "edited", actor, "", bundleItemAuditSnapshot(item), bundleItemAuditSnapshot(after))
	}); err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	return getSelfImprovementProposalBundleFromStore(st, bundle.ID)
}

func rejectSelfImprovementProposalBundleItemWithActor(st *store.Store, bundleID, itemID, reason, actor string) (SelfImprovementProposalBundle, error) {
	return decideSelfImprovementProposalBundleItem(st, bundleID, itemID, ProposalBundleDecisionRejected, "", reason, actor)
}

func linkSelfImprovementProposalBundleItemWithActor(st *store.Store, bundleID, itemID, assetID, reason, actor string) (SelfImprovementProposalBundle, error) {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return SelfImprovementProposalBundle{}, &store.ErrValidation{Msg: "linked asset id is required"}
	}
	return decideSelfImprovementProposalBundleItem(st, bundleID, itemID, ProposalBundleDecisionLinkedExisting, assetID, reason, actor)
}

func publishSelfImprovementProposalBundleWithActor(st *store.Store, bundleID, actor string) (SelfImprovementProposalBundle, error) {
	var publishedID string
	if err := st.Transact(func(tx *store.Tx) error {
		bundle, err := getSelfImprovementProposalBundle(tx, bundleID)
		if err != nil {
			return err
		}
		if bundle.Status != ProposalBundleStatusPending {
			return &store.ErrValidation{Msg: "only pending proposal bundles can be published"}
		}
		for _, item := range bundle.Items {
			before := bundleItemAuditSnapshot(item)
			switch item.Decision {
			case ProposalBundleDecisionRejected:
				if err := insertBundleItemEvent(tx, bundle.ID, item.ID, "finalized", actor, "bundle published", before, before); err != nil {
					return err
				}
				continue
			case ProposalBundleDecisionLinkedExisting:
				if err := insertBundleItemEvent(tx, bundle.ID, item.ID, "finalized", actor, "bundle published", before, before); err != nil {
					return err
				}
				continue
			case ProposalBundleDecisionAccepted, ProposalBundleDecisionPending:
			default:
				return &store.ErrValidation{Msg: fmt.Sprintf("unsupported proposal bundle item decision %q", item.Decision)}
			}
			versionID, err := publishBundleCatalogItem(tx, item, bundle.WorkspaceID, bundle.RecommendationID)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`
				UPDATE self_improvement_proposal_bundle_items
				SET decision=?, published_version_id=?, updated_at=datetime('now')
				WHERE id=?`, ProposalBundleDecisionPublished, versionID, item.ID); err != nil {
				return fmt.Errorf("selfimprovement: mark proposal bundle item published: %w", err)
			}
			after := item
			after.Decision = ProposalBundleDecisionPublished
			after.PublishedVersionID = versionID
			if err := insertBundleItemEvent(tx, bundle.ID, item.ID, "published", actor, "", before, bundleItemAuditSnapshot(after)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`
			UPDATE self_improvement_proposal_bundles
			SET status=?, updated_at=datetime('now')
			WHERE id=?`, ProposalBundleStatusPublished, bundle.ID); err != nil {
			return fmt.Errorf("selfimprovement: mark proposal bundle published: %w", err)
		}
		publishedID = bundle.ID
		return nil
	}); err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	return getSelfImprovementProposalBundleFromStore(st, publishedID)
}

func discardSelfImprovementProposalBundleWithActor(st *store.Store, bundleID, actor string) (SelfImprovementProposalBundle, error) {
	var discardedID string
	if err := st.Transact(func(tx *store.Tx) error {
		bundle, err := getSelfImprovementProposalBundle(tx, bundleID)
		if err != nil {
			return err
		}
		if bundle.Status != ProposalBundleStatusPending {
			return &store.ErrValidation{Msg: "only pending proposal bundles can be discarded"}
		}
		if _, err := tx.Exec(`UPDATE self_improvement_proposal_bundles SET status=?, updated_at=datetime('now') WHERE id=?`, ProposalBundleStatusDiscarded, bundle.ID); err != nil {
			return fmt.Errorf("selfimprovement: discard proposal bundle: %w", err)
		}
		if _, err := tx.Exec(`UPDATE self_improvement_proposal_bundle_items SET decision=?, updated_at=datetime('now') WHERE bundle_id=? AND decision IN ('pending', 'accepted')`, ProposalBundleDecisionDiscarded, bundle.ID); err != nil {
			return fmt.Errorf("selfimprovement: discard proposal bundle items: %w", err)
		}
		for _, item := range bundle.Items {
			before := bundleItemAuditSnapshot(item)
			after := item
			if item.Decision == ProposalBundleDecisionAccepted || item.Decision == ProposalBundleDecisionPending {
				after.Decision = ProposalBundleDecisionDiscarded
			}
			if err := insertBundleItemEvent(tx, bundle.ID, item.ID, "discarded", actor, "bundle discarded", before, bundleItemAuditSnapshot(after)); err != nil {
				return err
			}
		}
		discardedID = bundle.ID
		return nil
	}); err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	return getSelfImprovementProposalBundleFromStore(st, discardedID)
}

func recommendationBundleItems(rec SelfImprovementRecommendation) ([]SelfImprovementBundleItemInput, error) {
	var items []SelfImprovementBundleItemInput
	if raw, ok := rec.StructuredOutput["changes"]; ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle changes: %v", err)}
		}
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle changes: %v", err)}
		}
	}
	if len(items) == 0 {
		if nonConvertibleRecommendationType(rec.Type) {
			return nil, &store.ErrValidation{Msg: fmt.Sprintf("recommendation type %q is not proposal-convertible", rec.Type)}
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

func validateBundleItemForCreate(q querier, workspaceID string, item SelfImprovementBundleItemInput) error {
	if strings.TrimSpace(item.ProposedBody) == "" {
		return &store.ErrValidation{Msg: "proposal bundle item body is required"}
	}
	switch strings.TrimSpace(item.AssetType) {
	case "prompt", "skill", "guardrail":
	default:
		return &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", item.AssetType)}
	}
	switch strings.TrimSpace(item.Operation) {
	case ProposalBundleOperationUpdateExisting:
		return validateBundleUpdateExisting(q, item)
	case ProposalBundleOperationCreateNew:
		return validateBundleCreateNew(q, workspaceID, item)
	default:
		return &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle operation %q is unsupported", item.Operation)}
	}
}

func validateBundleUpdateExisting(q querier, item SelfImprovementBundleItemInput) error {
	if strings.TrimSpace(item.AssetID) == "" {
		return &store.ErrValidation{Msg: "proposal bundle update item asset id is required"}
	}
	if strings.TrimSpace(item.BaseVersionID) == "" {
		return &store.ErrValidation{Msg: "proposal bundle update item base version is required"}
	}
	current, err := currentCatalogVersionID(q, item.AssetType, item.AssetID)
	if err != nil {
		return err
	}
	if item.BaseVersionID != current {
		return &store.ErrValidation{Msg: "proposal bundle update item base version is stale; re-analyze feedback before creating a bundle"}
	}
	return nil
}

func validateBundleCreateNew(q querier, workspaceID string, item SelfImprovementBundleItemInput) error {
	if strings.TrimSpace(item.ProposedRef) == "" || strings.TrimSpace(item.ProposedName) == "" {
		return &store.ErrValidation{Msg: "proposal bundle create-new item requires proposed ref and name"}
	}
	if strings.TrimSpace(item.ProposedScope) == "" {
		return &store.ErrValidation{Msg: "proposal bundle create-new item requires proposed scope"}
	}
	return validateBundleScope(q, item.ProposedScope, workspaceID)
}

func hydrateBundleItemMetadata(q querier, item SelfImprovementBundleItemInput) (SelfImprovementBundleItemInput, error) {
	if item.AssetType != "guardrail" || item.Operation != ProposalBundleOperationUpdateExisting {
		return item, nil
	}
	guardrail, err := getGuardrailFrom(q, item.AssetID)
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
	bundle := SelfImprovementProposalBundle{Items: out}
	if err := hydrateProposalBundleReadState(q, &bundle); err != nil {
		return nil, err
	}
	return bundle.Items, nil
}

func hydrateProposalBundleReadState(q querier, bundle *SelfImprovementProposalBundle) error {
	for i := range bundle.Items {
		if bundle.Items[i].BaseVersionID == "" {
			continue
		}
		base, err := readSelfImprovementProposalBaseVersion(q, bundle.Items[i].AssetType, bundle.Items[i].BaseVersionID)
		if err != nil {
			return err
		}
		bundle.Items[i].BaseVersion = &base
		if current, err := currentCatalogVersionID(q, bundle.Items[i].AssetType, bundle.Items[i].AssetID); err == nil {
			bundle.Items[i].CurrentVersionID = current
			bundle.Items[i].Stale = current != bundle.Items[i].BaseVersionID
		}
	}
	return nil
}

func decideSelfImprovementProposalBundleItem(st *store.Store, bundleID, itemID, decision, linkedAssetID, reason, actor string) (SelfImprovementProposalBundle, error) {
	bundle, item, err := getBundleAndItem(st, bundleID, itemID)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	if bundle.Status != ProposalBundleStatusPending {
		return SelfImprovementProposalBundle{}, &store.ErrValidation{Msg: "only pending proposal bundle items can be changed"}
	}
	after := item
	after.Decision = decision
	after.DecisionReason = strings.TrimSpace(reason)
	if linkedAssetID != "" {
		after.AssetID = linkedAssetID
	}
	eventType := "rejected"
	if decision == ProposalBundleDecisionLinkedExisting {
		eventType = "linked_existing"
	}
	if err := st.Transact(func(tx *store.Tx) error {
		if decision == ProposalBundleDecisionLinkedExisting {
			if item.Operation != ProposalBundleOperationCreateNew {
				return &store.ErrValidation{Msg: "only create-new proposal bundle items can link existing assets"}
			}
			if _, err := currentCatalogVersionID(tx, item.AssetType, linkedAssetID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`
			UPDATE self_improvement_proposal_bundle_items
			SET decision=?, asset_id=CASE WHEN ? <> '' THEN ? ELSE asset_id END, decision_reason=?, updated_at=datetime('now')
			WHERE id=? AND bundle_id=?`, decision, linkedAssetID, linkedAssetID, strings.TrimSpace(reason), item.ID, bundle.ID); err != nil {
			return fmt.Errorf("selfimprovement: decide proposal bundle item: %w", err)
		}
		return insertBundleItemEvent(tx, bundle.ID, item.ID, eventType, actor, strings.TrimSpace(reason), bundleItemAuditSnapshot(item), bundleItemAuditSnapshot(after))
	}); err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	return getSelfImprovementProposalBundleFromStore(st, bundle.ID)
}

func getBundleAndItem(st *store.Store, bundleID, itemID string) (SelfImprovementProposalBundle, SelfImprovementBundleItem, error) {
	bundle, err := getSelfImprovementProposalBundleFromStore(st, bundleID)
	if err != nil {
		return SelfImprovementProposalBundle{}, SelfImprovementBundleItem{}, err
	}
	for _, item := range bundle.Items {
		if item.ID == itemID {
			return bundle, item, nil
		}
	}
	return SelfImprovementProposalBundle{}, SelfImprovementBundleItem{}, &store.ErrNotFound{Msg: fmt.Sprintf("proposal bundle item %q not found", itemID)}
}

func publishBundleCatalogItem(tx *sql.Tx, item SelfImprovementBundleItem, workspaceID, recommendationID string) (string, error) {
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
			return "", &store.ErrValidation{Msg: "proposal bundle item base version is stale; re-analyze feedback before publishing"}
		}
		return publishBundleUpdateExisting(tx, item, meta)
	case ProposalBundleOperationCreateNew:
		return publishBundleCreateNew(tx, item, workspaceID, meta)
	default:
		return "", &store.ErrValidation{Msg: fmt.Sprintf("unsupported proposal bundle operation %q", item.Operation)}
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
		version, err = store.CreatePromptDraftTx(tx, prompt.ID, prompt.Description, item.ProposedBody, meta)
	case "skill":
		version, err = store.CreateSkillDraftTx(tx, item.AssetID, item.ProposedBody, meta)
	case "guardrail":
		guardrail, err := getGuardrailFrom(tx, item.AssetID)
		if err != nil {
			return "", err
		}
		guardrail.Description = item.ProposedDescription
		guardrail.Content = item.ProposedBody
		guardrail.Enabled = item.ProposedEnabled
		guardrail.Position = normalizedBundlePosition(item.ProposedPosition)
		version, err = store.CreateGuardrailDraftTx(tx, guardrail.ID, guardrail, meta)
	default:
		return "", &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", item.AssetType)}
	}
	if err != nil {
		return "", err
	}
	switch item.AssetType {
	case "prompt":
		_, err = store.PublishPromptVersionTx(tx, version.ID)
	case "skill":
		_, _, err = store.PublishSkillVersionTx(tx, version.ID)
	case "guardrail":
		_, err = store.PublishGuardrailVersionTx(tx, version.ID)
	}
	return version.ID, err
}

func publishBundleCreateNew(tx *sql.Tx, item SelfImprovementBundleItem, workspaceID string, meta fleet.CatalogVersionMetadata) (string, error) {
	scope, repo := parseBundleScope(item.ProposedScope, workspaceID)
	if err := ensureBundleCreateNewRefAvailable(tx, item.AssetType, item.ProposedRef); err != nil {
		return "", err
	}
	switch item.AssetType {
	case "prompt":
		prompt, err := store.UpsertPromptTx(tx, fleet.Prompt{ID: item.ProposedRef, Name: item.ProposedName, WorkspaceID: scope, Repo: repo, Content: item.ProposedBody})
		if err != nil {
			return "", err
		}
		if err := updatePublishedCatalogVersionMetadata(tx, "prompt", prompt.VersionID, meta); err != nil {
			return "", err
		}
		return prompt.VersionID, nil
	case "skill":
		if err := store.UpsertSkillTx(tx, item.ProposedRef, fleet.Skill{Name: item.ProposedName, WorkspaceID: scope, Repo: repo, Prompt: item.ProposedBody}); err != nil {
			return "", err
		}
		skill, err := readSkill(tx, item.ProposedRef)
		if err != nil {
			return "", err
		}
		if err := updatePublishedCatalogVersionMetadata(tx, "skill", skill.VersionID, meta); err != nil {
			return "", err
		}
		return skill.VersionID, nil
	case "guardrail":
		if err := store.UpsertGuardrailTx(tx, fleet.Guardrail{
			ID: item.ProposedRef, Name: item.ProposedName, WorkspaceID: scope,
			Description: item.ProposedDescription, Content: item.ProposedBody,
			Enabled: item.ProposedEnabled, Position: normalizedBundlePosition(item.ProposedPosition),
		}); err != nil {
			return "", err
		}
		guardrail, err := getGuardrailFrom(tx, item.ProposedRef)
		if err != nil {
			return "", err
		}
		if err := updatePublishedCatalogVersionMetadata(tx, "guardrail", guardrail.VersionID, meta); err != nil {
			return "", err
		}
		return guardrail.VersionID, nil
	default:
		return "", &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", item.AssetType)}
	}
}

func updatePublishedCatalogVersionMetadata(tx *sql.Tx, assetType, versionID string, meta fleet.CatalogVersionMetadata) error {
	meta.State = "proposal"
	normalized, err := normalizeNewCatalogVersionMetadata(meta, "proposal")
	if err != nil {
		return err
	}
	var table string
	switch assetType {
	case "prompt":
		table = "prompt_versions"
	case "skill":
		table = "skill_versions"
	case "guardrail":
		table = "guardrail_versions"
	default:
		return &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", assetType)}
	}
	res, err := tx.Exec(
		fmt.Sprintf(`UPDATE %s SET source_type=?, source_ref=?, author=?, changelog=? WHERE id=? AND state='published'`, table),
		normalized.SourceType, normalized.SourceRef, normalized.Author, normalized.Changelog, versionID,
	)
	if err != nil {
		return fmt.Errorf("store: update proposal bundle create-new version metadata: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update proposal bundle create-new version metadata: %w", err)
	}
	if affected == 0 {
		return &store.ErrNotFound{Msg: fmt.Sprintf("%s version %q not found", assetType, versionID)}
	}
	return nil
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
		return &store.ErrValidation{Msg: "proposal bundle create-new item ref is required"}
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
		return &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", assetType)}
	}
	if err != nil {
		return fmt.Errorf("store: check proposal bundle create-new ref: %w", err)
	}
	if exists {
		return &store.ErrConflict{Msg: fmt.Sprintf("%s %q already exists", assetType, ref)}
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
		return "", &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", assetType)}
	}
	if err != nil {
		return "", catalogReadErr(assetType, assetID, err)
	}
	if id == "" {
		return "", &store.ErrValidation{Msg: fmt.Sprintf("%s %q has no current version", assetType, assetID)}
	}
	return id, nil
}

func readPromptFrom(q querier, ref string) (fleet.Prompt, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fleet.Prompt{}, &store.ErrValidation{Msg: "prompt id is required"}
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
		return fleet.Prompt{}, &store.ErrNotFound{Msg: fmt.Sprintf("prompt %q not found", ref)}
	}
	if err != nil {
		return fleet.Prompt{}, fmt.Errorf("store: read prompt %s: %w", ref, err)
	}
	return p, nil
}

func parseBundleScope(raw, currentWorkspace string) (workspace, repo string) {
	raw = strings.TrimSpace(raw)
	scope := strings.ToLower(raw)
	if scope == "" || scope == "global" {
		return "", ""
	}
	if scope == "workspace" {
		return fleet.NormalizeWorkspaceID(currentWorkspace), ""
	}
	parts := strings.Split(raw, "/")
	if len(parts) >= 3 {
		return fleet.NormalizeWorkspaceID(parts[0]), fleet.NormalizeRepoName(strings.Join(parts[1:], "/"))
	}
	return fleet.NormalizeWorkspaceID(raw), ""
}

func validateBundleScope(q querier, raw, currentWorkspace string) error {
	raw = strings.TrimSpace(raw)
	scope := strings.ToLower(raw)
	if scope == "global" || scope == "workspace" {
		return nil
	}
	workspace, repo := parseBundleScope(raw, currentWorkspace)
	if workspace == "" && repo == "" {
		return nil
	}
	var exists bool
	if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM workspaces WHERE id=?)`, workspace).Scan(&exists); err != nil {
		return fmt.Errorf("store: validate proposal bundle scope workspace: %w", err)
	}
	if !exists {
		return &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle scope workspace %q does not exist", workspace)}
	}
	if repo == "" {
		return nil
	}
	if len(strings.Split(raw, "/")) != 3 || repo == "" {
		return &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle repo scope %q is invalid", raw)}
	}
	if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM repos WHERE workspace_id=? AND name=?)`, workspace, repo).Scan(&exists); err != nil {
		return fmt.Errorf("store: validate proposal bundle scope repo: %w", err)
	}
	if !exists {
		return &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle scope repo %q does not exist", repo)}
	}
	return nil
}

type recommendationSnapshot struct {
	Type                    string         `json:"type"`
	Status                  string         `json:"status"`
	Confidence              string         `json:"confidence"`
	Risk                    string         `json:"risk"`
	Finding                 string         `json:"finding"`
	NormalizedLesson        string         `json:"normalized_lesson"`
	Rationale               string         `json:"rationale"`
	EvidenceFeedbackIDs     []int64        `json:"evidence_feedback_ids"`
	EvidenceSourceURLs      []string       `json:"evidence_source_urls"`
	AttributionConfidence   string         `json:"attribution_confidence"`
	TargetAssetType         string         `json:"target_asset_type"`
	TargetAssetID           string         `json:"target_asset_id"`
	TargetBaseVersionID     string         `json:"target_base_version_id"`
	ProposedPatch           string         `json:"proposed_patch"`
	ProposedNewBody         string         `json:"proposed_new_body"`
	SuggestedRolloutScope   string         `json:"suggested_rollout_scope"`
	AnalyzerPromptRef       string         `json:"analyzer_prompt_ref"`
	AnalyzerPromptVersionID string         `json:"analyzer_prompt_version_id"`
	StructuredOutput        map[string]any `json:"structured_output"`
	Error                   string         `json:"error"`
}

func recommendationSnapshotHash(rec SelfImprovementRecommendation) (string, error) {
	data, err := json.Marshal(recommendationSnapshot{
		Type:                    rec.Type,
		Status:                  rec.Status,
		Confidence:              rec.Confidence,
		Risk:                    rec.Risk,
		Finding:                 rec.Finding,
		NormalizedLesson:        rec.NormalizedLesson,
		Rationale:               rec.Rationale,
		EvidenceFeedbackIDs:     rec.EvidenceFeedbackIDs,
		EvidenceSourceURLs:      rec.EvidenceSourceURLs,
		AttributionConfidence:   rec.AttributionConfidence,
		TargetAssetType:         rec.TargetAssetType,
		TargetAssetID:           rec.TargetAssetID,
		TargetBaseVersionID:     rec.TargetBaseVersionID,
		ProposedPatch:           rec.ProposedPatch,
		ProposedNewBody:         rec.ProposedNewBody,
		SuggestedRolloutScope:   rec.SuggestedRolloutScope,
		AnalyzerPromptRef:       rec.AnalyzerPromptRef,
		AnalyzerPromptVersionID: rec.AnalyzerPromptVersionID,
		StructuredOutput:        rec.StructuredOutput,
		Error:                   rec.Error,
	})
	if err != nil {
		return "", fmt.Errorf("store: hash recommendation snapshot: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func getSelfImprovementRecommendation(q querier, id string) (SelfImprovementRecommendation, error) {
	var rec SelfImprovementRecommendation
	var evidenceIDs, evidenceURLs, structured string
	err := q.QueryRow(`
		SELECT id, workspace_id, feedback_event_id, type, status, confidence, risk,
		       finding, normalized_lesson, rationale, evidence_feedback_ids, evidence_source_urls,
		       attribution_confidence, target_asset_type, target_asset_id, target_base_version_id,
		       proposed_patch, proposed_new_body, suggested_rollout_scope, analyzer_prompt_ref,
		       analyzer_prompt_version_id, structured_output, error, created_at, updated_at
		FROM self_improvement_recommendations
		WHERE id=?`, strings.TrimSpace(id)).
		Scan(&rec.ID, &rec.WorkspaceID, &rec.FeedbackEventID, &rec.Type, &rec.Status, &rec.Confidence, &rec.Risk,
			&rec.Finding, &rec.NormalizedLesson, &rec.Rationale, &evidenceIDs, &evidenceURLs,
			&rec.AttributionConfidence, &rec.TargetAssetType, &rec.TargetAssetID, &rec.TargetBaseVersionID,
			&rec.ProposedPatch, &rec.ProposedNewBody, &rec.SuggestedRolloutScope, &rec.AnalyzerPromptRef,
			&rec.AnalyzerPromptVersionID, &structured, &rec.Error, &rec.CreatedAt, &rec.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SelfImprovementRecommendation{}, &store.ErrNotFound{Msg: fmt.Sprintf("recommendation %q not found", id)}
	}
	if err != nil {
		return SelfImprovementRecommendation{}, err
	}
	rec.EvidenceFeedbackIDs = splitInt64CSV(evidenceIDs)
	rec.EvidenceSourceURLs = splitCSV(evidenceURLs)
	if structured != "" {
		_ = json.Unmarshal([]byte(structured), &rec.StructuredOutput)
	}
	return rec, nil
}

func insertBundleItemEvent(tx *sql.Tx, bundleID, itemID, eventType, actor, reason string, before, after any) error {
	beforeJSON, err := bundleEventJSON(before)
	if err != nil {
		return err
	}
	afterJSON, err := bundleEventJSON(after)
	if err != nil {
		return err
	}
	if strings.TrimSpace(actor) == "" {
		actor = "system"
	}
	if _, err := tx.Exec(`
		INSERT INTO self_improvement_proposal_bundle_item_events (
			id, bundle_id, item_id, event_type, actor, reason, before_json, after_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"bundleevent_"+randomHexID(), bundleID, itemID, strings.TrimSpace(eventType),
		strings.TrimSpace(actor), strings.TrimSpace(reason), beforeJSON, afterJSON,
	); err != nil {
		return fmt.Errorf("store: insert proposal bundle item event: %w", err)
	}
	return nil
}

func bundleEventJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("store: marshal proposal bundle item event: %w", err)
	}
	return string(data), nil
}

func bundleItemInputAuditSnapshot(bundleID, itemID string, item SelfImprovementBundleItemInput) map[string]any {
	return map[string]any{
		"id":                   itemID,
		"bundle_id":            bundleID,
		"operation":            item.Operation,
		"asset_type":           item.AssetType,
		"asset_id":             item.AssetID,
		"base_version_id":      item.BaseVersionID,
		"proposed_ref":         item.ProposedRef,
		"proposed_name":        item.ProposedName,
		"proposed_scope":       item.ProposedScope,
		"proposed_body":        item.ProposedBody,
		"proposed_description": item.ProposedDescription,
		"proposed_enabled":     bundleItemInputEnabled(item),
		"proposed_position":    normalizedBundlePosition(item.ProposedPosition),
		"duplicate_risk":       item.DuplicateRisk,
		"rationale":            item.Rationale,
		"decision":             ProposalBundleDecisionAccepted,
	}
}

func bundleItemAuditSnapshot(item SelfImprovementBundleItem) map[string]any {
	return map[string]any{
		"id":                    item.ID,
		"bundle_id":             item.BundleID,
		"operation":             item.Operation,
		"asset_type":            item.AssetType,
		"asset_id":              item.AssetID,
		"base_version_id":       item.BaseVersionID,
		"proposed_ref":          item.ProposedRef,
		"proposed_name":         item.ProposedName,
		"proposed_scope":        item.ProposedScope,
		"proposed_body":         item.ProposedBody,
		"proposed_description":  item.ProposedDescription,
		"proposed_enabled":      item.ProposedEnabled,
		"proposed_position":     item.ProposedPosition,
		"analyst_proposed_body": item.AnalystProposedBody,
		"duplicate_risk":        item.DuplicateRisk,
		"rationale":             item.Rationale,
		"decision":              item.Decision,
		"decision_reason":       item.DecisionReason,
		"published_version_id":  item.PublishedVersionID,
	}
}

func randomHexID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(b[:])
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nonConvertibleRecommendationType(typ string) bool {
	switch strings.TrimSpace(typ) {
	case "needs_more_context", "no_action", "split_agent", "change_dispatch_wiring":
		return true
	default:
		return false
	}
}

func recommendationProposalChangelog(rec SelfImprovementRecommendation) string {
	for _, value := range []string{rec.NormalizedLesson, rec.Rationale, rec.Finding} {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return "Self-improvement recommendation " + rec.ID
}

func getGuardrailFrom(q querier, ref string) (fleet.Guardrail, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fleet.Guardrail{}, &store.ErrValidation{Msg: "guardrail id is required"}
	}
	var g fleet.Guardrail
	var enabled int
	err := q.QueryRow(`
		SELECT ref, COALESCE(workspace_id, ''), name, description, content, enabled, position, COALESCE(current_version_id, '')
		FROM guardrails
		WHERE id=? OR ref=? OR name=?`, ref, ref, fleet.NormalizeGuardrailName(ref)).
		Scan(&g.ID, &g.WorkspaceID, &g.Name, &g.Description, &g.Content, &enabled, &g.Position, &g.VersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Guardrail{}, &store.ErrNotFound{Msg: fmt.Sprintf("guardrail %q not found", ref)}
	}
	if err != nil {
		return fleet.Guardrail{}, fmt.Errorf("store: read guardrail %s: %w", ref, err)
	}
	g.Enabled = enabled != 0
	return g, nil
}

func readSkill(q querier, ref string) (fleet.Skill, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fleet.Skill{}, &store.ErrValidation{Msg: "skill id is required"}
	}
	var skill fleet.Skill
	err := q.QueryRow(`
		SELECT s.ref, COALESCE(s.workspace_id, ''), COALESCE(s.repo, ''), s.name, s.prompt,
		       COALESCE(sv.id, ''), COALESCE(sv.version_number, 0)
		FROM skills s
		LEFT JOIN skill_versions sv ON sv.id = s.current_version_id
		WHERE s.id=? OR s.ref=? OR s.name=?`, ref, ref, fleet.NormalizeSkillName(ref)).
		Scan(&skill.ID, &skill.WorkspaceID, &skill.Repo, &skill.Name, &skill.Prompt, &skill.VersionID, &skill.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Skill{}, &store.ErrNotFound{Msg: fmt.Sprintf("skill %q not found", ref)}
	}
	if err != nil {
		return fleet.Skill{}, fmt.Errorf("store: read skill %s: %w", ref, err)
	}
	return skill, nil
}

func readSelfImprovementProposalBaseVersion(q querier, targetType, versionID string) (fleet.CatalogVersion, error) {
	var version fleet.CatalogVersion
	var err error
	switch targetType {
	case "prompt":
		err = q.QueryRow(`
			SELECT id, prompt_id, version_number, state, description, content, source_type, source_ref, author, changelog,
			       COALESCE(base_version_id, ''), body_hash, created_at, COALESCE(published_at, '')
			FROM prompt_versions
			WHERE id=?`, versionID).
			Scan(&version.ID, &version.AssetID, &version.Version, &version.State, &version.Description, &version.Content,
				&version.SourceType, &version.SourceRef, &version.Author, &version.Changelog,
				&version.BaseVersionID, &version.BodyHash, &version.CreatedAt, &version.PublishedAt)
	case "skill":
		err = q.QueryRow(`
			SELECT id, skill_id, version_number, state, prompt, source_type, source_ref, author, changelog,
			       COALESCE(base_version_id, ''), body_hash, created_at, COALESCE(published_at, '')
			FROM skill_versions
			WHERE id=?`, versionID).
			Scan(&version.ID, &version.AssetID, &version.Version, &version.State, &version.Prompt,
				&version.SourceType, &version.SourceRef, &version.Author, &version.Changelog,
				&version.BaseVersionID, &version.BodyHash, &version.CreatedAt, &version.PublishedAt)
	case "guardrail":
		var enabled int
		err = q.QueryRow(`
			SELECT id, guardrail_id, version_number, state, description, content, enabled, position, source_type, source_ref,
			       author, changelog, COALESCE(base_version_id, ''), body_hash, created_at, COALESCE(published_at, '')
			FROM guardrail_versions
			WHERE id=?`, versionID).
			Scan(&version.ID, &version.AssetID, &version.Version, &version.State, &version.Description, &version.Content,
				&enabled, &version.Position, &version.SourceType, &version.SourceRef,
				&version.Author, &version.Changelog, &version.BaseVersionID, &version.BodyHash, &version.CreatedAt, &version.PublishedAt)
		version.Enabled = enabled != 0
	default:
		return fleet.CatalogVersion{}, &store.ErrValidation{Msg: fmt.Sprintf("recommendation target type %q is not proposal-convertible", targetType)}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.CatalogVersion{}, &store.ErrNotFound{Msg: fmt.Sprintf("%s version %q not found", targetType, versionID)}
	}
	if err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: read %s version %s: %w", targetType, versionID, err)
	}
	return version, nil
}

func normalizeNewCatalogVersionMetadata(meta fleet.CatalogVersionMetadata, defaultState string) (fleet.CatalogVersionMetadata, error) {
	meta.State = strings.TrimSpace(meta.State)
	if meta.State == "" {
		meta.State = defaultState
	}
	meta.SourceType = strings.TrimSpace(meta.SourceType)
	meta.SourceRef = strings.TrimSpace(meta.SourceRef)
	meta.Author = strings.TrimSpace(meta.Author)
	meta.Changelog = strings.TrimSpace(meta.Changelog)
	return meta, nil
}

func catalogReadErr(kind, ref string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return &store.ErrNotFound{Msg: fmt.Sprintf("%s %q not found", kind, ref)}
	}
	return fmt.Errorf("store: read %s %s: %w", kind, ref, err)
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func splitInt64CSV(s string) []int64 {
	parts := splitCSV(s)
	out := make([]int64, 0, len(parts))
	for _, part := range parts {
		var value int64
		if _, err := fmt.Sscan(part, &value); err == nil && value > 0 {
			out = append(out, value)
		}
	}
	return out
}
