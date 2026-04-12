package workflow

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

type processorHandler interface {
	HandleIssueLabelEvent(context.Context, IssueRequest) error
	HandlePullRequestLabelEvent(context.Context, PRRequest) error
}

type Processor struct {
	handler    processorHandler
	channels   *DataChannels
	workers    int
	shutdown   time.Duration
	logger     zerolog.Logger
	drainCtx   context.Context
	drainReady chan struct{} // closed by setDrainCtx; processingCtx waits on it
}

func NewProcessor(channels *DataChannels, handler processorHandler, workers int, shutdownTimeout time.Duration, logger zerolog.Logger) *Processor {
	if workers <= 0 {
		workers = 1
	}
	return &Processor{
		handler:    handler,
		channels:   channels,
		workers:    workers,
		shutdown:   shutdownTimeout,
		logger:     logger.With().Str("component", "workflow_processor").Logger(),
		drainReady: make(chan struct{}),
	}
}

// Run starts workers and blocks until ctx is cancelled and queues are drained
// (or the shutdown timeout elapses).
func (p *Processor) Run(ctx context.Context) error {
	p.logger.Info().Int("workers_per_type", p.workers).Msg("starting workflow processor")
	var wg sync.WaitGroup
	wg.Add(p.workers * 2)
	for range p.workers {
		go p.runIssueWorker(ctx, &wg)
		go p.runPRWorker(ctx, &wg)
	}

	<-ctx.Done()
	p.logger.Info().Msg("stopping workflow processor")
	drainCtx, cancel := context.WithTimeout(context.Background(), p.shutdown)
	defer cancel()
	p.setDrainCtx(drainCtx)
	p.channels.Close()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-drainCtx.Done():
		p.logger.Warn().Msg("processor stop timed out, workers may still be running")
	}
	return nil
}

func (p *Processor) runIssueWorker(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	for req := range p.channels.IssueChan() {
		if err := p.handler.HandleIssueLabelEvent(p.processingCtx(ctx), req); err != nil {
			p.logger.Error().Err(err).Str("repo", req.Repo.FullName).Int("issue_number", req.Issue.Number).Msg("failed to process issue webhook")
		}
	}
}

func (p *Processor) runPRWorker(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	for req := range p.channels.PRChan() {
		if err := p.handler.HandlePullRequestLabelEvent(p.processingCtx(ctx), req); err != nil {
			p.logger.Error().Err(err).Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Msg("failed to process pr webhook")
		}
	}
}

// processingCtx returns the appropriate context for a queued item.
// During normal operation the run context is returned as-is. Once shutdown
// begins, the run context is already cancelled, so processingCtx blocks on
// drainReady until Run installs the real drain context via setDrainCtx.
// This guarantees that any item dequeued during the brief race window between
// ctx cancellation and setDrainCtx being called still receives a live context
// with the full shutdown deadline — not an already-cancelled sentinel.
// The channel close in setDrainCtx provides the happens-before guarantee so
// no separate mutex is required on the drainCtx read.
func (p *Processor) processingCtx(ctx context.Context) context.Context {
	if ctx.Err() == nil {
		return ctx
	}
	// Wait for Run to install the real drain context. After drainReady is
	// closed the write to drainCtx has already happened (setDrainCtx closes
	// the channel only after assigning drainCtx), so reading drainCtx here is
	// safe without additional synchronisation.
	<-p.drainReady
	return p.drainCtx
}

func (p *Processor) setDrainCtx(ctx context.Context) {
	p.drainCtx = ctx
	close(p.drainReady)
}
