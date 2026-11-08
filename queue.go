package notification

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// SendFunc performs one delivery attempt. A [Dispatcher] calls it, possibly
// several times, with [Delivery.Attempt] raised on each retry.
type SendFunc func(ctx context.Context, d Delivery) error

// Dispatcher runs deliveries for notifications that implement [ShouldQueue].
// Dispatch should hand the delivery off and return quickly; the error it
// returns is about queueing, not about delivering.
//
// [NewQueue] returns an in-process dispatcher, which calls send on its own
// workers. send is a closure, so it cannot cross a process boundary: for a
// durable job queue, use [NewJobDispatcher], which serializes each delivery
// with a [Codec] for any queue library and is completed by
// [Notifier.HandleJob] in the worker.
type Dispatcher interface {
	Dispatch(ctx context.Context, d Delivery, send SendFunc) error
}

// DispatcherFunc adapts a function to the [Dispatcher] interface.
type DispatcherFunc func(ctx context.Context, d Delivery, send SendFunc) error

// Dispatch implements [Dispatcher].
func (f DispatcherFunc) Dispatch(ctx context.Context, d Delivery, send SendFunc) error {
	return f(ctx, d, send)
}

// Backoffer is an optional [Notification] interface that overrides a retrying
// dispatcher's delays between attempts. The last delay is reused once
// attempts outnumber the entries.
type Backoffer interface {
	Backoff() []time.Duration
}

// Delayer is an optional [Notification] interface. Queued deliveries wait
// Delay(channel) before their first attempt, so a channel can be held back —
// a reminder SMS an hour after the email, say.
type Delayer interface {
	Delay(channel string) time.Duration
}

// Queue is an in-process [Dispatcher]: a pool of worker goroutines with
// retries, backoff and delays. Create one with [NewQueue] and pass it to
// [WithQueue].
//
// Deliveries live in memory. They survive neither a crash nor a restart, so
// call [Queue.Close] on shutdown to drain them, and use a durable job queue
// where losing a notification is not acceptable.
type Queue struct {
	jobs    chan *queuedJob
	stop    chan struct{}
	base    context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
	pending sync.WaitGroup

	mu     sync.RWMutex
	closed bool

	tries   int
	backoff []time.Duration
	onError func(ctx context.Context, d Delivery, err error)
}

type queuedJob struct {
	ctx  context.Context
	d    Delivery
	send SendFunc
}

// QueueOption configures a [Queue].
type QueueOption func(*queueConfig)

type queueConfig struct {
	workers int
	buffer  int
	tries   int
	backoff []time.Duration
	onError func(ctx context.Context, d Delivery, err error)
}

// WithWorkers sets how many deliveries run concurrently. The default is 4.
func WithWorkers(n int) QueueOption {
	return func(c *queueConfig) { c.workers = n }
}

// WithBuffer sets how many deliveries may wait for a worker before Dispatch
// blocks. The default is 256.
func WithBuffer(n int) QueueOption {
	return func(c *queueConfig) { c.buffer = n }
}

// WithTries sets the maximum number of attempts per delivery, including the
// first. The default is 3. Notifications can override it with [Retryable].
// Errors for which [IsPermanent] reports true are never retried.
func WithTries(n int) QueueOption {
	return func(c *queueConfig) { c.tries = n }
}

// WithBackoff sets the delays between attempts. The last delay is reused once
// attempts outnumber the entries. The default is 1s, 5s, 30s.
func WithBackoff(delays ...time.Duration) QueueOption {
	return func(c *queueConfig) { c.backoff = delays }
}

// WithErrorHandler is called once for each delivery that failed for good:
// its last attempt failed, or its error was permanent. Use it to log or to
// store failed notifications. It runs on a worker goroutine.
func WithErrorHandler(fn func(ctx context.Context, d Delivery, err error)) QueueOption {
	return func(c *queueConfig) { c.onError = fn }
}

