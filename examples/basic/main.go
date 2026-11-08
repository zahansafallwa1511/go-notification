// Example showing basic usage of go-notification: a notification sent over
// mail, SMS and an in-app inbox, user preferences with ShouldSend, an
// on-demand recipient, a listener, and queued delivery with retries.
//
// It needs no services or credentials: mail goes to the log, SMS to memory and
// the inbox to a MemoryStore. Run it with:
//
//	go run ./examples/basic
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/database"
	"github.com/zahansafallwa1511/go-notification/mail"
	"github.com/zahansafallwa1511/go-notification/sms"
)

// User is a notifiable: it tells each channel where to deliver.
type User struct {
	ID       int64
	Name     string
	Email    string
	Phone    string
	SMSOptIn bool
}

// RouteNotificationFor returns the user's address on each channel. An empty
// string means "no address here", and the channel is skipped, not failed.
func (u User) RouteNotificationFor(channel string, _ notification.Notification) string {
	switch channel {
	case "mail":
		return fmt.Sprintf("%s <%s>", u.Name, u.Email)
	case "sms":
		return u.Phone
	case database.ChannelName:
		return strconv.FormatInt(u.ID, 10)
	}
	return ""
}

// NotifiableType names the recipient type stored with inbox records.
func (User) NotifiableType() string { return "user" }

// InvoicePaid is a notification. Via picks the channels; ToMail, ToSMS and
// ToDatabase build the content for each.
type InvoicePaid struct {
	InvoiceID int
	Amount    string
}

func (InvoicePaid) Via(notification.Notifiable) []string {
	return []string{"mail", "sms", database.ChannelName}
}

func (n InvoicePaid) ToMail(to notification.Notifiable) (*mail.Message, error) {
	return mail.NewMessage().
		Subject(fmt.Sprintf("Invoice #%d paid", n.InvoiceID)).
		Line(fmt.Sprintf("We received your payment of %s.", n.Amount)).
		Action("View invoice", fmt.Sprintf("https://acme.test/invoices/%d", n.InvoiceID)).
		Line("Thank you for your business!").
		Success(), nil
}

func (n InvoicePaid) ToSMS(notification.Notifiable) (*sms.Message, error) {
	return sms.NewMessage(fmt.Sprintf("Acme: invoice #%d paid (%s).", n.InvoiceID, n.Amount)), nil
}

func (n InvoicePaid) ToDatabase(notification.Notifiable) (any, error) {
	return map[string]any{"invoice_id": n.InvoiceID, "amount": n.Amount}, nil
}

// DatabaseType keeps the stored type stable if the Go type is renamed.
func (InvoicePaid) DatabaseType() string { return "invoice.paid" }

// ShouldSend respects the user's SMS preference.
func (InvoicePaid) ShouldSend(to notification.Notifiable, channel string) bool {
	if u, ok := to.(User); ok && channel == "sms" {
		return u.SMSOptIn
	}
	return true
}

// WeeklyDigest is queued: Send hands it to the queue and returns at once.
type WeeklyDigest struct{ Items int }

func (WeeklyDigest) Via(notification.Notifiable) []string { return []string{"mail"} }
func (WeeklyDigest) ShouldQueue() bool                    { return true }
func (WeeklyDigest) Tries() int                           { return 3 }

func (d WeeklyDigest) ToMail(notification.Notifiable) (*mail.Message, error) {
	return mail.NewMessage().
		Subject("Your weekly digest").
		Line(fmt.Sprintf("%d things happened this week.", d.Items)), nil
}

// DeployFinished goes to an on-demand recipient — an address that is not one
// of your own types.
type DeployFinished struct{ Version string }

func (DeployFinished) Via(notification.Notifiable) []string { return []string{"mail"} }

func (d DeployFinished) ToMail(notification.Notifiable) (*mail.Message, error) {
	return mail.NewMessage().
		Subject("Deployed " + d.Version).
		Line(d.Version + " is live."), nil
}

