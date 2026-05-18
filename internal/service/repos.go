package service

import (
	"database/sql"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func (s *Service) UpsertRepo(r fleet.Repo) error {
	if err := fleet.ValidateRepoName(r.Name); err != nil {
		return &store.ErrValidation{Msg: err.Error()}
	}
	return s.withTx("upsert repo", func(tx *sql.Tx) error {
		return store.UpsertRepoTx(tx, r)
	})
}

func (s *Service) EnableWorkspaceRepo(workspace, repo string, enabled bool) error {
	return s.withTx("enable repo", func(tx *sql.Tx) error {
		return store.EnableWorkspaceRepoTx(tx, workspace, repo, enabled)
	})
}

func (s *Service) DeleteWorkspaceRepo(workspace, repo string) error {
	return s.withDeleteTx("delete repo", func(tx *sql.Tx) error {
		return store.DeleteWorkspaceRepoTx(tx, workspace, repo)
	})
}
