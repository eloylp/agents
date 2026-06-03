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

func (s *Service) CreateProposal(id string) (SelfImprovementProposal, error) {
	return createSelfImprovementProposal(s.store, id)
}

func (s *Service) ListRecommendations(workspace, status string, limit int) ([]SelfImprovementRecommendation, error) {
	rows, err := s.store.ListSelfImprovementRecommendations(workspace, status, limit)
	if err != nil {
		return nil, err
	}
	out := make([]SelfImprovementRecommendation, 0, len(rows))
	for _, row := range rows {
		out = append(out, recommendationFromRow(row))
	}
	return out, nil
}

func (s *Service) GetRecommendation(id string) (SelfImprovementRecommendation, error) {
	row, err := s.store.GetSelfImprovementRecommendation(id)
	if err != nil {
		return SelfImprovementRecommendation{}, err
	}
	return recommendationFromRow(row), nil
}

func (s *Service) ListProposals(recommendationID string) ([]SelfImprovementProposal, error) {
	rows, err := s.store.ListSelfImprovementProposals(recommendationID)
	if err != nil {
		return nil, err
	}
	out := make([]SelfImprovementProposal, 0, len(rows))
	for _, row := range rows {
		out = append(out, proposalFromRow(row))
	}
	return out, nil
}

func (s *Service) CreateProposalBundle(id string) (SelfImprovementProposalBundle, error) {
	return createSelfImprovementProposalBundle(s.store, id)
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

func (s *Service) ListRecommendationsWithProposals(workspace string, limit int) ([]SelfImprovementRecommendation, error) {
	rows, err := s.store.ListSelfImprovementRecommendationsWithProposals(workspace, limit)
	if err != nil {
		return nil, err
	}
	out := make([]SelfImprovementRecommendation, 0, len(rows))
	for _, row := range rows {
		out = append(out, recommendationFromRow(row))
	}
	return out, nil
}

func (s *Service) UpdateProposalBundleItem(bundleID, itemID string, in SelfImprovementBundleItemUpdate, actor string) (SelfImprovementProposalBundle, error) {
	return updateSelfImprovementProposalBundleItemWithActor(s.store, bundleID, itemID, in, actor)
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
