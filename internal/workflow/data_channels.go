package workflow

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrIssueQueueFull = errors.New("issue queue full")
	ErrPRQueueFull    = errors.New("pr queue full")
)

type DataChannels struct {
	issueQueue chan IssueRequest
	prQueue    chan PRRequest
	closeOnce  sync.Once
}

func NewDataChannels(issueBuffer, prBuffer int) *DataChannels {
	return &DataChannels{
		issueQueue: make(chan IssueRequest, issueBuffer),
		prQueue:    make(chan PRRequest, prBuffer),
	}
}

// PushIssue enqueues a request without blocking. The select has three arms:
// context cancellation (caller is shutting down), successful enqueue, and the
// default case which fires immediately when the channel buffer is full.
func (dc *DataChannels) PushIssue(ctx context.Context, req IssueRequest) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case dc.issueQueue <- req:
		return nil
	default:
		return ErrIssueQueueFull
	}
}

func (dc *DataChannels) PushPR(ctx context.Context, req PRRequest) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case dc.prQueue <- req:
		return nil
	default:
		return ErrPRQueueFull
	}
}

func (dc *DataChannels) IssueChan() <-chan IssueRequest {
	return dc.issueQueue
}

func (dc *DataChannels) PRChan() <-chan PRRequest {
	return dc.prQueue
}

// Close shuts down both queues. sync.Once prevents a double-close panic if
// Stop is called concurrently or more than once.
func (dc *DataChannels) Close() {
	dc.closeOnce.Do(func() {
		close(dc.issueQueue)
		close(dc.prQueue)
	})
}
