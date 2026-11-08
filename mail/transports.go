package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	notification "github.com/zahansafallwa1511/go-notification"
)

// LogTransport writes emails to a logger instead of sending them. Use it in
// development.
type LogTransport struct {
	logger   *slog.Logger
	withBody bool
}

// NewLogTransport returns a transport that logs each email at Info level. A
// nil logger uses slog.Default.
func NewLogTransport(logger *slog.Logger) *LogTransport {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogTransport{logger: logger, withBody: true}
}

// WithoutBody stops the transport logging the text body.
func (t *LogTransport) WithoutBody() *LogTransport {
	c := *t
	c.withBody = false
	return &c
}

// Send implements [Transport].
func (t *LogTransport) Send(ctx context.Context, e *Email) error {
	to := make([]string, 0, len(e.To))
	for _, a := range e.AllRecipients() {
		to = append(to, a.String())
	}
	attrs := []any{
		slog.String("from", e.From.String()),
		slog.Any("to", to),
		slog.String("subject", e.Subject),
		slog.Int("attachments", len(e.Attachments)),
	}
	if id := e.Headers[HeaderNotificationID]; id != "" {
		attrs = append(attrs, slog.String("notification_id", id))
	}
	if t.withBody {
		attrs = append(attrs, slog.String("text", e.Text))
	}
	t.logger.InfoContext(ctx, "mail", attrs...)
	return nil
}

// ArrayTransport keeps emails in memory instead of sending them. Use it in
// tests. It is safe for concurrent use.
type ArrayTransport struct {
	mu     sync.Mutex
	emails []Email
	err    error
}

// NewArrayTransport returns an empty ArrayTransport.
func NewArrayTransport() *ArrayTransport { return &ArrayTransport{} }

// Send implements [Transport]. It records a copy of e.
func (t *ArrayTransport) Send(_ context.Context, e *Email) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	t.emails = append(t.emails, *e)
	return nil
}

// FailWith makes later sends fail with err without recording. Pass nil to
// succeed again.
func (t *ArrayTransport) FailWith(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.err = err
}

// Emails returns the recorded emails, in order.
func (t *ArrayTransport) Emails() []Email {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Email(nil), t.emails...)
}

// Last returns the most recent email, and false when none was sent.
func (t *ArrayTransport) Last() (Email, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.emails) == 0 {
		return Email{}, false
	}
	return t.emails[len(t.emails)-1], true
}

// Reset forgets every recorded email.
func (t *ArrayTransport) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emails = nil
}

// Failover returns a transport that tries each transport in order until one
// succeeds — a backup provider for when the primary is down.
//
// When every transport fails, the error joins all of their errors. It counts
// as permanent (see notification.IsPermanent) only if every failure was
// permanent, so a retrying queue tries again while any transport might still
// recover.
func Failover(transports ...Transport) Transport {
	return &failover{transports: transports}
}

type failover struct {
	transports []Transport
}

func (f *failover) Send(ctx context.Context, e *Email) error {
	return sendInOrder(ctx, e, f.transports, 0)
}

// RoundRobin returns a transport that spreads emails across transports,
// starting each send at the next transport in turn and failing over to the
// others like [Failover].
func RoundRobin(transports ...Transport) Transport {
	return &roundRobin{transports: transports}
}

type roundRobin struct {
	transports []Transport
	next       atomic.Uint64
}

func (r *roundRobin) Send(ctx context.Context, e *Email) error {
	if len(r.transports) == 0 {
		return sendInOrder(ctx, e, nil, 0)
	}
	start := int((r.next.Add(1) - 1) % uint64(len(r.transports)))
	return sendInOrder(ctx, e, r.transports, start)
}

func sendInOrder(ctx context.Context, e *Email, ts []Transport, start int) error {
	if len(ts) == 0 {
		return notification.Permanent(errors.New("mail: no transports configured"))
	}
	var temporary, permanent []error
	for i := range ts {
		if err := ctx.Err(); err != nil {
			return err
		}
		t := ts[(start+i)%len(ts)]
		err := t.Send(ctx, e)
		if err == nil {
			return nil
		}
		if notification.IsPermanent(err) {
			permanent = append(permanent, err)
		} else {
			temporary = append(temporary, err)
		}
	}
	if len(temporary) == 0 {
		return notification.Permanent(fmt.Errorf("mail: all %d transports failed: %w", len(ts), errors.Join(permanent...)))
	}
	// Report only the temporary failures through the error chain, so the
	// permanent ones do not make the whole failover look permanent.
	if len(permanent) == 0 {
		return fmt.Errorf("mail: all %d transports failed: %w", len(ts), errors.Join(temporary...))
	}
	return fmt.Errorf("mail: all %d transports failed (%d permanently: %v): %w",
		len(ts), len(permanent), errors.Join(permanent...), errors.Join(temporary...))
}
