// Package notification sends notifications to people and systems over any
// number of channels — mail, SMS, Slack, Discord, Telegram, Microsoft Teams,
// webhooks, a database inbox, or a channel you write yourself.
//
// The package is split in two layers:
//
//   - [Channel] is the one-method contract every delivery channel implements.
//     It is the only thing a new channel must satisfy.
//   - [Notifier] is what you call. It asks each notification which channels it
//     wants, skips the ones that should not run, sends the rest (inline or
//     through a [Dispatcher]), and reports what happened to a [Listener].
//
// Channels live in their own packages (mail, sms, slack, ...) and each defines
// the small interface a notification implements to support it — ToMail, ToSMS,
// ToSlack and so on. Importing this package never pulls in a third-party
// dependency.
//
// A notification is any type with a Via method:
//
//	type InvoicePaid struct{ Invoice Invoice }
//
//	func (InvoicePaid) Via(notification.Notifiable) []string {
//		return []string{"mail", "slack"}
//	}
//
//	func (n InvoicePaid) ToMail(notification.Notifiable) (*mail.Message, error) {
//		return mail.NewMessage().
//			Subject("Invoice paid").
//			Line(fmt.Sprintf("Invoice #%d has been paid.", n.Invoice.ID)).
//			Action("View invoice", n.Invoice.URL), nil
//	}
//
//	func (n InvoicePaid) ToSlack(notification.Notifiable) (*slack.Message, error) {
//		return slack.NewMessage(fmt.Sprintf("Invoice #%d paid", n.Invoice.ID)), nil
//	}
//
// and is sent with a [Notifier]:
//
//	notifier := notification.New(
//		notification.WithChannel("mail", mail.NewChannel(transport, mail.From("Acme", "billing@acme.test"))),
//		notification.WithChannel("slack", slack.NewChannel()),
//	)
//	err := notifier.Send(ctx, user, InvoicePaid{Invoice: inv})
//
// Application code should depend on the [Sender] interface rather than on
// *Notifier, so tests can swap in notificationtest.Fake.
package notification

import (
	"context"
	"errors"
)

// Notifiable is anything that can receive a notification: a user, a team, an
// on-call rotation. It tells each channel where to deliver.
//
// RouteNotificationFor returns the address for the named channel — an email
// address for "mail", a phone number for "sms", a webhook URL for "slack", a
// user id for "database". Returning "" means the notifiable has no address on
// that channel, and the channel is skipped rather than failed.
//
// The notification being sent is passed so a route can depend on it, for
// example sending billing mail to a different address.
type Notifiable interface {
	RouteNotificationFor(channel string, n Notification) string
}

// NotifiableFunc adapts a function to the [Notifiable] interface.
type NotifiableFunc func(channel string, n Notification) string

// RouteNotificationFor implements [Notifiable].
func (f NotifiableFunc) RouteNotificationFor(channel string, n Notification) string {
	return f(channel, n)
}

// Notification is a message that can be delivered over one or more channels.
//
// Via names the channels to use for a given recipient, by the names they were
// registered under with [WithChannel]. A notification supports a channel by
// implementing that channel's interface, such as mail.Notification.
type Notification interface {
	Via(to Notifiable) []string
}

// Channel delivers a notification over one medium. It is the contract every
// channel — built in or third party — implements.
//
// Implementations must be safe for concurrent use. Send should return
// [ErrNoRoute] when the recipient has no address on this channel, and an error
// wrapping [ErrUnsupported] when the notification does not implement the
// channel's interface.
type Channel interface {
	Send(ctx context.Context, d Delivery) error
}

// ChannelFunc adapts a function to the [Channel] interface, which is handy for
// one-off channels and tests.
type ChannelFunc func(ctx context.Context, d Delivery) error

// Send implements [Channel].
func (f ChannelFunc) Send(ctx context.Context, d Delivery) error { return f(ctx, d) }

// Delivery is one notification on its way to one recipient over one channel.
type Delivery struct {
	// ID identifies this notification for this recipient. Every channel
	// delivering the same Send call to the same recipient sees the same ID,
	// so it doubles as an idempotency key and as the database record id.
	ID string

	// Channel is the name the channel was registered under.
	Channel string

	// To is the recipient.
	To Notifiable

	// Notification is what is being sent.
	Notification Notification

	// Attempt counts delivery attempts, starting at 1. Only a retrying
	// [Dispatcher] raises it above 1.
	Attempt int
}

// Route returns the recipient's address on this delivery's channel.
func (d Delivery) Route() string {
	if d.To == nil {
		return ""
	}
	return d.To.RouteNotificationFor(d.Channel, d.Notification)
}

// Sender is the interface application code should depend on. *[Notifier]
// implements it, and so does notificationtest.Fake.
type Sender interface {
	// Send delivers n to the recipient over every channel n.Via names,
	// queueing it first when n implements [ShouldQueue] and a queue is set.
	Send(ctx context.Context, to Notifiable, n Notification) error

	// SendNow delivers n immediately, ignoring [ShouldQueue].
	SendNow(ctx context.Context, to Notifiable, n Notification) error
}

// ShouldSender is an optional [Notification] interface. ShouldSend is asked
// before each channel runs; returning false skips that channel. Use it for
// user preferences, quiet hours, or to drop a notification that is stale by
// the time a queue gets to it.
type ShouldSender interface {
	ShouldSend(to Notifiable, channel string) bool
}

// ShouldQueue is an optional [Notification] interface. Notifications that
// report true are handed to the [Notifier]'s queue, configured with
// [WithQueue], instead of being sent inline. Without a queue they are sent
// inline.
type ShouldQueue interface {
	ShouldQueue() bool
}

// Retryable is an optional [Notification] interface that overrides the retry
// policy of a retrying [Dispatcher] such as the one [NewQueue] returns.
type Retryable interface {
	// Tries is the maximum number of attempts per channel, including the
	// first. Zero or less falls back to the dispatcher's default.
	Tries() int
}

// Errors reported by channels and the [Notifier].
var (
	// ErrNoRoute means the recipient has no address on the channel. The
	// Notifier treats it as a skip, not a failure.
	ErrNoRoute = errors.New("notification: recipient has no route for channel")

	// ErrUnsupported means the notification does not implement the
	// interface a channel needs (for example ToMail for the mail channel).
	ErrUnsupported = errors.New("notification: notification not supported by channel")

	// ErrUnknownChannel means a notification named a channel that was never
	// registered.
	ErrUnknownChannel = errors.New("notification: unknown channel")

	// ErrQueueClosed is returned when sending through a queue that has been
	// closed.
	ErrQueueClosed = errors.New("notification: queue closed")
)
