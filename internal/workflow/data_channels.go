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

// QueueStat describes the current depth and capacity of one event queue.
type QueueStat struct {
	Buffered int
	Capacity int
}

// QueueStats returns the current depth and capacity of the issue and PR queues.
// Reading channel length and capacity is safe without holding the mutex because
// these are intrinsic properties of the channel value and the lock only guards
// the closed flag and send operations.
func (dc *DataChannels) QueueStats() (issues, prs QueueStat) {
	return QueueStat{len(dc.issueQueue), cap(dc.issueQueue)},
		QueueStat{len(dc.prQueue), cap(dc.prQueue)}
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
