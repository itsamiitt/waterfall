package job

// Queue is a bounded, two-lane (premium/bulk) in-process queue. Bounded capacity is the
// back-pressure mechanism (docs/11 §4): when a lane is full, Submit fails fast so the API
// can shed load (HTTP 429) rather than growing memory without limit. A production build
// swaps this for the Kafka-protocol log (ADR-0013); the interface is the seam.
type Queue struct {
	high chan *Job
	low  chan *Job
}

// NewQueue builds a queue with the given per-lane capacity.
func NewQueue(capacity int) *Queue {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue{
		high: make(chan *Job, capacity),
		low:  make(chan *Job, capacity),
	}
}

// Depth returns the number of jobs currently queued across both lanes (for a saturation
// gauge, docs/20).
func (q *Queue) Depth() int {
	return len(q.high) + len(q.low)
}

// Submit enqueues j without blocking. It returns false if the target lane is full
// (caller should shed). Selection is by j.Priority.
func (q *Queue) Submit(j *Job) bool {
	lane := q.low
	if j.Priority >= PriorityPremium {
		lane = q.high
	}
	select {
	case lane <- j:
		return true
	default:
		return false // full -> shed
	}
}

// dequeue blocks until a job is available or done is closed, preferring the premium lane.
// It returns (nil, false) when done is closed and no premium job is immediately ready.
func (q *Queue) dequeue(done <-chan struct{}) (*Job, bool) {
	// Fast path: prefer a ready premium job.
	select {
	case j := <-q.high:
		return j, true
	default:
	}
	select {
	case <-done:
		return nil, false
	case j := <-q.high:
		return j, true
	case j := <-q.low:
		return j, true
	}
}
