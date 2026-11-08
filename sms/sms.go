// Package sms delivers notifications as text messages.
//
// The channel is provider-agnostic: it builds an [SMS] and hands it to a
// [Provider]. Providers for Twilio and Vonage live in the sms/twilio and
// sms/vonage packages; any other gateway is supported by implementing the
// one-method Provider interface.
//
//	provider := twilio.New(accountSID, authToken)
//	notifier := notification.New(
//		notification.WithChannel("sms", sms.NewChannel(provider, sms.WithFrom("+15550100"))),
//	)
//
// A notification supports the channel by implementing [Notification]:
//
//	func (n OrderShipped) ToSMS(notification.Notifiable) (*sms.Message, error) {
//		return sms.NewMessage("Your order #" + n.ID + " has shipped."), nil
//	}
//
// The recipient's route for "sms" is the phone number, in E.164 form
// (+15550100).
package sms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	notification "github.com/zahansafallwa1511/go-notification"
)

// Notification is implemented by notifications that can be sent as SMS.
type Notification interface {
	ToSMS(to notification.Notifiable) (*Message, error)
}

// Message is the content of a text message, built by a notification.
type Message struct {
	// Content is the text body.
	Content string
	// From overrides the channel's default sender.
	From string
	// Unicode asks providers that distinguish encodings to send the message
	// as Unicode (UCS-2) rather than GSM-7.
	Unicode bool
}

// NewMessage returns a message with the given content.
func NewMessage(content string) *Message {
	return &Message{Content: content}
}

// Text sets the content and returns m.
func (m *Message) Text(content string) *Message {
	m.Content = content
	return m
}

// Sender sets the sender, overriding the channel default, and returns m.
func (m *Message) Sender(from string) *Message {
	m.From = from
	return m
}

// AsUnicode marks the message as Unicode and returns m.
func (m *Message) AsUnicode() *Message {
	m.Unicode = true
	return m
}

// SMS is one fully addressed text message, as handed to a [Provider].
type SMS struct {
	// To is the recipient phone number.
	To string
	// From is the sender: a phone number, short code or alphanumeric id.
	// It may be empty when the provider supplies its own sender.
	From string
	// Body is the text.
	Body string
	// Unicode mirrors [Message.Unicode].
	Unicode bool
	// ID is the notification's delivery id, usable as an idempotency key.
	ID string
}

// Provider sends a text message through a gateway. It is the adapter contract
// for SMS providers; implementations must be safe for concurrent use.
type Provider interface {
	Send(ctx context.Context, msg SMS) error
}

// ProviderFunc adapts a function to the [Provider] interface.
type ProviderFunc func(ctx context.Context, msg SMS) error

// Send implements [Provider].
func (f ProviderFunc) Send(ctx context.Context, msg SMS) error { return f(ctx, msg) }

// Channel is the SMS [notification.Channel].
type Channel struct {
	provider Provider
	from     string
}

var _ notification.Channel = (*Channel)(nil)

// Option configures a [Channel].
type Option func(*Channel)

// WithFrom sets the default sender used when a message does not set one.
func WithFrom(from string) Option {
	return func(c *Channel) { c.from = from }
}

// NewChannel returns an SMS channel that sends through p.
func NewChannel(p Provider, opts ...Option) *Channel {
	c := &Channel{provider: p}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// Send implements [notification.Channel].
func (c *Channel) Send(ctx context.Context, d notification.Delivery) error {
	to := strings.TrimSpace(d.Route())
	if to == "" {
		return notification.ErrNoRoute
	}
	n, ok := d.Notification.(Notification)
	if !ok {
		return fmt.Errorf("%w: %T does not implement sms.Notification", notification.ErrUnsupported, d.Notification)
	}
	msg, err := n.ToSMS(d.To)
	if err != nil {
		return err
	}
	if msg == nil || strings.TrimSpace(msg.Content) == "" {
		return notification.Permanent(errors.New("sms: message has no content"))
	}
	if c.provider == nil {
		return notification.Permanent(errors.New("sms: channel has no provider"))
	}
	from := msg.From
	if from == "" {
		from = c.from
	}
	return c.provider.Send(ctx, SMS{To: to, From: from, Body: msg.Content, Unicode: msg.Unicode, ID: d.ID})
}

// LogProvider writes messages to a logger instead of sending them. Use it in
// development.
type LogProvider struct {
	logger *slog.Logger
}

// NewLogProvider returns a provider that logs each message at info level. A
// nil logger uses slog.Default().
func NewLogProvider(logger *slog.Logger) *LogProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogProvider{logger: logger}
}

// Send implements [Provider].
func (p *LogProvider) Send(ctx context.Context, msg SMS) error {
	p.logger.InfoContext(ctx, "sms", "to", msg.To, "from", msg.From, "body", msg.Body, "id", msg.ID)
	return nil
}

// ArrayProvider keeps messages in memory instead of sending them. Use it in
// tests. It is safe for concurrent use.
type ArrayProvider struct {
	mu   sync.Mutex
	sent []SMS
}

// NewArrayProvider returns an empty ArrayProvider.
func NewArrayProvider() *ArrayProvider { return &ArrayProvider{} }

// Send implements [Provider].
func (p *ArrayProvider) Send(_ context.Context, msg SMS) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, msg)
	return nil
}

// Messages returns the messages sent so far, in order.
func (p *ArrayProvider) Messages() []SMS {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]SMS(nil), p.sent...)
}

// Reset forgets every message.
func (p *ArrayProvider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = nil
}
