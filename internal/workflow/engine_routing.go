package workflow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

// fanOut runs all agents matched for ev in parallel, capped by e.maxConcurrent.
// When a dedup store is configured, each agent run is gated through a
// TryClaim/CommitClaim/AbandonClaim sequence keyed on (agent, repo, number) so
// that concurrent or near-simultaneous events for the same item do not produce
// duplicate runs within the dedup window.
// A failing agent does not abort the others; all errors are joined and returned.
func (e *Engine) fanOut(ctx context.Context, ev Event) error {
	// Read the four entity sets from SQLite for this event. The cfg
	// snapshot scopes the agent lookup and the runAgent calls beneath it
	// to a single consistent epoch.
	cfg, err := e.loadCfg()
	if err != nil {
		return err
	}

	matched := e.agentsForEvent(cfg, ev)
	if len(matched) == 0 {
		e.logger.Info().
			Str("repo", ev.Repo.FullName).
			Str("kind", ev.Kind).
			Msg("no bindings matched event, skipping")
		return nil
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	sem := semaphore.NewWeighted(int64(e.maxConcurrent))
	for _, agent := range matched {
		if err := sem.Acquire(ctx, 1); err != nil {
			// Context cancelled before we could run all matched agents.
			break
		}
		wg.Add(1)
		go func(a fleet.Agent) {
			defer wg.Done()
			defer sem.Release(1)
			dedupRepo := dedupRepoKey(eventWorkspaceID(ev), ev.Repo.FullName)

			// Gate through the dedup store when configured, but only for
			// item-scoped events (number > 0).  Repo-level events such as
			// push have number=0 and must never be collapsed, each push is
			// a distinct event with a different head_sha, so two quick pushes
			// to the same repo should both trigger their bound agents.
			if e.dispatcher != nil && ev.Number > 0 {
				if !e.dispatcher.dedup.TryClaimForDispatch(a.Name, dedupRepo, ev.Number, time.Now()) {
					e.logger.Debug().
						Str("agent", a.Name).
						Str("repo", ev.Repo.FullName).
						Int("number", ev.Number).
						Msg("run skipped: agent already claimed within dedup window")
					e.runsDeduped.Add(1)
					return
				}
				// Increment the in-flight refcount so that the claim persists
				// past the TTL window for the duration of the run. Without this
				// a long-running agent (> dedup_window_seconds) would allow a
				// second identical event to pass the TTL check and start a
				// concurrent duplicate run.
				e.dispatcher.dedup.MarkWebhookRunInFlight(a.Name, dedupRepo, ev.Number)
			}

			// Abandon the in-flight marker and pending claim on panic so that
			// future events can retry. Only applies when a claim was taken (number > 0).
			defer func() {
				if r := recover(); r != nil {
					if e.dispatcher != nil && ev.Number > 0 {
						e.dispatcher.dedup.AbandonWebhookRun(a.Name, dedupRepo, ev.Number)
					}
					e.logger.Error().
						Interface("panic", r).
						Str("agent", a.Name).
						Str("repo", ev.Repo.FullName).
						Int("number", ev.Number).
						Msg("panic in agent run; claim abandoned")
					panic(r)
				}
			}()

			if err := e.runAgent(ctx, ev, a, cfg); err != nil {
				// Abandon on failure so that a retry or a subsequent event can
				// claim the slot and attempt the run again.
				if e.dispatcher != nil && ev.Number > 0 {
					e.dispatcher.dedup.AbandonWebhookRun(a.Name, dedupRepo, ev.Number)
				}
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			} else {
				// Commit the claim and release the in-flight marker. CommitClaim
				// marks the entry as committed so the TTL window stays active;
				// FinalizeWebhookRun decrements the refcount while preserving
				// the TTL entry, together they suppress duplicate runs until
				// the window expires without blocking new events after it does.
				if e.dispatcher != nil && ev.Number > 0 {
					e.dispatcher.dedup.CommitClaim(a.Name, dedupRepo, ev.Number)
					e.dispatcher.dedup.FinalizeWebhookRun(a.Name, dedupRepo, ev.Number)
				}
			}
		}(agent)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// agentsForEvent returns all enabled agents bound to ev.Repo whose binding
// matches ev. A label binding matches when ev is a labeled event and the
// payload label is in the binding's Labels slice. An event binding matches
// when ev.Kind appears in the binding's Events slice.
// cfg must be a snapshot already held by the caller to ensure a single
// consistent epoch across the lookup and the subsequent runAgent calls.
func (e *Engine) agentsForEvent(cfg *config.Config, ev Event) []fleet.Agent {
	workspaceID := eventWorkspaceID(ev)
	repo, ok := cfg.RepoByNameInWorkspace(ev.Repo.FullName, workspaceID)
	if !ok || !repo.Enabled {
		return nil
	}

	isLabeled := slices.Contains(labeledKinds, ev.Kind)
	label := ""
	if isLabeled {
		if v, ok := ev.Payload["label"]; ok {
			label, _ = v.(string)
		}
	}

	seen := make(map[string]struct{})
	var matched []fleet.Agent
	for _, b := range repo.Use {
		if !b.IsEnabled() {
			continue
		}
		var matches bool
		switch {
		case b.IsLabel() && isLabeled && label != "":
			matches = containsNormalized(b.Labels, label)
		case b.IsEvent():
			matches = containsNormalized(b.Events, ev.Kind)
		}
		if !matches {
			continue
		}
		if _, dup := seen[b.Agent]; dup {
			continue
		}
		agent, ok := cfg.AgentByNameInWorkspace(b.Agent, workspaceID)
		if !ok || !agentScopeAllowsRepo(agent, repo) {
			continue
		}
		seen[b.Agent] = struct{}{}
		matched = append(matched, agent)
	}
	return matched
}

func containsNormalized(haystack []string, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	return slices.ContainsFunc(haystack, func(s string) bool {
		return strings.ToLower(strings.TrimSpace(s)) == needle
	})
}
