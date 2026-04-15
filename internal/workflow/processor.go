package workflow

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

type processorHandler interface {
	HandleEvent(context.Context, Event) error
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

// Run starts workers and blocks until ctx is cancelled and the queue is drained
// (or the shutdown timeout elapses).
func (p *Processor) Run(ctx context.Context) error {
	p.logger.Info().Int("workers", p.workers).Msg("starting workflow processor")
	var wg sync.WaitGroup
	wg.Add(p.workers)
	for range p.workers {
		go p.runWorker(ctx, &wg)
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

func (p *Processor) runWorker(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	for ev := range p.channels.EventChan() {
		if err := p.handler.HandleEvent(p.processingCtx(ctx), ev); err != nil {
			p.logger.Error().Err(err).Str("repo", ev.Repo.FullName).Str("kind", ev.Kind).Int("number", ev.Number).Msg("failed to process webhook event")
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
