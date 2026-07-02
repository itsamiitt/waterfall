package pgoutbox

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/pg"
)

// Relay is the "message relay" half of the transactional-outbox pattern: it claims pending
// job_outbox rows and feeds them to the in-process worker queue. It runs as a trusted system
// consumer on a BYPASSRLS connection (it processes every tenant's jobs); each claimed job
// carries its own principal, so execution stays tenant-scoped (G1).
//
// Claiming uses FOR UPDATE SKIP LOCKED so multiple relay replicas can poll concurrently
// without double-claiming (competing consumers). A claim stamps claimed_at; a row is only
// re-claimable once it is stale (claimed_at older than the visibility timeout) — which is
// exactly how a crashed relay's in-flight jobs are recovered.
type Relay struct {
	conn        *pg.Conn // privileged (BYPASSRLS) connection; used only from the drain loop
	queue       *job.Queue
	visibility  time.Duration
	batch       int
	maxAttempts int
	onDead      func(jobID string, attempts int)

	done chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

// RelayOption configures a Relay.
type RelayOption func(*Relay)

// WithMaxAttempts caps how many times a row may be delivered before it is dead-lettered
// (parked: dead=true, pending=false) instead of redelivered. Guards against a poison job that
// crashes its worker every time and would otherwise loop forever. n <= 0 leaves the default.
func WithMaxAttempts(n int) RelayOption {
	return func(r *Relay) {
		if n > 0 {
			r.maxAttempts = n
		}
	}
}

// WithDeadLetterHook registers a callback invoked once per row when it is dead-lettered
// (for a metric / alert). It runs on the drain goroutine, so it must not block.
func WithDeadLetterHook(fn func(jobID string, attempts int)) RelayOption {
	return func(r *Relay) { r.onDead = fn }
}

// NewRelay builds a relay over a privileged connection and the in-process queue. visibility
// is how long a claimed-but-unfinished row waits before it may be re-claimed (crash recovery).
func NewRelay(conn *pg.Conn, queue *job.Queue, visibility time.Duration, opts ...RelayOption) *Relay {
	if visibility <= 0 {
		visibility = 30 * time.Second
	}
	r := &Relay{conn: conn, queue: queue, visibility: visibility, batch: 64, maxAttempts: 10, done: make(chan struct{})}
	for _, o := range opts {
		o(r)
	}
	return r
}

// deadRow identifies a row parked in this claim cycle.
type deadRow struct {
	jobID    string
	attempts int
}

// claim atomically claims up to batch pending rows that are unclaimed or stale-claimed,
// skipping rows another relay currently holds. Each claim increments the row's attempt count;
// a row whose attempts would exceed maxAttempts is parked (dead=true, pending=false) instead
// of delivered. Returns the live jobs to deliver and the rows that were dead-lettered.
func (r *Relay) claim() ([]*job.Job, []deadRow, error) {
	res, err := r.conn.QueryParams(`with sel as (
			select job_id from job_outbox
			where pending and not dead
			  and (claimed_at is null or claimed_at < now() - make_interval(secs => $1::double precision))
			order by created_at
			for update skip locked
			limit $2::int
		),
		bumped as (
			update job_outbox o set
				attempts   = o.attempts + 1,
				claimed_at = now(),
				updated_at = now(),
				dead       = (o.attempts + 1 > $3::int),
				pending    = (o.attempts + 1 <= $3::int),
				last_error = case when o.attempts + 1 > $3::int
					then 'dead-lettered after ' || (o.attempts + 1)::text ||
					     ' delivery attempts without reaching a terminal state'
					else o.last_error end
			from sel where o.job_id = sel.job_id
			returning o.job_id, o.payload, o.dead, o.attempts
		)
		select job_id, payload, dead, attempts from bumped`,
		r.visibility.Seconds(), r.batch, r.maxAttempts)
	if err != nil {
		return nil, nil, err
	}
	var jobs []*job.Job
	var dead []deadRow
	for _, row := range res.Rows {
		if row[0] == nil {
			continue
		}
		if row[2] != nil && *row[2] == "t" { // dead
			att := 0
			if row[3] != nil {
				att, _ = strconv.Atoi(*row[3])
			}
			dead = append(dead, deadRow{jobID: *row[0], attempts: att})
			continue
		}
		if row[1] == nil {
			continue
		}
		var j job.Job
		if err := json.Unmarshal([]byte(*row[1]), &j); err != nil {
			return nil, nil, err
		}
		jobs = append(jobs, &j)
	}
	return jobs, dead, nil
}

// DrainOnce claims a batch of pending jobs and submits them to the queue, returning how many
// were enqueued. A job that cannot be enqueued (queue saturated) stays claimed and is
// re-claimed after the visibility timeout. Rows that exceeded maxAttempts are dead-lettered
// (the hook, if set, fires once per parked row).
func (r *Relay) DrainOnce() (int, error) {
	jobs, dead, err := r.claim()
	if err != nil {
		return 0, err
	}
	if r.onDead != nil {
		for _, d := range dead {
			r.onDead(d.jobID, d.attempts)
		}
	}
	n := 0
	for _, j := range jobs {
		if r.queue.Submit(j) {
			n++
		}
	}
	return n, nil
}

// Start drains immediately (recovering pending jobs at startup) then on an interval.
func (r *Relay) Start(interval time.Duration) {
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	_, _ = r.DrainOnce()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-r.done:
				return
			case <-t.C:
				_, _ = r.DrainOnce()
			}
		}
	}()
}

// Stop halts the drain loop.
func (r *Relay) Stop() {
	r.once.Do(func() { close(r.done) })
	r.wg.Wait()
}
