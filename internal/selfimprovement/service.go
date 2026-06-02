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

func (s *Service) CreateProposal(id string) (store.SelfImprovementProposal, error) {
	return createSelfImprovementProposal(s.store, id)
}

func (s *Service) CreateProposalBundle(id string) (store.SelfImprovementProposalBundle, error) {
	return createSelfImprovementProposalBundle(s.store, id)
}

func (s *Service) GetProposalBundle(id string) (store.SelfImprovementProposalBundle, error) {
	return getSelfImprovementProposalBundleFromStore(s.store, id)
}

func (s *Service) ListRecommendationsWithBundles(workspace string, limit int) ([]store.SelfImprovementRecommendation, error) {
	recs, err := s.store.ListSelfImprovementRecommendations(workspace, "", limit)
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

func (s *Service) UpdateProposalBundleItem(bundleID, itemID string, in store.SelfImprovementBundleItemUpdate, actor string) (store.SelfImprovementProposalBundle, error) {
	return updateSelfImprovementProposalBundleItemWithActor(s.store, bundleID, itemID, in, actor)
}

func (s *Service) RejectProposalBundleItem(bundleID, itemID, reason, actor string) (store.SelfImprovementProposalBundle, error) {
	return rejectSelfImprovementProposalBundleItemWithActor(s.store, bundleID, itemID, reason, actor)
}

func (s *Service) LinkProposalBundleItem(bundleID, itemID, assetID, reason, actor string) (store.SelfImprovementProposalBundle, error) {
	return linkSelfImprovementProposalBundleItemWithActor(s.store, bundleID, itemID, assetID, reason, actor)
}

func (s *Service) PublishProposalBundle(bundleID, actor string) (store.SelfImprovementProposalBundle, error) {
	return publishSelfImprovementProposalBundleWithActor(s.store, bundleID, actor)
}

func (s *Service) DiscardProposalBundle(bundleID, actor string) (store.SelfImprovementProposalBundle, error) {
	return discardSelfImprovementProposalBundleWithActor(s.store, bundleID, actor)
}
