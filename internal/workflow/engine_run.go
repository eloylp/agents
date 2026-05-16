package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// runAgent executes agent using the per-event cfg snapshot the caller
// loaded from SQLite. Backend resolution and runner construction happen
// here from that same snapshot, so the agent's backend, prompt, skills,
// and runner configuration all come from one consistent read.
func (e *Engine) runAgent(ctx context.Context, ev Event, agent fleet.Agent, cfg *config.Config) error {
	workspaceID := eventWorkspaceID(ev)
	backend := cfg.ResolveBackend(agent.Backend)
	if backend == "" {
		return fmt.Errorf("agent %q: no runner available for backend %q", agent.Name, agent.Backend)
	}
	backendCfg, ok := cfg.Daemon.AIBackends[backend]
	if !ok {
		return fmt.Errorf("agent %q: no runner for backend %q", agent.Name, backend)
	}
	if fleet.IsPinnedModelUnavailable(agent.Model, backendCfg) {
		return fmt.Errorf(
			"agent %q: configured model %q is not available for backend %q; run backend discovery and update the agent model",
			agent.Name,
			agent.Model,
			backend,
		)
	}

	rootEventID, dispatchDepth, err := extractDispatchContext(ev)
	if err != nil {
		return err
	}

	// Build dispatch context fields for dispatched agents.
	var invokedBy, reason, parentSpanID string
	if ev.Kind == "agent.dispatch" {
		payload, err := decodeAgentDispatchPayload(ev)
		if err != nil {
			return err
		}
		invokedBy = payload.InvokedBy
		reason = payload.Reason
		parentSpanID = payload.ParentSpanID
	}

	// Serialise the read/run/write sequence for this (agent, repo) pair to
	// prevent a lost-update race on memory. Without this lock two overlapping
	// runs (cron tick + dispatch, two manual triggers, or any combination)
	// would both read the same old memory, run independently, and whichever
	// finishes last would silently clobber the other's persisted state.
	// Held across the entire read/run/write sequence even when memory is
	// disabled so concurrent runs of the same (agent, repo) still serialise
	// on dispatch and trace recording.
	runKey := workspaceID + "\x00" + agent.Name + "\x00" + ev.Repo.FullName
	e.runLock.acquire(runKey)
	defer e.runLock.release(runKey)

	// Build the roster of peer agents for this repo.
	roster := BuildRoster(cfg, workspaceID, ev.Repo.FullName, agent.Name)

	promptPayload := ev.Payload

	// Memory load is gated on the agent's AllowMemory flag and on a configured
	// backend. The same gate applies to the persist path below, so an agent
	// with AllowMemory=false neither reads nor writes memory regardless of the
	// trigger surface (event-driven, dispatched, or on-demand /run).
	memoryEnabled := agent.IsAllowMemory() && e.memory != nil
	var existingMemory string
	if memoryEnabled {
		mem, err := e.memory.ReadMemory(workspaceID, agent.Name, ev.Repo.FullName)
		if err != nil {
			return fmt.Errorf("agent %q: read memory: %w", agent.Name, err)
		}
		existingMemory = mem
	}

	guardrails, err := e.store.ReadWorkspacePromptGuardrails(workspaceID)
	if err != nil {
		return fmt.Errorf("agent %q: load guardrails: %w", agent.Name, err)
	}
	guardrails = slices.Insert(guardrails, 0, dynamicWorkspaceGuardrail(workspaceID, agent, cfg.Repos))

	rendered, err := ai.RenderAgentPrompt(agent, cfg.Skills, guardrails, ai.PromptContext{
		Repo:          ev.Repo.FullName,
		Number:        ev.Number,
		Backend:       backend,
		EventKind:     ev.Kind,
		Actor:         ev.Actor,
		Payload:       promptPayload,
		Roster:        roster,
		InvokedBy:     invokedBy,
		Reason:        reason,
		RootEventID:   rootEventID,
		DispatchDepth: dispatchDepth,
		Memory:        existingMemory,
		HasMemory:     memoryEnabled,
	})
	if err != nil {
		return fmt.Errorf("agent %q: render prompt: %w", agent.Name, err)
	}
	workflow := fmt.Sprintf("%s:%s", backend, agent.Name)
	logger := e.logger.With().
		Str("repo", ev.Repo.FullName).
		Int("number", ev.Number).
		Str("agent", agent.Name).
		Str("backend", backend).
		Str("root_event_id", rootEventID).
		Int("dispatch_depth", dispatchDepth).
		Logger()
	if invokedBy != "" {
		logger = logger.With().Str("invoked_by", invokedBy).Logger()
	}

	spanStart := time.Now()
	spanID := GenEventID()
	composedPrompt := rendered.System
	if rendered.User != "" {
		if composedPrompt != "" {
			composedPrompt += "\n\n"
		}
		composedPrompt += rendered.User
	}

	if e.budgetStore != nil {
		if err := e.budgetStore.CheckBudgetsWithLogger(workspaceID, ev.Repo.FullName, backend, agent.Name, logger); err != nil {
			spanEnd := time.Now()
			var queueWaitMs int64
			if !ev.EnqueuedAt.IsZero() {
				queueWaitMs = spanStart.Sub(ev.EnqueuedAt).Milliseconds()
			}
			if e.traceRec != nil {
				status := "error"
				var exceeded *store.BudgetExceededError
				if errors.As(err, &exceeded) {
					status = "budget_exceeded"
				}
				e.traceRec.RecordSpan(SpanInput{
					SpanID:        spanID,
					WorkspaceID:   workspaceID,
					RootEventID:   rootEventID,
					ParentSpanID:  parentSpanID,
					Agent:         agent.Name,
					Backend:       backend,
					Repo:          ev.Repo.FullName,
					EventKind:     ev.Kind,
					InvokedBy:     invokedBy,
					Number:        ev.Number,
					DispatchDepth: dispatchDepth,
					QueueWaitMs:   queueWaitMs,
					StartedAt:     spanStart,
					FinishedAt:    spanEnd,
					Status:        status,
					ErrorMsg:      err.Error(),
					Prompt:        composedPrompt,
				})
			}
			logger.Warn().Err(err).Msg("agent run rejected by token budget")
			return fmt.Errorf("agent %q: %w", agent.Name, err)
		}
	}

	logger.Info().Str("workflow", workflow).Msg("invoking ai agent")

	if e.runTracker != nil {
		e.runTracker.StartRun(agent.Name)
		defer e.runTracker.FinishRun(agent.Name)
	}

	runner := e.runnerBuilder(workspaceID, backend, backendCfg)

	// Live-stream registration: announce the run to the publisher so the
	// runners view can show an in-flight row with this span_id. The stream
	// body itself is backed by persisted trace_steps rows; RecordStep fans
	// each row out after it commits.
	var onLine func([]byte)
	if e.streamPub != nil {
		e.streamPub.BeginRun(BeginRunInput{
			SpanID:      spanID,
			EventID:     ev.ID,
			WorkspaceID: workspaceID,
			Agent:       agent.Name,
			Backend:     backend,
			Repo:        ev.Repo.FullName,
			EventKind:   ev.Kind,
			StartedAt:   spanStart,
		})
		defer e.streamPub.EndRun(spanID)
	}
	if e.stepRec != nil {
		parser := ai.NewTraceStepParser(backend)
		onLine = func(line []byte) {
			if parser == nil {
				return
			}
			for _, step := range parser(line) {
				e.stepRec.RecordStep(spanID, step)
			}
		}
	}
	resp, runErr := runner.Run(ctx, ai.Request{
		Workflow: workflow,
		Repo:     ev.Repo.FullName,
		Number:   ev.Number,
		Model:    agent.Model,
		System:   rendered.System,
		User:     rendered.User,
		OnLine:   onLine,
	})
	spanEnd := time.Now()

	// Compute queue-wait duration from when the event was enqueued to when
	// the runner started. Zero when EnqueuedAt is unset (e.g. cron events
	// created before this field existed).
	var queueWaitMs int64
	if !ev.EnqueuedAt.IsZero() {
		queueWaitMs = spanStart.Sub(ev.EnqueuedAt).Milliseconds()
	}

	// Record the trace span regardless of outcome. The composed prompt
	// (system + user) is captured so operators can inspect "what
	// exactly did the agent see" from the Traces / Runners UI; the
	// observe store gzips it before persistence. Token usage comes
	// from the runner's CLI parser.
	if e.traceRec != nil {
		status, errMsg := "success", ""
		if runErr != nil {
			status = "error"
			errMsg = runErr.Error()
		}
		e.traceRec.RecordSpan(SpanInput{
			SpanID:           spanID,
			WorkspaceID:      workspaceID,
			RootEventID:      rootEventID,
			ParentSpanID:     parentSpanID,
			Agent:            agent.Name,
			Backend:          backend,
			Repo:             ev.Repo.FullName,
			EventKind:        ev.Kind,
			InvokedBy:        invokedBy,
			Number:           ev.Number,
			DispatchDepth:    dispatchDepth,
			QueueWaitMs:      queueWaitMs,
			ArtifactsCount:   len(resp.Artifacts),
			Summary:          resp.Summary,
			StartedAt:        spanStart,
			FinishedAt:       spanEnd,
			Status:           status,
			ErrorMsg:         errMsg,
			Prompt:           composedPrompt,
			InputTokens:      resp.Usage.InputTokens,
			OutputTokens:     resp.Usage.OutputTokens,
			CacheReadTokens:  resp.Usage.CacheReadTokens,
			CacheWriteTokens: resp.Usage.CacheWriteTokens,
		})
	}

	// Record transcript steps when available and the run was not already
	// parsed incrementally from stdout. The incremental path is used for
	// known streaming backends so live streams can replay and tail DB rows.
	if e.stepRec != nil && onLine == nil && len(resp.Steps) > 0 {
		e.stepRec.RecordSteps(spanID, resp.Steps)
	}

	if runErr != nil {
		return fmt.Errorf("agent %q: %w", agent.Name, runErr)
	}
	logger.Info().Int("artifacts_stored", len(resp.Artifacts)).Msg("agent run completed")

	// Persist memory after a successful run, gated on the same flag that
	// controlled the load above. An empty resp.Memory is treated as "agent
	// did not return updated memory, preserve existing" rather than "wipe":
	// a structured-output omission must not silently destroy stored memory.
	// To explicitly clear, the agent must return a non-empty sentinel that
	// the operator opts into; the runtime's default is preserve-on-empty.
	// A write failure here is logged but does not fail the whole run: the
	// agent's GitHub-side artifacts are already in place,
	// and surfacing a memory-write error as a run failure would mask the
	// successful work that just landed. The next run reads from disk so the
	// stale state is naturally observable; if persistence is consistently
	// failing the operator will see it in logs.
	if memoryEnabled && resp.Memory != "" {
		if err := e.memory.WriteMemory(workspaceID, agent.Name, ev.Repo.FullName, resp.Memory); err != nil {
			logger.Error().Err(err).Msg("agent run completed but memory write failed")
		}
	}

	// Process any dispatch requests from the agent's response, threading the
	// current spanID so child runs can link back to their parent span.
	if e.dispatcher != nil && len(resp.Dispatch) > 0 {
		if err := e.dispatcher.ProcessDispatches(ctx, agent, ev, rootEventID, dispatchDepth, spanID, resp.Dispatch); err != nil {
			return fmt.Errorf("agent %q: dispatch: %w", agent.Name, err)
		}
	}

	return nil
}
