package workflow

import (
	"context"
	"time"
	"sync"

	"github.com/rs/zerolog"
)

type processorHandler interface {
	HandleIssueLabelEvent(context.Context, IssueRequest) error
	HandlePullRequestLabelEvent(context.Context, PRRequest) error
}

type Processor struct {
	handler   processorHandler
	channels  *DataChannels
	shutdown  time.Duration
	logger    zerolog.Logger
	ctxMu     sync.RWMutex
	drainCtx  context.Context
}

func NewProcessor(channels *DataChannels, handler processorHandler, shutdownTimeout time.Duration, logger zerolog.Logger) *Processor {
	return &Processor{
		handler:  handler,
		channels: channels,
		shutdown: shutdownTimeout,
		logger:   logger.With().Str("component", "workflow_processor").Logger(),
	}
}

// Run starts workers and blocks until ctx is cancelled and queues are drained
// (or the shutdown timeout elapses).
func (p *Processor) Run(ctx context.Context) error {
	p.logger.Info().Msg("starting workflow processor")
	var wg sync.WaitGroup
	wg.Add(2)
	go p.runIssueWorker(ctx, &wg)
	go p.runPRWorker(ctx, &wg)

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
	p.logger.Info().Msg("issue queue drained")
}

func (p *Processor) runPRWorker(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	for req := range p.channels.PRChan() {
		if err := p.handler.HandlePullRequestLabelEvent(p.processingCtx(ctx), req); err != nil {
			p.logger.Error().Err(err).Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Msg("failed to process pr webhook")
		}
	}
	p.logger.Info().Msg("pr queue drained")
}

// processingCtx returns the appropriate context for a queued item.
// During normal operation the run context is returned as-is. Once shutdown
// begins the run context is already cancelled, so we fall back to the drain
// context (set by Stop) which carries the shutdown deadline. This lets
// workers finish in-flight items without being aborted the moment the
// shutdown signal arrives.
func (p *Processor) processingCtx(ctx context.Context) context.Context {
	if ctx.Err() == nil {
		return ctx
	}
	p.ctxMu.RLock()
	defer p.ctxMu.RUnlock()
	if p.drainCtx != nil {
		return p.drainCtx
	}
	return ctx
}

func (p *Processor) setDrainCtx(ctx context.Context) {
	p.ctxMu.Lock()
	defer p.ctxMu.Unlock()
	p.drainCtx = ctx
}
