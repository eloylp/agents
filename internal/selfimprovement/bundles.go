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

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

const (
	ProposalBundleStatusPending   = "pending"
	ProposalBundleStatusPublished = "published"
	ProposalBundleStatusResolved  = "resolved"
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

func proposalBundleFromRow(row store.SelfImprovementProposalBundleRow) SelfImprovementProposalBundle {
	items := make([]SelfImprovementBundleItem, 0, len(row.Items))
	for _, item := range row.Items {
		items = append(items, proposalBundleItemFromRow(item))
	}
	var recommendation *SelfImprovementRecommendation
	if row.Recommendation != nil {
		converted := recommendationFromRow(*row.Recommendation)
		recommendation = &converted
	}
	return SelfImprovementProposalBundle{
		ID:                              row.ID,
		WorkspaceID:                     row.WorkspaceID,
		RecommendationID:                row.RecommendationID,
		RecommendationUpdatedAtSnapshot: row.RecommendationUpdatedAtSnapshot,
		RecommendationSnapshotHash:      row.RecommendationSnapshotHash,
		RecommendationChanged:           row.RecommendationChanged,
		Status:                          row.Status,
		CreatedAt:                       row.CreatedAt,
		UpdatedAt:                       row.UpdatedAt,
		Recommendation:                  recommendation,
		Items:                           items,
	}
}

func proposalBundleItemFromRow(row store.SelfImprovementBundleItemRow) SelfImprovementBundleItem {
	return SelfImprovementBundleItem{
		ID:                  row.ID,
		BundleID:            row.BundleID,
		Operation:           row.Operation,
		AssetType:           row.AssetType,
		AssetID:             row.AssetID,
		BaseVersionID:       row.BaseVersionID,
		ProposedRef:         row.ProposedRef,
		ProposedName:        row.ProposedName,
		ProposedScope:       row.ProposedScope,
		ProposedBody:        row.ProposedBody,
		ProposedDescription: row.ProposedDescription,
		ProposedEnabled:     row.ProposedEnabled,
		ProposedPosition:    row.ProposedPosition,
		AnalystProposedBody: row.AnalystProposedBody,
		DuplicateRisk:       row.DuplicateRisk,
		Rationale:           row.Rationale,
		Decision:            row.Decision,
		DecisionReason:      row.DecisionReason,
		PublishedVersionID:  row.PublishedVersionID,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
		BaseVersion:         row.BaseVersion,
		CurrentVersionID:    row.CurrentVersionID,
		Stale:               row.Stale,
	}
}

func proposalBundleRowFromBundle(bundle SelfImprovementProposalBundle) store.SelfImprovementProposalBundleRow {
	items := make([]store.SelfImprovementBundleItemRow, 0, len(bundle.Items))
	for _, item := range bundle.Items {
		items = append(items, proposalBundleItemRowFromItem(item))
	}
	return store.SelfImprovementProposalBundleRow{
		ID:                              bundle.ID,
		WorkspaceID:                     bundle.WorkspaceID,
		RecommendationID:                bundle.RecommendationID,
		RecommendationUpdatedAtSnapshot: bundle.RecommendationUpdatedAtSnapshot,
		RecommendationSnapshotHash:      bundle.RecommendationSnapshotHash,
		RecommendationChanged:           bundle.RecommendationChanged,
		Status:                          bundle.Status,
		CreatedAt:                       bundle.CreatedAt,
		UpdatedAt:                       bundle.UpdatedAt,
		Recommendation:                  recommendationRowPtr(bundle.Recommendation),
		Items:                           items,
	}
}

func recommendationRowPtr(rec *SelfImprovementRecommendation) *store.SelfImprovementRecommendationRow {
	if rec == nil {
		return nil
	}
	converted := recommendationRowFromRecommendation(*rec)
	return &converted
}

func proposalBundleItemRowFromItem(item SelfImprovementBundleItem) store.SelfImprovementBundleItemRow {
	return store.SelfImprovementBundleItemRow{
		ID:                  item.ID,
		BundleID:            item.BundleID,
		Operation:           item.Operation,
		AssetType:           item.AssetType,
		AssetID:             item.AssetID,
		BaseVersionID:       item.BaseVersionID,
		ProposedRef:         item.ProposedRef,
		ProposedName:        item.ProposedName,
		ProposedScope:       item.ProposedScope,
		ProposedBody:        item.ProposedBody,
		ProposedDescription: item.ProposedDescription,
		ProposedEnabled:     item.ProposedEnabled,
		ProposedPosition:    item.ProposedPosition,
		AnalystProposedBody: item.AnalystProposedBody,
		DuplicateRisk:       item.DuplicateRisk,
		Rationale:           item.Rationale,
		Decision:            item.Decision,
		DecisionReason:      item.DecisionReason,
		PublishedVersionID:  item.PublishedVersionID,
		CreatedAt:           item.CreatedAt,
		UpdatedAt:           item.UpdatedAt,
		BaseVersion:         item.BaseVersion,
		CurrentVersionID:    item.CurrentVersionID,
		Stale:               item.Stale,
	}
}
func createSelfImprovementProposalBundle(st *store.Store, id string) (SelfImprovementProposalBundle, error) {
	recRow, err := st.GetSelfImprovementRecommendation(id)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	rec := recommendationFromRow(recRow)
	if rec.Status != RecommendationStatusRecommended {
		return SelfImprovementProposalBundle{}, &store.ErrValidation{Msg: "recommendation must be proposal-ready before creating a proposal bundle"}
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
	if err := validateNoDuplicateBundleDraftItems(items); err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	snapshotHash, err := recommendationSnapshotHash(rec)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	bundleID := "bundle_" + randomHexID()
	if err := st.Transact(func(tx *store.Tx) error {
		if err := store.InsertSelfImprovementProposalBundleRow(tx, store.SelfImprovementProposalBundleRow{
			ID:                              bundleID,
			WorkspaceID:                     rec.WorkspaceID,
			RecommendationID:                rec.ID,
			RecommendationUpdatedAtSnapshot: rec.UpdatedAt,
			RecommendationSnapshotHash:      snapshotHash,
		}); err != nil {
			return err
		}
		for _, item := range items {
			if err := validateBundleItemForCreate(tx, rec.WorkspaceID, item); err != nil {
				return err
			}
			if err := validateNoOpenBundleItemDraft(tx, "", item); err != nil {
				return err
			}
			item, err = hydrateBundleItemMetadata(tx, item)
			if err != nil {
				return err
			}
			itemID := "bundleitem_" + randomHexID()
			if err := store.InsertSelfImprovementProposalBundleItemRow(tx, store.SelfImprovementBundleItemRow{
				ID:                  itemID,
				BundleID:            bundleID,
				Operation:           item.Operation,
				AssetType:           item.AssetType,
				AssetID:             item.AssetID,
				BaseVersionID:       item.BaseVersionID,
				ProposedRef:         item.ProposedRef,
				ProposedName:        item.ProposedName,
				ProposedScope:       item.ProposedScope,
				ProposedBody:        item.ProposedBody,
				ProposedDescription: item.ProposedDescription,
				ProposedEnabled:     bundleItemInputEnabled(item),
				ProposedPosition:    normalizedBundlePosition(item.ProposedPosition),
				AnalystProposedBody: item.ProposedBody,
				DuplicateRisk:       item.DuplicateRisk,
				Rationale:           item.Rationale,
				Decision:            ProposalBundleDecisionAccepted,
			}); err != nil {
				return err
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
	row, err := st.GetSelfImprovementProposalBundleRow(id)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	bundle := proposalBundleFromRow(row)
	recRow, err := st.GetSelfImprovementRecommendation(bundle.RecommendationID)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	rec := recommendationFromRow(recRow)
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

func getSelfImprovementProposalBundle(tx *store.Tx, id string) (SelfImprovementProposalBundle, error) {
	row, err := store.GetSelfImprovementProposalBundleRowTx(tx, id)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	bundle := proposalBundleFromRow(row)
	recRow, err := store.GetSelfImprovementRecommendationFrom(tx, bundle.RecommendationID)
	if err == nil {
		rec := recommendationFromRow(recRow)
		hash, hashErr := recommendationSnapshotHash(rec)
		if hashErr == nil {
			bundle.RecommendationChanged = rec.UpdatedAt != bundle.RecommendationUpdatedAtSnapshot || hash != bundle.RecommendationSnapshotHash
		}
		bundle.Recommendation = &rec
	}
	if err := hydrateProposalBundleReadState(tx, &bundle); err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	return bundle, nil
}

func updateSelfImprovementProposalBundleItemWithActor(st *store.Store, bundleID, itemID string, in SelfImprovementBundleItemUpdate, actor string) (SelfImprovementProposalBundle, bool, error) {
	bundle, item, err := getBundleAndItem(st, bundleID, itemID)
	if err != nil {
		return SelfImprovementProposalBundle{}, false, err
	}
	if bundle.Status != ProposalBundleStatusPending {
		return SelfImprovementProposalBundle{}, false, &store.ErrValidation{Msg: "only pending proposal bundle items can be edited"}
	}
	body := strings.TrimSpace(in.ProposedBody)
	if body == "" {
		return SelfImprovementProposalBundle{}, false, &store.ErrValidation{Msg: "proposal bundle item body is required"}
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
	if proposalBundleItemDraftEqual(item, after) {
		bundle, err = getSelfImprovementProposalBundleFromStore(st, bundle.ID)
		return bundle, false, err
	}
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
			if err := validateNoOpenBundleItemDraft(tx, bundle.ID, SelfImprovementBundleItemInput{
				Operation:     item.Operation,
				AssetType:     item.AssetType,
				ProposedRef:   ref,
				ProposedScope: scope,
			}); err != nil {
				return err
			}
		}
		if err := store.UpdateSelfImprovementProposalBundleItemDraftRow(tx, bundle.ID, proposalBundleItemRowFromItem(after)); err != nil {
			return err
		}
		return insertBundleItemEvent(tx, bundle.ID, item.ID, "edited", actor, "", bundleItemAuditSnapshot(item), bundleItemAuditSnapshot(after))
	}); err != nil {
		return SelfImprovementProposalBundle{}, false, err
	}
	bundle, err = getSelfImprovementProposalBundleFromStore(st, bundle.ID)
	return bundle, true, err
}

func proposalBundleItemDraftEqual(a, b SelfImprovementBundleItem) bool {
	return a.ProposedRef == b.ProposedRef &&
		a.ProposedName == b.ProposedName &&
		a.ProposedScope == b.ProposedScope &&
		a.ProposedBody == b.ProposedBody &&
		a.ProposedDescription == b.ProposedDescription &&
		a.ProposedEnabled == b.ProposedEnabled &&
		a.ProposedPosition == b.ProposedPosition
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
		publishedVersions := 0
		for _, item := range bundle.Items {
			before := bundleItemAuditSnapshot(item)
			switch item.Decision {
			case ProposalBundleDecisionRejected:
				if err := insertBundleItemEvent(tx, bundle.ID, item.ID, "finalized", actor, "bundle resolved", before, before); err != nil {
					return err
				}
				continue
			case ProposalBundleDecisionLinkedExisting:
				if err := insertBundleItemEvent(tx, bundle.ID, item.ID, "finalized", actor, "bundle resolved", before, before); err != nil {
					return err
				}
				continue
			case ProposalBundleDecisionAccepted, ProposalBundleDecisionPending:
			default:
				return &store.ErrValidation{Msg: fmt.Sprintf("unsupported proposal bundle item decision %q", item.Decision)}
			}
			if err := validateNoOpenBundleItemDraft(tx, bundle.ID, SelfImprovementBundleItemInput{
				Operation:     item.Operation,
				AssetType:     item.AssetType,
				AssetID:       item.AssetID,
				ProposedRef:   item.ProposedRef,
				ProposedScope: item.ProposedScope,
			}); err != nil {
				return err
			}
			versionID, err := publishBundleCatalogItem(tx, item, bundle.WorkspaceID, bundle.RecommendationID)
			if err != nil {
				return err
			}
			if err := store.MarkSelfImprovementProposalBundleItemPublishedRow(tx, item.ID, versionID, ProposalBundleDecisionPublished); err != nil {
				return err
			}
			after := item
			after.Decision = ProposalBundleDecisionPublished
			after.PublishedVersionID = versionID
			if err := insertBundleItemEvent(tx, bundle.ID, item.ID, "published", actor, "", before, bundleItemAuditSnapshot(after)); err != nil {
				return err
			}
			publishedVersions++
		}
		status := ProposalBundleStatusResolved
		if publishedVersions > 0 {
			status = ProposalBundleStatusPublished
		}
		if err := store.UpdateSelfImprovementProposalBundleStatusRow(tx, bundle.ID, status); err != nil {
			return err
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
		if err := store.UpdateSelfImprovementProposalBundleStatusRow(tx, bundle.ID, ProposalBundleStatusDiscarded); err != nil {
			return err
		}
		if err := store.DiscardPendingSelfImprovementProposalBundleItemRows(tx, bundle.ID, ProposalBundleDecisionDiscarded); err != nil {
			return err
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

func validateNoDuplicateBundleDraftItems(items []SelfImprovementBundleItemInput) error {
	seen := map[string]struct{}{}
	for _, item := range items {
		key := bundleDraftKey(item)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			return &store.ErrValidation{Msg: fmt.Sprintf("proposal bundle contains more than one draft for %s", bundleDraftLabel(item))}
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateNoOpenBundleItemDraft(q querier, excludeBundleID string, item SelfImprovementBundleItemInput) error {
	if bundleDraftKey(item) == "" {
		return nil
	}
	existing, err := store.FindOpenSelfImprovementBundleItemDraft(q, excludeBundleID, store.SelfImprovementBundleItemInputRow{
		Operation:     item.Operation,
		AssetType:     item.AssetType,
		AssetID:       item.AssetID,
		ProposedRef:   item.ProposedRef,
		ProposedScope: item.ProposedScope,
	})
	if err == nil {
		return &store.ErrConflict{Msg: fmt.Sprintf("%s already has an open proposal draft in bundle %s", bundleDraftLabel(item), existing.BundleID)}
	}
	var nf *store.ErrNotFound
	if errors.As(err, &nf) {
		return nil
	}
	return err
}

func bundleDraftKey(item SelfImprovementBundleItemInput) string {
	switch strings.TrimSpace(item.Operation) {
	case ProposalBundleOperationUpdateExisting:
		if strings.TrimSpace(item.AssetType) == "" || strings.TrimSpace(item.AssetID) == "" {
			return ""
		}
		return strings.TrimSpace(item.AssetType) + "\x00" + strings.TrimSpace(item.AssetID)
	case ProposalBundleOperationCreateNew:
		if strings.TrimSpace(item.AssetType) == "" || strings.TrimSpace(item.ProposedScope) == "" || strings.TrimSpace(item.ProposedRef) == "" {
			return ""
		}
		return strings.TrimSpace(item.AssetType) + "\x00" + strings.ToLower(strings.TrimSpace(item.ProposedScope)) + "\x00" + strings.ToLower(strings.TrimSpace(item.ProposedRef))
	default:
		return ""
	}
}

func bundleDraftLabel(item SelfImprovementBundleItemInput) string {
	switch strings.TrimSpace(item.Operation) {
	case ProposalBundleOperationUpdateExisting:
		return strings.TrimSpace(item.AssetType) + "/" + strings.TrimSpace(item.AssetID)
	case ProposalBundleOperationCreateNew:
		return strings.TrimSpace(item.AssetType) + "/" + strings.TrimSpace(item.ProposedScope) + "/" + strings.TrimSpace(item.ProposedRef)
	default:
		return "catalog item"
	}
}

func validateBundleUpdateExisting(q querier, item SelfImprovementBundleItemInput) error {
	if strings.TrimSpace(item.AssetID) == "" {
		return &store.ErrValidation{Msg: "proposal bundle update item asset id is required"}
	}
	if strings.TrimSpace(item.BaseVersionID) == "" {
		return &store.ErrValidation{Msg: "proposal bundle update item base version is required"}
	}
	current, err := store.CurrentSelfImprovementCatalogVersionID(q, item.AssetType, item.AssetID)
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
	return store.ValidateSelfImprovementBundleScope(q, item.ProposedScope, workspaceID)
}

func hydrateBundleItemMetadata(q querier, item SelfImprovementBundleItemInput) (SelfImprovementBundleItemInput, error) {
	if item.AssetType != "guardrail" || item.Operation != ProposalBundleOperationUpdateExisting {
		return item, nil
	}
	guardrail, err := store.ReadSelfImprovementGuardrail(q, item.AssetID)
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

func hydrateProposalBundleReadState(q querier, bundle *SelfImprovementProposalBundle) error {
	for i := range bundle.Items {
		if bundle.Items[i].BaseVersionID == "" {
			continue
		}
		base, err := store.ReadSelfImprovementCatalogVersion(q, bundle.Items[i].AssetType, bundle.Items[i].BaseVersionID)
		if err != nil {
			return err
		}
		bundle.Items[i].BaseVersion = &base
		if current, err := store.CurrentSelfImprovementCatalogVersionID(q, bundle.Items[i].AssetType, bundle.Items[i].AssetID); err == nil {
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
			if _, err := store.CurrentSelfImprovementCatalogVersionID(tx, item.AssetType, linkedAssetID); err != nil {
				return err
			}
		}
		if err := store.UpdateSelfImprovementProposalBundleItemDecisionRow(tx, bundle.ID, proposalBundleItemRowFromItem(after)); err != nil {
			return err
		}
		if err := insertBundleItemEvent(tx, bundle.ID, item.ID, eventType, actor, strings.TrimSpace(reason), bundleItemAuditSnapshot(item), bundleItemAuditSnapshot(after)); err != nil {
			return err
		}
		if decision == ProposalBundleDecisionRejected && len(bundle.Items) == 1 {
			if err := store.UpdateSelfImprovementProposalBundleStatusRow(tx, bundle.ID, ProposalBundleStatusDiscarded); err != nil {
				return err
			}
			return store.UpdateSelfImprovementRecommendationDecisionRow(tx, bundle.RecommendationID, RecommendationStatusRejected, reason)
		}
		return nil
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
		current, err := store.CurrentSelfImprovementCatalogVersionID(tx, item.AssetType, item.AssetID)
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
		prompt, err := store.ReadSelfImprovementPrompt(tx, item.AssetID)
		if err != nil {
			return "", err
		}
		version, err = store.CreatePromptDraftTx(tx, prompt.ID, prompt.Description, item.ProposedBody, meta)
	case "skill":
		version, err = store.CreateSkillDraftTx(tx, item.AssetID, item.ProposedBody, meta)
	case "guardrail":
		guardrail, err := store.ReadSelfImprovementGuardrail(tx, item.AssetID)
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
	scope, repo := store.ParseSelfImprovementBundleScope(item.ProposedScope, workspaceID)
	if err := store.EnsureSelfImprovementCatalogRefAvailable(tx, item.AssetType, item.ProposedRef); err != nil {
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
		skill, err := store.ReadSelfImprovementSkill(tx, item.ProposedRef)
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
		guardrail, err := store.ReadSelfImprovementGuardrail(tx, item.ProposedRef)
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
	normalized, err := normalizeCatalogVersionMetadata(meta, "proposal")
	if err != nil {
		return err
	}
	return store.UpdatePublishedCatalogVersionProvenanceTx(tx, assetType, versionID, normalized.SourceType, normalized.SourceRef, normalized.Author, normalized.Changelog)
}

func normalizeCatalogVersionMetadata(meta fleet.CatalogVersionMetadata, defaultState string) (fleet.CatalogVersionMetadata, error) {
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
	return store.InsertSelfImprovementProposalBundleItemEventRow(tx, store.SelfImprovementProposalBundleItemEventRow{
		BundleID:   bundleID,
		ItemID:     itemID,
		EventType:  strings.TrimSpace(eventType),
		Actor:      strings.TrimSpace(actor),
		Reason:     strings.TrimSpace(reason),
		BeforeJSON: beforeJSON,
		AfterJSON:  afterJSON,
	})
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
