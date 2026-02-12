package poller

import (
	"context"
	"math/rand"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/github"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

type Poller struct {
	cfg    *config.Config
	store  *store.Store
	github *github.Client
	engine *workflow.Engine
	logger zerolog.Logger
	states map[string]*repoState
}

type repoState struct {
	repo     config.RepoConfig
	record   store.RepoRecord
	nextPoll time.Time
	interval time.Duration
}

func New(cfg *config.Config, store *store.Store, githubClient *github.Client, engine *workflow.Engine, logger zerolog.Logger) *Poller {
	return &Poller{
		cfg:    cfg,
		store:  store,
		github: githubClient,
		engine: engine,
		logger: logger.With().Str("component", "poller").Logger(),
		states: make(map[string]*repoState),
	}
}

func (p *Poller) Run(ctx context.Context) error {
	repos, err := p.store.ListRepos(ctx)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		cfgRepo, ok := p.cfg.RepoByName(repo.FullName)
		if !ok || !cfgRepo.Enabled {
			continue
		}
		interval := time.Duration(cfgRepo.PollIntervalSeconds) * time.Second
		p.states[repo.FullName] = &repoState{
			repo:     cfgRepo,
			record:   repo,
			interval: interval,
			nextPoll: time.Now(),
		}
	}
	if len(p.states) == 0 {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		nextRepo, sleepFor := p.nextDue()
		if sleepFor > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleepFor):
			}
		}
		if nextRepo == nil {
			continue
		}
		updated, err := p.pollRepo(ctx, nextRepo)
		if err != nil {
			p.logger.Error().Err(err).Str("repo", nextRepo.repo.FullName).Msg("poll failed")
		}
		p.scheduleNext(nextRepo, updated)
	}
}

func (p *Poller) nextDue() (*repoState, time.Duration) {
	var next *repoState
	var nextTime time.Time
	for _, state := range p.states {
		if next == nil || state.nextPoll.Before(nextTime) {
			next = state
			nextTime = state.nextPoll
		}
	}
	if next == nil {
		return nil, 0
	}
	wait := time.Until(next.nextPoll)
	if wait < 0 {
		wait = 0
	}
	return next, wait
}

func (p *Poller) scheduleNext(state *repoState, updated bool) {
	baseInterval := time.Duration(state.repo.PollIntervalSeconds) * time.Second
	if updated {
		state.interval = baseInterval
	} else {
		maxIdle := time.Duration(p.cfg.Poller.MaxIdleIntervalSeconds) * time.Second
		if state.interval < maxIdle {
			state.interval *= 2
			if state.interval > maxIdle {
				state.interval = maxIdle
			}
		}
	}
	jitterSeconds := p.cfg.Poller.JitterSeconds
	if jitterSeconds < 0 {
		jitterSeconds = 0
	}
	jitter := time.Duration(rand.Intn(jitterSeconds+1)) * time.Second
	state.nextPoll = time.Now().Add(state.interval + jitter)
}

func (p *Poller) pollRepo(ctx context.Context, state *repoState) (bool, error) {
	logger := p.logger.With().Str("repo", state.repo.FullName).Logger()
	issuesUpdated, err := p.pollIssues(ctx, state, logger)
	if err != nil {
		return false, err
	}
	prsUpdated, err := p.pollPRs(ctx, state, logger)
	if err != nil {
		return issuesUpdated, err
	}
	return issuesUpdated || prsUpdated, nil
}

func (p *Poller) pollIssues(ctx context.Context, state *repoState, logger zerolog.Logger) (bool, error) {
	issues, err := p.github.ListIssues(ctx, state.repo.FullName, state.record.LastIssueUpdatedAt, p.cfg.Poller.PerPage, p.cfg.Poller.MaxItemsPerPoll)
	if err != nil {
		return false, err
	}
	if len(issues) == 0 {
		return false, nil
	}
	updated := false
	var latest time.Time
	for _, issue := range issues {
		if issue.UpdatedAt.After(latest) {
			latest = issue.UpdatedAt
		}
		if err := p.engine.HandleIssue(ctx, state.repo, issue); err != nil {
			logger.Error().Err(err).Int("issue_number", issue.Number).Msg("issue workflow failed")
			continue
		}
		updated = true
	}
	if !latest.IsZero() {
		state.record.LastIssueUpdatedAt = &latest
		if err := p.store.UpdateRepoPollState(ctx, state.repo.FullName, &latest, nil); err != nil {
			return updated, err
		}
	}
	return updated, nil
}

func (p *Poller) pollPRs(ctx context.Context, state *repoState, logger zerolog.Logger) (bool, error) {
	prs, err := p.github.ListPullRequests(ctx, state.repo.FullName, state.record.LastPRUpdatedAt, p.cfg.Poller.PerPage, p.cfg.Poller.MaxItemsPerPoll)
	if err != nil {
		return false, err
	}
	if len(prs) == 0 {
		return false, nil
	}
	updated := false
	var latest time.Time
	for _, pr := range prs {
		if pr.UpdatedAt.After(latest) {
			latest = pr.UpdatedAt
		}
		if err := p.engine.HandlePullRequest(ctx, state.repo, pr); err != nil {
			logger.Error().Err(err).Int("pr_number", pr.Number).Msg("pr workflow failed")
			continue
		}
		updated = true
	}
	if !latest.IsZero() {
		state.record.LastPRUpdatedAt = &latest
		if err := p.store.UpdateRepoPollState(ctx, state.repo.FullName, nil, &latest); err != nil {
			return updated, err
		}
	}
	return updated, nil
}
