package workflow

import (
	"context"
	"sync"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
)

type processorHandler interface {
	HandleIssueLabelEvent(context.Context, IssueRequest) error
	HandlePullRequestLabelEvent(context.Context, PRRequest) error
}

type Processor struct {
	handler    processorHandler
	wg         *sync.WaitGroup
	workerWg   sync.WaitGroup
	issueQueue chan IssueRequest
	prQueue    chan PRRequest
	startOnce  sync.Once
	stopOnce   sync.Once
	logger     zerolog.Logger
}

func NewProcessor(cfg *config.Config, handler processorHandler, wg *sync.WaitGroup, logger zerolog.Logger) *Processor {
	return &Processor{
		handler:    handler,
		wg:         wg,
		issueQueue: make(chan IssueRequest, cfg.HTTP.IssueQueueBuffer),
		prQueue:    make(chan PRRequest, cfg.HTTP.PRQueueBuffer),
		logger:     logger.With().Str("component", "workflow_processor").Logger(),
	}
}

func (p *Processor) Start(ctx context.Context) (chan<- IssueRequest, chan<- PRRequest) {
	p.startOnce.Do(func() {
		p.logger.Info().Msg("starting workflow processor")
		p.workerWg.Add(2)
		p.wg.Add(2)
		go p.runIssueWorker(ctx)
		go p.runPRWorker(ctx)
	})
	return p.issueQueue, p.prQueue
}

func (p *Processor) Stop(ctx context.Context) {
	p.stopOnce.Do(func() {
		p.logger.Info().Msg("stopping workflow processor")
		close(p.issueQueue)
		close(p.prQueue)
		done := make(chan struct{})
		go func() {
			p.workerWg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
		}
	})
}

func (p *Processor) runIssueWorker(ctx context.Context) {
	defer p.workerWg.Done()
	defer p.wg.Done()
	for req := range p.issueQueue {
		processCtx := ctx
		if ctx.Err() != nil {
			processCtx = context.Background()
		}
		if err := p.handler.HandleIssueLabelEvent(processCtx, req); err != nil {
			p.logger.Error().Err(err).Str("repo", req.Repo.FullName).Int("issue_number", req.Issue.Number).Msg("failed to process issue webhook")
		}
	}
	p.logger.Info().Msg("issue queue drained")
}

func (p *Processor) runPRWorker(ctx context.Context) {
	defer p.workerWg.Done()
	defer p.wg.Done()
	for req := range p.prQueue {
		processCtx := ctx
		if ctx.Err() != nil {
			processCtx = context.Background()
		}
		if err := p.handler.HandlePullRequestLabelEvent(processCtx, req); err != nil {
			p.logger.Error().Err(err).Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Msg("failed to process pr webhook")
		}
	}
	p.logger.Info().Msg("pr queue drained")
}
