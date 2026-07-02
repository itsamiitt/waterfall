package job

import (
	"context"
	"time"
)

// Submitter accepts a job for asynchronous execution. It is the seam between the API and
// the delivery mechanism: the in-process QueueSubmitter (this file) offers no crash
// durability, while the durable submitter (internal/durable) persists the job and a
// publish-intent atomically (transactional outbox) so it survives a crash. Both satisfy
// this interface, so the gateway is agnostic to which is wired.
type Submitter interface {
	// Submit accepts j for execution. accepted=false with err==nil means the job was
	// shed under back-pressure (the API surfaces 429). A non-nil err is an internal fault.
	Submit(ctx context.Context, j *Job) (accepted bool, err error)
}

// QueueSubmitter persists a job as queued and enqueues it on the in-process Queue. It
// sheds (accepted=false) when the queue is saturated. Not crash-durable: a process exit
// loses queued-but-unstarted jobs — use the durable submitter for at-least-once delivery.
type QueueSubmitter struct {
	store Store
	queue *Queue
	now   func() time.Time
}

// NewQueueSubmitter builds an in-process submitter.
func NewQueueSubmitter(store Store, queue *Queue, now func() time.Time) *QueueSubmitter {
	if now == nil {
		now = time.Now
	}
	return &QueueSubmitter{store: store, queue: queue, now: now}
}

var _ Submitter = (*QueueSubmitter)(nil)

func (q *QueueSubmitter) Submit(ctx context.Context, j *Job) (bool, error) {
	// Persist queued BEFORE enqueue so the worker's later state updates always win the
	// ordering (the channel send happens-before the worker's dequeue).
	j.Status = StatusQueued
	if err := q.store.Put(ctx, j); err != nil {
		return false, err
	}
	if !q.queue.Submit(j) {
		j.Status = StatusFailed
		j.Err = "queue_full: shed under back-pressure"
		j.UpdatedAt = q.now()
		_ = q.store.Put(ctx, j)
		return false, nil
	}
	return true, nil
}
