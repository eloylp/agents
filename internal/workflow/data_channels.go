package workflow

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrIssueQueueFull = errors.New("issue queue full")
	ErrPRQueueFull    = errors.New("pr queue full")
	ErrQueueClosed    = errors.New("queue closed")
)

type DataChannels struct {
	issueQueue chan IssueRequest
	prQueue    chan PRRequest
	mu         sync.RWMutex
	closed     bool
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
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	if dc.closed {
		return ErrQueueClosed
	}
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
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	if dc.closed {
		return ErrQueueClosed
	}
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

// Close shuts down both queues. The write lock ensures no in-flight Push call
// can race with channel closure. Subsequent Close calls are safe because the
// closed flag is checked first.
func (dc *DataChannels) Close() {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if dc.closed {
		return
	}
	dc.closed = true
	close(dc.issueQueue)
	close(dc.prQueue)
}
