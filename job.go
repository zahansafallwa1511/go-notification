package notification

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Job is one delivery prepared for a durable job queue. [NewJobDispatcher]
// builds one per channel delivery and hands it to your [EnqueueFunc]; a
// worker later passes Payload to [Notifier.HandleJob].
//
// Tries, Backoff and Delay carry the notification's own [Retryable],
// [Backoffer] and [Delayer] settings. Their zero values mean "use the queue's
// default", so they map directly onto most queue libraries' dispatch options.
type Job struct {
	// Payload is the serialized delivery. Store it as the job body.
	Payload []byte

	// ID and Channel identify the delivery. ID+":"+Channel is unique per
	// delivery, which makes it a good key for queues that deduplicate jobs.
	ID      string
	Channel string

	// Delay is how long the job should wait before its first attempt.
	Delay time.Duration

	// Tries is the maximum number of attempts; zero means the queue's
	// default.
	Tries int

	// Backoff lists the delays between attempts; nil means the queue's
	// default.
	Backoff []time.Duration
}

// EnqueueFunc stores a [Job] in a job queue. It is the only code needed to
// connect a queue library: push job.Payload as the job body, applying Delay,
// Tries and Backoff where the library supports them. It should return once
// the job is stored durably.
type EnqueueFunc func(ctx context.Context, job Job) error

// NewJobDispatcher returns a [Dispatcher] for durable job queues, built on any
// queue library. Each delivery is serialized with codec and handed to
// enqueue; a worker hands the payload back to [Notifier.HandleJob]:
//
//	// Producer
//	notifier := notification.New(
//		notification.WithQueue(notification.NewJobDispatcher(codec, func(ctx context.Context, job notification.Job) error {
//			return myqueue.Push(ctx, "notifications", job.Payload, job.Delay)
//		})),
//		// channels...
//	)
//
//	// Worker
//	err := notifier.HandleJob(ctx, codec, payload, attempt)
//
// Retries are the queue's job, not this dispatcher's: it serializes and
// enqueues once. A delivery that cannot be serialized — its notification or
// recipient type is not registered with codec — fails at Send time with the
// reason.
func NewJobDispatcher(codec *Codec, enqueue EnqueueFunc) Dispatcher {
	return DispatcherFunc(func(ctx context.Context, d Delivery, _ SendFunc) error {
		payload, err := codec.Encode(d)
		if err != nil {
			return err
		}
		job := Job{Payload: payload, ID: d.ID, Channel: d.Channel}
		if dl, ok := d.Notification.(Delayer); ok {
			job.Delay = max(dl.Delay(d.Channel), 0)
		}
		if r, ok := d.Notification.(Retryable); ok {
			job.Tries = max(r.Tries(), 0)
		}
		if b, ok := d.Notification.(Backoffer); ok {
			job.Backoff = b.Backoff()
		}
		return enqueue(ctx, job)
	})
}

// HandleJob is the worker side of [NewJobDispatcher]: it decodes payload with
// codec and makes one delivery attempt with [Notifier.Deliver]. attempt is the
// queue's attempt number, starting at 1; zero or less counts as 1.
//
// The returned error tells the queue what to do. nil means done, including
// when the delivery was skipped. A payload that cannot be decoded, and any
// error for which [IsPermanent] reports true, will never succeed, so the job
// should be failed rather than retried:
//
//	if err := notifier.HandleJob(ctx, codec, payload, attempt); err != nil {
//		if notification.IsPermanent(err) {
//			return myqueue.FailNow(err)
//		}
//		return err // retried with the queue's backoff
//	}
//
// Errors from a recipient resolver (see [Codec.Resolve]) are returned as
// they are, so a database outage is retried.
func (nt *Notifier) HandleJob(ctx context.Context, codec *Codec, payload []byte, attempt int) error {
	d, err := codec.Decode(ctx, payload)
	if err != nil {
		return err
	}
	d.Attempt = attempt
	return nt.Deliver(ctx, d)
}

// Deliver makes one delivery attempt: it runs the channel d.Channel for
// d.To and d.Notification, keeping d.ID and d.Attempt, with [ShouldSender],
// middleware and listeners applied as for [Notifier.Send]. Unlike Send it
// ignores Via and never queues.
//
// It is the building block for workers of durable queues, which rebuild a
// delivery in another process; most code calls Send instead. It returns the
// channel's error itself rather than a [ChannelError], so the caller can
// decide on a retry with [IsPermanent]. A skipped delivery — no route, or
// ShouldSend returned false — returns nil.
func (nt *Notifier) Deliver(ctx context.Context, d Delivery) error {
	switch {
	case d.To == nil:
		return Permanent(errors.New("notification: nil recipient"))
	case d.Notification == nil:
		return Permanent(errors.New("notification: nil notification"))
	}
	ch, ok := nt.Channel(d.Channel)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownChannel, d.Channel)
	}
	if d.Attempt < 1 {
		d.Attempt = 1
	}
	return nt.deliver(ctx, ch, d)
}
