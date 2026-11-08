package notification

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// Middleware wraps a [Channel] to add behaviour around every delivery — rate
// limiting, logging, tracing, rewriting recipients in staging. The first
// middleware registered is the outermost.
type Middleware func(next Channel) Channel

// Notifier sends notifications over registered channels. It is safe for
// concurrent use. Keep one on your application struct and pass it — or better,
// the [Sender] interface it implements — where it is needed.
type Notifier struct {
	mu         sync.RWMutex
	channels   map[string]Channel
	middleware []Middleware
	listeners  listeners
	queue      Dispatcher
	newID      func() string
}

// Option configures a [Notifier].
type Option func(*Notifier)

// WithChannel registers a channel under name. Notifications select channels by
// these names in their Via method. Registering an existing name replaces it.
func WithChannel(name string, ch Channel) Option {
	return func(n *Notifier) { n.channels[name] = ch }
}

// WithMiddleware wraps every channel with mw. Middleware runs for inline and
// queued deliveries alike, on each attempt.
func WithMiddleware(mw ...Middleware) Option {
	return func(n *Notifier) { n.middleware = append(n.middleware, mw...) }
}

// WithListener registers a [Listener]. It may be given more than once.
func WithListener(l Listener) Option {
	return func(n *Notifier) { n.listeners = append(n.listeners, l) }
}

// WithQueue sets the [Dispatcher] used for notifications that implement
// [ShouldQueue]. [NewQueue] returns an in-process one; adapt any job queue by
// implementing Dispatcher.
func WithQueue(d Dispatcher) Option {
	return func(n *Notifier) { n.queue = d }
}

// WithIDGenerator replaces the function that generates [Delivery.ID]. The
// default produces random version 4 UUIDs.
func WithIDGenerator(fn func() string) Option {
	return func(n *Notifier) { n.newID = fn }
}

// New returns a Notifier. Register channels with [WithChannel] here or later
// with [Notifier.Extend].
func New(opts ...Option) *Notifier {
	n := &Notifier{
		channels: map[string]Channel{},
		newID:    NewID,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(n)
		}
	}
	return n
}

// Extend registers ch under name after construction, replacing any channel
// already registered under that name.
func (nt *Notifier) Extend(name string, ch Channel) {
	nt.mu.Lock()
	defer nt.mu.Unlock()
	nt.channels[name] = ch
}

// Channel returns the channel registered under name, unwrapped by middleware.
func (nt *Notifier) Channel(name string) (Channel, bool) {
	nt.mu.RLock()
	defer nt.mu.RUnlock()
	ch, ok := nt.channels[name]
	return ch, ok
}

// Channels lists the registered channel names, sorted.
func (nt *Notifier) Channels() []string {
	nt.mu.RLock()
	defer nt.mu.RUnlock()
	return slices.Sorted(maps.Keys(nt.channels))
}

// Send delivers n to the recipient over every channel n.Via names. When n
// implements [ShouldQueue] and a queue is configured, each channel delivery is
// handed to the queue and Send returns once they are queued.
//
// Channels are independent: one failing does not stop the others. The
// returned error joins a [*ChannelError] for every channel that failed or
// could not be queued. Recipients without a route on a channel are skipped,
// not failed.
func (nt *Notifier) Send(ctx context.Context, to Notifiable, n Notification) error {
	return nt.send(ctx, to, n, true)
}

// SendNow delivers n immediately on the calling goroutine, even if it
// implements [ShouldQueue].
func (nt *Notifier) SendNow(ctx context.Context, to Notifiable, n Notification) error {
	return nt.send(ctx, to, n, false)
}

// Close closes the queue, if one is configured and it has a
// Close(context.Context) error method, waiting for queued deliveries to
// finish until ctx is done.
func (nt *Notifier) Close(ctx context.Context) error {
	if c, ok := nt.queue.(interface{ Close(context.Context) error }); ok {
		return c.Close(ctx)
	}
	return nil
}

// SendAll delivers n to every recipient through s. Each recipient gets its own
// [Delivery.ID]. It returns the errors of all recipients joined.
//
//	err := notification.SendAll(ctx, notifier, admins, IncidentOpened{ID: id})
func SendAll[T Notifiable](ctx context.Context, s Sender, recipients []T, n Notification) error {
	var errs []error
	for _, to := range recipients {
		if err := s.Send(ctx, to, n); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (nt *Notifier) send(ctx context.Context, to Notifiable, n Notification, allowQueue bool) error {
	if to == nil {
		return errors.New("notification: nil recipient")
	}
	if n == nil {
		return errors.New("notification: nil notification")
	}

	queued := false
	if allowQueue && nt.queue != nil {
		if q, ok := n.(ShouldQueue); ok && q.ShouldQueue() {
			queued = true
		}
	}

	id := nt.newID()
	var errs []error
	seen := map[string]bool{}
	for _, name := range n.Via(to) {
		if seen[name] {
			continue
		}
		seen[name] = true

		d := Delivery{ID: id, Channel: name, To: to, Notification: n, Attempt: 1}
		ch, ok := nt.Channel(name)
		if !ok {
			errs = append(errs, &ChannelError{Channel: name, ID: id, Err: ErrUnknownChannel})
			continue
		}

		var err error
		if queued {
			err = nt.queue.Dispatch(ctx, d, func(ctx context.Context, d Delivery) error {
				return nt.deliver(ctx, ch, d)
			})
		} else {
			err = nt.deliver(ctx, ch, d)
		}
		if err != nil {
			errs = append(errs, &ChannelError{Channel: name, ID: id, Err: err})
		}
	}
	return errors.Join(errs...)
}

// deliver runs one channel for one delivery, applying ShouldSend, middleware
// and listeners. An ErrNoRoute from the channel is reported as a skip.
func (nt *Notifier) deliver(ctx context.Context, ch Channel, d Delivery) error {
	if ss, ok := d.Notification.(ShouldSender); ok && !ss.ShouldSend(d.To, d.Channel) {
		nt.listeners.OnSkipped(ctx, d, ErrSkipped)
		return nil
	}

	nt.mu.RLock()
	mw := nt.middleware
	nt.mu.RUnlock()
	for i := len(mw) - 1; i >= 0; i-- {
		ch = mw[i](ch)
	}

	nt.listeners.OnSending(ctx, d)
	err := ch.Send(ctx, d)
	switch {
	case err == nil:
		nt.listeners.OnSent(ctx, d)
		return nil
	case errors.Is(err, ErrNoRoute):
		nt.listeners.OnSkipped(ctx, d, err)
		return nil
	default:
		nt.listeners.OnFailed(ctx, d, err)
		return err
	}
}

// NewID returns a random version 4 UUID. It is the default [Delivery.ID]
// generator.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Routes is an on-demand recipient: a set of addresses that is not one of
// your own types. Use it to notify someone who is not a user of your system.
//
//	to := notification.Route("mail", "ops@acme.test").Route("slack", webhookURL)
//	err := notifier.Send(ctx, to, DeployFinished{})
type Routes map[string]string

// Route returns a [Routes] with one address.
func Route(channel, address string) Routes {
	return Routes{channel: address}
}

// Route adds an address for channel and returns r.
func (r Routes) Route(channel, address string) Routes {
	r[channel] = address
	return r
}

// RouteNotificationFor implements [Notifiable].
func (r Routes) RouteNotificationFor(channel string, _ Notification) string {
	return r[channel]
}
