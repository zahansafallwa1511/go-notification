package notification

import (
	"context"
	"errors"
)

// ErrSkipped is the reason passed to [Listener.OnSkipped] when a
// notification's [ShouldSender.ShouldSend] returned false.
var ErrSkipped = errors.New("notification: skipped by ShouldSend")

// Listener observes deliveries. Register one with [WithListener] to log,
// meter or trace what the [Notifier] does.
//
// Callbacks run on the goroutine performing the delivery, which is a queue
// worker for queued notifications. They must not block.
type Listener interface {
	// OnSending runs just before a channel is called.
	OnSending(ctx context.Context, d Delivery)
	// OnSent runs after a channel delivered successfully.
	OnSent(ctx context.Context, d Delivery)
	// OnFailed runs after a channel attempt failed. A retrying dispatcher
	// may call the channel again afterwards.
	OnFailed(ctx context.Context, d Delivery, err error)
	// OnSkipped runs when a channel is not attempted, because the recipient
	// has no route ([ErrNoRoute]) or ShouldSend returned false
	// ([ErrSkipped]).
	OnSkipped(ctx context.Context, d Delivery, reason error)
}

// ListenerFuncs adapts a set of functions to the [Listener] interface, so a
// listener can be written without declaring a type. Nil fields are ignored.
//
//	notification.WithListener(notification.ListenerFuncs{
//		Failed: func(ctx context.Context, d notification.Delivery, err error) {
//			slog.ErrorContext(ctx, "notification failed", "channel", d.Channel, "err", err)
//		},
//	})
type ListenerFuncs struct {
	Sending func(ctx context.Context, d Delivery)
	Sent    func(ctx context.Context, d Delivery)
	Failed  func(ctx context.Context, d Delivery, err error)
	Skipped func(ctx context.Context, d Delivery, reason error)
}

// OnSending implements [Listener].
func (l ListenerFuncs) OnSending(ctx context.Context, d Delivery) {
	if l.Sending != nil {
		l.Sending(ctx, d)
	}
}

// OnSent implements [Listener].
func (l ListenerFuncs) OnSent(ctx context.Context, d Delivery) {
	if l.Sent != nil {
		l.Sent(ctx, d)
	}
}

// OnFailed implements [Listener].
func (l ListenerFuncs) OnFailed(ctx context.Context, d Delivery, err error) {
	if l.Failed != nil {
		l.Failed(ctx, d, err)
	}
}

// OnSkipped implements [Listener].
func (l ListenerFuncs) OnSkipped(ctx context.Context, d Delivery, reason error) {
	if l.Skipped != nil {
		l.Skipped(ctx, d, reason)
	}
}

// listeners fans one event out to several listeners.
type listeners []Listener

func (ls listeners) OnSending(ctx context.Context, d Delivery) {
	for _, l := range ls {
		l.OnSending(ctx, d)
	}
}

func (ls listeners) OnSent(ctx context.Context, d Delivery) {
	for _, l := range ls {
		l.OnSent(ctx, d)
	}
}

func (ls listeners) OnFailed(ctx context.Context, d Delivery, err error) {
	for _, l := range ls {
		l.OnFailed(ctx, d, err)
	}
}

func (ls listeners) OnSkipped(ctx context.Context, d Delivery, reason error) {
	for _, l := range ls {
		l.OnSkipped(ctx, d, reason)
	}
}