// NewQueue starts an in-process queue. Call [Queue.Close] to stop it.
func NewQueue(opts ...QueueOption) *Queue {
	cfg := queueConfig{
		workers: 4,
		buffer:  256,
		tries:   3,
		backoff: []time.Duration{time.Second, 5 * time.Second, 30 * time.Second},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.workers < 1 {
		cfg.workers = 1
	}
	if cfg.buffer < 0 {
		cfg.buffer = 0
	}
	if cfg.tries < 1 {
		cfg.tries = 1
	}

	base, cancel := context.WithCancel(context.Background())
	q := &Queue{
		jobs:    make(chan *queuedJob, cfg.buffer),
		stop:    make(chan struct{}),
		base:    base,
		cancel:  cancel,
		tries:   cfg.tries,
		backoff: cfg.backoff,
		onError: cfg.onError,
	}
	q.workers.Add(cfg.workers)
	for range cfg.workers {
		go q.work()
	}
	return q
}

// Dispatch queues one delivery. It blocks while the buffer is full, until ctx
// is done. The delivery runs with a context that keeps ctx's values but not
// its cancellation, so a request context ending does not abort it.
func (q *Queue) Dispatch(ctx context.Context, d Delivery, send SendFunc) error {
	q.mu.RLock()
	if q.closed {
		q.mu.RUnlock()
		return ErrQueueClosed
	}
	q.pending.Add(1)
	q.mu.RUnlock()

	j := &queuedJob{ctx: context.WithoutCancel(ctx), d: d, send: send}
	if dl, ok := d.Notification.(Delayer); ok {
		if delay := dl.Delay(d.Channel); delay > 0 {
			q.after(delay, j)
			return nil
		}
	}

	select {
	case q.jobs <- j:
		return nil
	case <-ctx.Done():
		q.pending.Done()
		return ctx.Err()
	case <-q.base.Done():
		q.pending.Done()
		return ErrQueueClosed
	}
}

// Close stops accepting deliveries and waits for queued ones, including
// pending retries and delays, to finish. If ctx ends first, in-flight
// deliveries are cancelled, waiting ones are dropped, and ctx's error is
// returned.
func (q *Queue) Close(ctx context.Context) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.closed = true
	q.mu.Unlock()

	drained := make(chan struct{})
	go func() {
		q.pending.Wait()
		close(drained)
	}()

	var err error
	select {
	case <-drained:
	case <-ctx.Done():
		err = ctx.Err()
		q.cancel()
		<-drained
	}
	q.cancel()
	close(q.stop)
	q.workers.Wait()
	return err
}

func (q *Queue) work() {
	defer q.workers.Done()
	for {
		select {
		case j := <-q.jobs:
			q.run(j)
		case <-q.stop:
			return
		}
	}
}

// run performs one attempt and either finishes the job or schedules a retry.
func (q *Queue) run(j *queuedJob) {
	if q.base.Err() != nil {
		q.pending.Done() // dropped by a Close that ran out of time
		return
	}

	ctx, cancel := context.WithCancel(j.ctx)
	stop := context.AfterFunc(q.base, cancel)
	err := q.attempt(ctx, j)
	stop()
	cancel()

	if err == nil {
		q.pending.Done()
		return
	}
	if IsPermanent(err) || j.d.Attempt >= q.triesFor(j.d) || q.base.Err() != nil {
		if q.onError != nil {
			q.onError(j.ctx, j.d, err)
		}
		q.pending.Done()
		return
	}

	delay := q.backoffFor(j.d)
	j.d.Attempt++
	q.after(delay, j)
}

// attempt calls send, turning a panic into a permanent error so one bad
// channel cannot take the process down.
func (q *Queue) attempt(ctx context.Context, j *queuedJob) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = Permanent(fmt.Errorf("notification: channel %q panicked: %v", j.d.Channel, r))
		}
	}()
	return j.send(ctx, j.d)
}

// after re-queues j once delay has passed. The job stays pending meanwhile, so
// Close waits for it.
func (q *Queue) after(delay time.Duration, j *queuedJob) {
	t := time.NewTimer(delay)
	go func() {
		select {
		case <-t.C:
		case <-q.base.Done():
			t.Stop()
			q.pending.Done()
			return
		}
		select {
		case q.jobs <- j:
		case <-q.base.Done():
			q.pending.Done()
		}
	}()
}

func (q *Queue) triesFor(d Delivery) int {
	if r, ok := d.Notification.(Retryable); ok && r.Tries() > 0 {
		return r.Tries()
	}
	return q.tries
}

func (q *Queue) backoffFor(d Delivery) time.Duration {
	delays := q.backoff
	if b, ok := d.Notification.(Backoffer); ok {
		delays = b.Backoff()
	}
	if len(delays) == 0 {
		return 0
	}
	i := d.Attempt - 1
	if i >= len(delays) {
		i = len(delays) - 1
	}
	return delays[i]
}
