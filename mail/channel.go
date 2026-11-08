// Package mail is the email channel for go-notification.
//
// A notification supports email by implementing [Notification]:
//
//	func (n InvoicePaid) ToMail(to notification.Notifiable) (*mail.Message, error) {
//		return mail.NewMessage().
//			Subject("Invoice paid").
//			Line(fmt.Sprintf("Invoice #%d has been paid.", n.ID)).
//			Action("View invoice", n.URL).
//			Line("Thank you for your business!"), nil
//	}
//
// The [Channel] renders the [Message] into an [Email] — HTML through a
// built-in responsive template, plus a plain-text alternative — and hands it
// to a [Transport]:
//
//	ch := mail.NewChannel(transport,
//		mail.From("Acme", "hello@acme.test"),
//		mail.WithAppName("Acme"),
//	)
//	notifier := notification.New(notification.WithChannel("mail", ch))
//
// Transport is the adapter contract for mail providers. This package ships
// dependency-free ones — [NewLogTransport], [NewArrayTransport], [Failover]
// and [RoundRobin] — and the subpackages add real providers: mail/resend
// (HTTP API), and the separate modules mail/smtp and mail/ses. Implement
// Transport to send through anything else.
//
// The recipient's address comes from RouteNotificationFor("mail", n), either
// a bare address or "Name <address>".
package mail

import (
	"context"
	"fmt"

	notification "github.com/zahansafallwa1511/go-notification"
)

// Notification is implemented by notifications that can be sent as email.
type Notification interface {
	ToMail(to notification.Notifiable) (*Message, error)
}

// Channel is the email [notification.Channel].
type Channel struct {
	transport Transport
	cfg       *config
}

var _ notification.Channel = (*Channel)(nil)

// NewChannel returns a mail channel that sends through t.
func NewChannel(t Transport, opts ...Option) *Channel {
	return &Channel{transport: t, cfg: newConfig(opts)}
}

// Send implements [notification.Channel].
func (c *Channel) Send(ctx context.Context, d notification.Delivery) error {
	e, err := c.Build(d)
	if err != nil {
		return err
	}
	return c.transport.Send(ctx, e)
}

// Build renders the email for d without sending it. Send uses it; it is
// exported for previews and tests.
func (c *Channel) Build(d notification.Delivery) (*Email, error) {
	route := d.Route()
	if route == "" {
		return nil, notification.ErrNoRoute
	}
	to, err := ParseAddress(route)
	if err != nil {
		return nil, notification.Permanent(err)
	}

	mn, ok := d.Notification.(Notification)
	if !ok {
		return nil, fmt.Errorf("%w: %T does not implement mail.Notification", notification.ErrUnsupported, d.Notification)
	}
	msg, err := mn.ToMail(d.To)
	if err != nil {
		return nil, fmt.Errorf("mail: building message: %w", err)
	}
	if msg == nil {
		return nil, notification.Permanent(fmt.Errorf("mail: %T.ToMail returned a nil message", d.Notification))
	}

	e, err := render(msg, c.cfg)
	if err != nil {
		return nil, notification.Permanent(err)
	}
	e.To = []Address{to}
	if e.Headers == nil {
		e.Headers = map[string]string{}
	}
	if d.ID != "" {
		e.Headers[HeaderNotificationID] = d.ID
	}
	if len(c.cfg.alwaysTo) > 0 {
		e.Headers["X-Original-To"] = to.String()
		e.To = append([]Address(nil), c.cfg.alwaysTo...)
		e.Cc, e.Bcc = nil, nil
	}

	if err := e.Validate(); err != nil {
		return nil, notification.Permanent(err)
	}
	return e, nil
}
