package workflow

import (
	"context"
	"sync"

	"github.com/rs/zerolog"
)

type processorHandler interface {
	HandleIssueLabelEvent(context.Context, IssueRequest) error
	HandlePullRequestLabelEvent(context.Context, PRRequest) error
}

type Processor struct {
	handler   processorHandler
	wg        *sync.WaitGroup
	channels  *DataChannels
	startOnce sync.Once
	stopOnce  sync.Once
	logger    zerolog.Logger
	ctxMu     sync.RWMutex
	drainCtx  context.Context
}

func NewProcessor(channels *DataChannels, handler processorHandler, wg *sync.WaitGroup, logger zerolog.Logger) *Processor {
	return &Processor{
		handler:  handler,
		wg:       wg,
		channels: channels,
		logger:   logger.With().Str("component", "workflow_processor").Logger(),
	}
}

func (p *Processor) Start(ctx context.Context) *DataChannels {
	p.startOnce.Do(func() {
		p.logger.Info().Msg("starting workflow processor")
		p.wg.Add(2)
		go p.runIssueWorker(ctx)
		go p.runPRWorker(ctx)
	})
	return p.channels
}

func (p *Processor) Stop(ctx context.Context) {
	p.stopOnce.Do(func() {
		p.logger.Info().Msg("stopping workflow processor")
		p.setDrainCtx(ctx)
		p.channels.Close()
		done := make(chan struct{})
		go func() {
			p.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			p.logger.Warn().Msg("processor stop timed out, workers may still be running")
		}
	})
}

func (p *Processor) runIssueWorker(ctx context.Context) {
	defer p.wg.Done()
	for req := range p.channels.IssueChan() {
		if err := p.handler.HandleIssueLabelEvent(p.processingCtx(ctx), req); err != nil {
			p.logger.Error().Err(err).Str("repo", req.Repo.FullName).Int("issue_number", req.Issue.Number).Msg("failed to process issue webhook")
		}
	}
	p.logger.Info().Msg("issue queue drained")
}

func (p *Processor) runPRWorker(ctx context.Context) {
	defer p.wg.Done()
	for req := range p.channels.PRChan() {
		if err := p.handler.HandlePullRequestLabelEvent(p.processingCtx(ctx), req); err != nil {
			p.logger.Error().Err(err).Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Msg("failed to process pr webhook")
		}
	}
	p.logger.Info().Msg("pr queue drained")
}

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