func main() {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Mail goes to the log. To fail the first attempt of every email and show
	// the queue retrying, wrap the transport.
	logMail := mail.NewLogTransport(logger).WithoutBody()
	var attempts atomic.Int32
	flaky := mail.TransportFunc(func(ctx context.Context, e *mail.Email) error {
		if e.Subject == "Your weekly digest" {
			if attempts.Add(1) == 1 {
				return errors.New("smtp: connection reset (simulated)")
			}
		}
		return logMail.Send(ctx, e)
	})

	smsProvider := sms.NewArrayProvider()
	inboxStore := database.NewMemoryStore()

	queue := notification.NewQueue(
		notification.WithWorkers(2),
		notification.WithBackoff(100*time.Millisecond),
		notification.WithErrorHandler(func(ctx context.Context, d notification.Delivery, err error) {
			logger.Error("gave up", "channel", d.Channel, "id", d.ID, "err", err)
		}),
	)

	notifier := notification.New(
		notification.WithChannel("mail", mail.NewChannel(flaky,
			mail.From("Acme", "hello@acme.test"),
			mail.WithAppName("Acme"),
		)),
		notification.WithChannel("sms", sms.NewChannel(smsProvider, sms.WithFrom("ACME"))),
		notification.WithChannel(database.ChannelName, database.NewChannel(inboxStore)),
		notification.WithQueue(queue),
		notification.WithListener(notification.ListenerFuncs{
			Sent: func(ctx context.Context, d notification.Delivery) {
				fmt.Printf("  sent     %-8s attempt %d\n", d.Channel, d.Attempt)
			},
			Failed: func(ctx context.Context, d notification.Delivery, err error) {
				fmt.Printf("  failed   %-8s attempt %d: %v\n", d.Channel, d.Attempt, err)
			},
			Skipped: func(ctx context.Context, d notification.Delivery, reason error) {
				fmt.Printf("  skipped  %-8s %v\n", d.Channel, reason)
			},
		}),
	)

	ada := User{ID: 1, Name: "Ada Lovelace", Email: "ada@example.com", Phone: "+15550100", SMSOptIn: true}
	alan := User{ID: 2, Name: "Alan Turing", Email: "alan@example.com"} // no phone, no SMS opt-in

	fmt.Println("== InvoicePaid to Ada (mail, sms, database)")
	must(notifier.Send(ctx, ada, InvoicePaid{InvoiceID: 1042, Amount: "£120.00"}))

	fmt.Println("== InvoicePaid to Alan (sms skipped by preference)")
	must(notifier.Send(ctx, alan, InvoicePaid{InvoiceID: 1043, Amount: "£80.00"}))

	fmt.Println("== DeployFinished to an on-demand address")
	must(notifier.Send(ctx, notification.Route("mail", "ops@acme.test"), DeployFinished{Version: "v1.4.2"}))

	fmt.Println("== WeeklyDigest to Ada (queued; first attempt fails, then retries)")
	must(notifier.Send(ctx, ada, WeeklyDigest{Items: 7}))

	// Close drains the queue, including pending retries.
	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	must(notifier.Close(closeCtx))

	fmt.Println("== SMS captured by the array provider")
	for _, m := range smsProvider.Messages() {
		fmt.Printf("  to %s from %s: %q\n", m.To, m.From, m.Body)
	}

	fmt.Println("== Ada's inbox")
	inbox := database.InboxFor(inboxStore, ada)
	unread, err := inbox.Unread(ctx, 10)
	must(err)
	for _, r := range unread {
		var data struct {
			InvoiceID int    `json:"invoice_id"`
			Amount    string `json:"amount"`
		}
		must(r.Decode(&data))
		fmt.Printf("  %s  invoice #%d  %s\n", r.Type, data.InvoiceID, data.Amount)
	}
	_, err = inbox.MarkAllAsRead(ctx)
	must(err)
	count, err := inbox.UnreadCount(ctx)
	must(err)
	fmt.Printf("  unread after MarkAllAsRead: %d\n", count)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
