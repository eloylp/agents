// Package selfimprovement owns recommendation and proposal-bundle use cases.
package selfimprovement

import (
	"errors"

	"github.com/eloylp/agents/internal/store"
)

// Service coordinates self-improvement recommendation workflows over the
// persistence primitives exposed by store.
type Service struct {
	store *store.Store
}

// New constructs a self-improvement use-case service.
func New(st *store.Store) *Service {
	return &Service{store: st}
}

func (s *Service) ListRecommendations(workspace, status string, limit int) ([]SelfImprovementRecommendation, error) {
	return s.ListRecommendationsPage(workspace, status, limit, 0)
}

func (s *Service) ListRecommendationsPage(workspace, status string, limit, offset int) ([]SelfImprovementRecommendation, error) {
	rows, err := s.store.ListSelfImprovementRecommendationsPage(workspace, status, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]SelfImprovementRecommendation, 0, len(rows))
	for _, row := range rows {
		rec := recommendationFromRow(row)
		if bundle, err := s.GetProposalBundle(rec.ID); err == nil {
			rec.ProposalBundle = &bundle
		} else {
			var nf *store.ErrNotFound
			if !errors.As(err, &nf) {
				return nil, err
			}
		}
		out = append(out, rec)
	}
	return out, nil
}

func (s *Service) CountRecommendations(workspace, status string) (int, error) {
	return s.store.CountSelfImprovementRecommendations(workspace, status)
}

func (s *Service) GetRecommendation(id string) (SelfImprovementRecommendation, error) {
	row, err := s.store.GetSelfImprovementRecommendation(id)
	if err != nil {
		return SelfImprovementRecommendation{}, err
	}
	rec := recommendationFromRow(row)
	if bundle, err := s.GetProposalBundle(rec.ID); err == nil {
		rec.ProposalBundle = &bundle
	} else {
		var nf *store.ErrNotFound
		if !errors.As(err, &nf) {
			return SelfImprovementRecommendation{}, err
		}
	}
	return rec, nil
}

func (s *Service) GetProposalBundle(id string) (SelfImprovementProposalBundle, error) {
	return getSelfImprovementProposalBundleFromStore(s.store, id)
}

func (s *Service) ListRecommendationsWithBundles(workspace string, limit int) ([]SelfImprovementRecommendation, error) {
	recs, err := s.ListRecommendations(workspace, "", limit)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		bundle, err := s.GetProposalBundle(recs[i].ID)
		if err == nil {
			recs[i].ProposalBundle = &bundle
			continue
		}
		var nf *store.ErrNotFound
		if errors.As(err, &nf) {
			continue
		}
		return nil, err
	}
	return recs, nil
}

func (s *Service) UpdateProposalBundleItem(bundleID, itemID string, in SelfImprovementBundleItemUpdate, actor string) (SelfImprovementProposalBundle, error) {
	bundle, _, err := updateSelfImprovementProposalBundleItemWithActor(s.store, bundleID, itemID, in, actor)
	if err != nil {
		return SelfImprovementProposalBundle{}, err
	}
	return bundle, nil
}

func (s *Service) RejectProposalBundleItem(bundleID, itemID, reason, actor string) (SelfImprovementProposalBundle, error) {
	return rejectSelfImprovementProposalBundleItemWithActor(s.store, bundleID, itemID, reason, actor)
}

func (s *Service) LinkProposalBundleItem(bundleID, itemID, assetID, reason, actor string) (SelfImprovementProposalBundle, error) {
	return linkSelfImprovementProposalBundleItemWithActor(s.store, bundleID, itemID, assetID, reason, actor)
}

func (s *Service) PublishProposalBundle(bundleID, actor string) (SelfImprovementProposalBundle, error) {
	return publishSelfImprovementProposalBundleWithActor(s.store, bundleID, actor)
}

func (s *Service) DiscardProposalBundle(bundleID, actor string) (SelfImprovementProposalBundle, error) {
	return discardSelfImprovementProposalBundleWithActor(s.store, bundleID, actor)
}
