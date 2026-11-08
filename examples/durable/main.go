// Example showing how to deliver notifications through a durable job queue —
// any queue library — with NewJobDispatcher, a Codec and HandleJob.
//
// The "queue" here is a slice of jobs, standing in for Redis, SQS, a database
// table, go-queue, asynq or River. Only bytes and retry settings cross it, so
// the worker could just as well be another process. Run it with:
//
//	go run ./examples/durable
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/mail"
)

// User is Identifiable, so queued deliveries store only "user"/id, and the
// worker reloads the user — seeing current data, and keeping personal data
// out of the queue.
type User struct {
	ID    int
	Email string
}

func (u User) RouteNotificationFor(channel string, _ notification.Notification) string {
	if channel == "mail" {
		return u.Email
	}
	return ""
}

func (u User) NotifiableKey() (kind, id string) { return "user", strconv.Itoa(u.ID) }

// users stands in for your database.
var users = map[string]User{"1": {ID: 1, Email: "ada@example.com"}}

// PasswordChanged is queued. Its exported fields are its payload.
type PasswordChanged struct {
	IP string
}

func (PasswordChanged) Via(notification.Notifiable) []string { return []string{"mail"} }
func (PasswordChanged) ShouldQueue() bool                    { return true }
func (PasswordChanged) Tries() int                           { return 3 }

func (n PasswordChanged) ToMail(notification.Notifiable) (*mail.Message, error) {
	return mail.NewMessage().
		Subject("Your password was changed").
		Line("Your password was changed from " + n.IP + ".").
		Line("If this wasn't you, reset it now."), nil
}

// newCodec is shared by producer and worker so both use the same names.
func newCodec() *notification.Codec {
	c := notification.NewCodec()
	notification.RegisterNotification[PasswordChanged](c, "password.changed")
	c.Resolve("user", func(_ context.Context, id string) (notification.Notifiable, error) {
		u, ok := users[id]
		if !ok {
			// Gone for good: drop the job instead of retrying.
			return nil, notification.Permanent(fmt.Errorf("user %s not found", id))
		}
		return u, nil
	})
	return c
}

func main() {
	ctx := context.Background()
	codec := newCodec()
	outbox := mail.NewArrayTransport()
	mailChannel := mail.NewChannel(outbox, mail.From("Acme", "security@acme.test"))

	// --- Producer (your web process) -------------------------------------
	var queue []notification.Job // stand-in for a real queue
	producer := notification.New(
		notification.WithChannel("mail", mailChannel),
		notification.WithQueue(notification.NewJobDispatcher(codec, func(_ context.Context, job notification.Job) error {
			// With a real library: push job.Payload, applying job.Delay,
			// job.Tries and job.Backoff as dispatch options.
			queue = append(queue, job)
			return nil
		})),
	)

	if err := producer.Send(ctx, users["1"], PasswordChanged{IP: "203.0.113.7"}); err != nil {
		fail(err)
	}
	fmt.Printf("queued %d job(s); payload: %s\n\n", len(queue), queue[0].Payload)

	// The user changes address before the worker runs. Because the
	// recipient travels by reference, the worker mails the new address.
	users["1"] = User{ID: 1, Email: "ada.lovelace@example.com"}

	// --- Worker (another process) ----------------------------------------
	worker := notification.New(notification.WithChannel("mail", mailChannel))

	outbox.FailWith(errors.New("smtp: 451 try again later (simulated)"))
	for _, job := range queue {
		for attempt := 1; ; attempt++ {
			err := worker.HandleJob(ctx, codec, job.Payload, attempt)
			if err == nil {
				fmt.Printf("attempt %d: delivered\n", attempt)
				break
			}
			if notification.IsPermanent(err) || attempt >= job.Tries {
				fmt.Printf("attempt %d: giving up: %v\n", attempt, err)
				break
			}
			fmt.Printf("attempt %d: %v; the queue will retry\n", attempt, err)
			outbox.FailWith(nil) // the server recovers
		}
	}

	if e, ok := outbox.Last(); ok {
		fmt.Printf("\nsent %q to %s\n", e.Subject, e.To[0].Email)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
