package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// handleDispatchEvent fires the target agent named in ev.Payload["target_agent"]
// directly, bypassing normal binding lookup.
func (e *Engine) handleDispatchEvent(ctx context.Context, ev Event) error {
	targetName, _ := ev.Payload["target_agent"].(string)
	if targetName == "" {
		return fmt.Errorf("agent.dispatch event missing target_agent in payload")
	}
	workspaceID := eventWorkspaceID(ev)

	// Read the four entity sets fresh from SQLite for this event. The
	// returned cfg is a per-event snapshot, no caching across events, no
	// reload chain. Cost is one SQLite snapshot read (~111µs for typical
	// fleet sizes); irrelevant at this daemon's traffic.
	cfg, err := e.loadCfg()
	if err != nil {
		return err
	}

	repo, ok := cfg.RepoByNameInWorkspace(ev.Repo.FullName, workspaceID)
	if !ok || !repo.Enabled {
		e.logger.Warn().Str("repo", ev.Repo.FullName).Msg("dispatch event for disabled or unknown repo, skipping")
		return nil
	}

	agent, ok := cfg.AgentByNameInWorkspace(targetName, workspaceID)
	if !ok {
		return fmt.Errorf("dispatch: target agent %q not found in workspace %q", targetName, workspaceID)
	}
	if !agentScopeAllowsRepo(agent, repo) {
		return fmt.Errorf("dispatch: target agent %q scope does not allow repo %q in workspace %q", targetName, repo.Name, workspaceID)
	}

	// agents.run events arrive from the HTTP /agents/run endpoint with no prior
	// dedup claim. Gate them here so two near-simultaneous on-demand requests for
	// the same (agent, repo) do not launch duplicate runs within the dedup window.
	//
	// agent.dispatch events skip this block: ProcessDispatches already claimed and
	// committed the dedup slot before enqueuing the event. Re-claiming would see
	// the committed entry and self-suppress every dispatched run. The enqueue-side
	// claim is the authoritative gate; handleDispatchEvent only executes it.
	if ev.Kind == "agents.run" && e.dispatcher != nil {
		dedupRepo := dedupRepoKey(workspaceID, repo.Name)
		if !e.dispatcher.dedup.TryClaimForDispatch(targetName, dedupRepo, ev.Number, time.Now()) {
			e.logger.Info().
				Str("repo", ev.Repo.FullName).
				Str("target", targetName).
				Msg("on-demand run skipped: agent already claimed within dedup window")
			e.runsDeduped.Add(1)
			return nil
		}
		e.dispatcher.dedup.MarkWebhookRunInFlight(targetName, dedupRepo, ev.Number)
	}

	// Autonomous (cron-fired) runs use the cron bucket dedup window so a
	// cron tick that fires moments after a dispatch already claimed the slot
	// self-suppresses (matches the old scheduler.executeAgentRun behavior).
	// Rollback on error so the slot is freed for the next tick; finalize on
	// success so the dedup window is preserved.
	if ev.Kind == "cron" && e.dispatcher != nil {
		if !e.dispatcher.TryMarkAutonomousRun(workspaceID, targetName, repo.Name, time.Now()) {
			e.logger.Info().
				Str("repo", ev.Repo.FullName).
				Str("target", targetName).
				Msg("autonomous run skipped: dispatch already claimed within dedup window")
			e.runsDeduped.Add(1)
			return nil
		}
	}

	e.logger.Info().
		Str("repo", ev.Repo.FullName).
		Str("target", targetName).
		Int("number", ev.Number).
		Str("invoked_by", ev.Actor).
		Str("kind", ev.Kind).
		Msg("running dispatched agent")

	runErr := e.runAgent(ctx, ev, agent, cfg)

	// Release the on-demand claim taken above for agents.run.
	if ev.Kind == "agents.run" && e.dispatcher != nil {
		dedupRepo := dedupRepoKey(workspaceID, repo.Name)
		if runErr != nil {
			e.dispatcher.dedup.AbandonWebhookRun(targetName, dedupRepo, ev.Number)
		} else {
			e.dispatcher.dedup.FinalizeWebhookRun(targetName, dedupRepo, ev.Number)
		}
	}
	// Release the cron bucket mark taken above for autonomous runs.
	if ev.Kind == "cron" && e.dispatcher != nil {
		if runErr != nil {
			e.dispatcher.RollbackAutonomousRun(workspaceID, targetName, repo.Name)
		} else {
			e.dispatcher.FinalizeAutonomousRun(workspaceID, targetName, repo.Name)
		}
	}
	// Notify the autonomous scheduler so its lastRuns map (which drives the
	// per-binding schedule display in /agents) reflects this run's outcome.
	// Fired only for autonomous events, webhook/agents.run/dispatch runs
	// have their own provenance and don't update the cron schedule view.
	if ev.Kind == "cron" && e.lastRunRec != nil {
		status := "success"
		if runErr != nil {
			status = "error"
		}
		e.lastRunRec.RecordLastRun(workspaceID, targetName, repo.Name, time.Now(), status)
	}
	return runErr
}

// extractDispatchContext extracts root event ID and dispatch depth from ev.
// For non-dispatch events, it generates a new root event ID using ev.ID if
// set, or a fresh random ID.
func extractDispatchContext(ev Event) (rootEventID string, depth int) {
	if ev.Kind == "agent.dispatch" {
		rootEventID, _ = ev.Payload["root_event_id"].(string)
		if d, ok := parseDispatchDepth(ev.Payload["dispatch_depth"]); ok {
			depth = d
		}
		return rootEventID, depth
	}
	// Regular event: use event ID as root, depth 0.
	if ev.ID != "" {
		return ev.ID, 0
	}
	return GenEventID(), 0
}

func parseDispatchDepth(value any) (int, bool) {
	switch d := value.(type) {
	case int:
		return d, true
	case int64:
		return intFromInt64(d)
	case float64:
		if math.Trunc(d) != d || d < float64(math.MinInt) || d > float64(math.MaxInt) {
			return 0, false
		}
		return int(d), true
	case json.Number:
		parsed, err := d.Int64()
		if err != nil {
			return 0, false
		}
		return intFromInt64(parsed)
	case string:
		parsed, err := strconv.ParseInt(d, 10, 0)
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

func intFromInt64(value int64) (int, bool) {
	if value < int64(math.MinInt) || value > int64(math.MaxInt) {
		return 0, false
	}
	return int(value), true
}
