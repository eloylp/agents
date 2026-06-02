// Package selfimprovement owns recommendation and proposal-bundle use cases.
package selfimprovement

import "github.com/eloylp/agents/internal/store"

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
	return CreateSelfImprovementProposal(s.store.DB(), id)
}

func (s *Service) CreateProposalBundle(id string) (store.SelfImprovementProposalBundle, error) {
	return CreateSelfImprovementProposalBundle(s.store.DB(), id)
}

func (s *Service) UpdateProposalBundleItem(bundleID, itemID string, in store.SelfImprovementBundleItemUpdate, actor string) (store.SelfImprovementProposalBundle, error) {
	return UpdateSelfImprovementProposalBundleItemWithActor(s.store.DB(), bundleID, itemID, in, actor)
}

func (s *Service) RejectProposalBundleItem(bundleID, itemID, reason, actor string) (store.SelfImprovementProposalBundle, error) {
	return RejectSelfImprovementProposalBundleItemWithActor(s.store.DB(), bundleID, itemID, reason, actor)
}

func (s *Service) LinkProposalBundleItem(bundleID, itemID, assetID, reason, actor string) (store.SelfImprovementProposalBundle, error) {
	return LinkSelfImprovementProposalBundleItemWithActor(s.store.DB(), bundleID, itemID, assetID, reason, actor)
}

func (s *Service) PublishProposalBundle(bundleID, actor string) (store.SelfImprovementProposalBundle, error) {
	return PublishSelfImprovementProposalBundleWithActor(s.store.DB(), bundleID, actor)
}

func (s *Service) DiscardProposalBundle(bundleID, actor string) (store.SelfImprovementProposalBundle, error) {
	return DiscardSelfImprovementProposalBundleWithActor(s.store.DB(), bundleID, actor)
}
