# go-notification

[![CI](https://github.com/zahansafallwa1511/go-notification/actions/workflows/ci.yml/badge.svg)](https://github.com/zahansafallwa1511/go-notification/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/zahansafallwa1511/go-notification.svg)](https://pkg.go.dev/github.com/zahansafallwa1511/go-notification)
[![Go Report Card](https://goreportcard.com/badge/github.com/zahansafallwa1511/go-notification)](https://goreportcard.com/report/github.com/zahansafallwa1511/go-notification)
[![Go 1.25+](https://img.shields.io/badge/go-1.25%2B-00ADD8)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Notifications for Go in the spirit of Laravel's: write one notification type,
deliver it by mail, SMS, Slack, Discord, Telegram, Microsoft Teams, a webhook
or an in-app inbox. The API follows standard-library habits: `context` comes
first, errors are returned, there are no globals, and the core module has
**no third-party dependencies**.

```go
err := notifier.Send(ctx, user, InvoicePaid{Invoice: inv})
```

## Install

```sh
go get github.com/zahansafallwa1511/go-notification
```

## Design

The library has two layers, so that using it and extending it are separate jobs:

| | |
|---|---|
| `notification.Channel` | The one-method contract for delivery channels: `Send(ctx, Delivery) error`. Implement it and any medium works everywhere. |
| `notification.Notifier` | What you call. It asks each notification which channels it wants, skips recipients with no route, runs middleware, queues or sends, and reports to listeners. |

Each channel lives in its own package and defines the small interface a
notification implements to support it. A notification is any type with a `Via`
method plus one `To…` method per channel it supports:

| Package | Notification implements | Route (what the recipient returns) |
|---|---|---|
| `mail` | `ToMail(Notifiable) (*mail.Message, error)` | `ada@example.com` or `Ada <ada@example.com>` |
| `sms` | `ToSMS(Notifiable) (*sms.Message, error)` | E.164 phone number, `+15550100` |
| `slack` | `ToSlack(Notifiable) (*slack.Message, error)` | Incoming-webhook URL, or a channel id/name with a bot token |
| `discord` | `ToDiscord(Notifiable) (*discord.Message, error)` | Channel webhook URL |
| `telegram` | `ToTelegram(Notifiable) (*telegram.Message, error)` | Chat id, or `@channelname` |
| `teams` | `ToTeams(Notifiable) (*teams.Message, error)` | Teams Workflows webhook URL |
| `webhook` | `ToWebhook(Notifiable) (*webhook.Message, error)` | Any URL |
| `database` | `ToDatabase(Notifiable) (any, error)` | The recipient's id |

Providers that need a third-party SDK live in **their own modules**, so
importing `go-notification` never pulls in `go-mail` or the AWS SDK:

| Adapter | Module | Depends on |
|---|---|---|
| `mail` log, array, `Failover`, `RoundRobin` | core | — |
| `mail/resend` | core | — (standard library HTTP) |
| `mail/smtp` | `go-notification/mail/smtp` | `github.com/wneessen/go-mail` |
| `mail/ses` | `go-notification/mail/ses` | AWS SDK for Go v2 (`sesv2`) |
| `sms` log, array | core | — |
| `sms/twilio`, `sms/vonage` | core | — (standard library HTTP) |
| `database` `MemoryStore` | core | — |
| `database/sqlstore` | core | — (`database/sql`; bring your own driver) |

## Usage

### Notifications and recipients

A recipient is any `Notifiable`: it returns its address for each channel.
Returning `""` means "no address here", and that channel is **skipped, not
failed**.

```go
type User struct {
    ID    int64
    Email string
    Phone string
}

func (u User) RouteNotificationFor(channel string, _ notification.Notification) string {
    switch channel {
    case "mail":
        return u.Email
    case "sms":
        return u.Phone
    case "database":
        return strconv.FormatInt(u.ID, 10)
    }
    return ""
}
```

A notification names its channels and builds the content for each:

```go
type InvoicePaid struct{ Invoice Invoice }

func (InvoicePaid) Via(notification.Notifiable) []string {
    return []string{"mail", "sms", "database"}
}

func (n InvoicePaid) ToMail(notification.Notifiable) (*mail.Message, error) {
    return mail.NewMessage().
        Subject("Invoice paid").
        Line(fmt.Sprintf("Invoice #%d has been paid.", n.Invoice.ID)).
        Action("View invoice", n.Invoice.URL).
        Line("Thank you for your business!"), nil
}

func (n InvoicePaid) ToSMS(notification.Notifiable) (*sms.Message, error) {
    return sms.NewMessage(fmt.Sprintf("Invoice #%d paid.", n.Invoice.ID)), nil
}

func (n InvoicePaid) ToDatabase(notification.Notifiable) (any, error) {
    return map[string]any{"invoice_id": n.Invoice.ID}, nil
}
```

Register channels by name and send:

```go
notifier := notification.New(
    notification.WithChannel("mail", mail.NewChannel(transport, mail.From("Acme", "billing@acme.test"))),
    notification.WithChannel("sms", sms.NewChannel(twilio.New(sid, token), sms.WithFrom("+15550100"))),
    notification.WithChannel("database", database.NewChannel(store)),
)
defer notifier.Close(ctx)

err := notifier.Send(ctx, user, InvoicePaid{Invoice: inv})
err = notification.SendAll(ctx, notifier, admins, InvoicePaid{Invoice: inv})
```

Channels are independent: one failing does not stop the others. `Send` joins
one `*notification.ChannelError` per failed channel:

```go
var ce *notification.ChannelError
if errors.As(err, &ce) {
    log.Printf("%s failed: %v", ce.Channel, ce.Err)
}
```

To notify someone who isn't one of your types, use on-demand `Routes`:

```go
to := notification.Route("mail", "ops@acme.test").Route("slack", webhookURL)
err := notifier.Send(ctx, to, DeployFinished{})
```

Application code should depend on the `notification.Sender` interface rather
than `*Notifier`, so that tests can pass in a fake (see [Testing your code](#testing-your-code)).

### Optional behaviour

A notification opts into extra behaviour by implementing small interfaces:

| Interface | Effect |
|---|---|
| `ShouldSend(to, channel) bool` | Asked before each channel. Use it for user preferences, quiet hours, or to drop stale notifications. |
| `ShouldQueue() bool` | Hand deliveries to the queue instead of sending inline. |
| `Tries() int` | Override the queue's attempt count. |
| `Backoff() []time.Duration` | Override the queue's delays between attempts. |
| `Delay(channel) time.Duration` | Hold a queued channel back, for example an SMS reminder an hour after the email. |

### Queueing

```go
queue := notification.NewQueue(
    notification.WithWorkers(4),
    notification.WithTries(3),
    notification.WithBackoff(time.Second, 5*time.Second, 30*time.Second),
    notification.WithErrorHandler(func(ctx context.Context, d notification.Delivery, err error) {
        slog.ErrorContext(ctx, "notification failed for good", "channel", d.Channel, "err", err)
    }),
)
notifier := notification.New(notification.WithQueue(queue), /* channels */)
defer notifier.Close(ctx) // drains queued deliveries and pending retries
```

Each channel delivery is retried on its own. Errors wrapped with
`notification.Permanent` are not retried, and neither are `ErrNoRoute`,
`ErrUnsupported`, or HTTP 4xx responses other than 408 and 429. Every delivery
carries a stable `Delivery.ID`, which channels send as an idempotency key, so a
retried request is safe to repeat.

`NewQueue` is in-process: deliveries survive neither a crash nor a restart. For
those, use a durable queue.

### Durable queues (any queue library)

The library doesn't depend on any queue. `NewJobDispatcher` turns each
channel delivery into a `notification.Job`: a JSON payload plus the
notification's delay, tries and backoff. Your one function hands that job to
whichever queue you run, whether Redis, SQS, a database table, go-queue, asynq
or River. The worker gives the payload back to `HandleJob`:

```go
// Shared by producer and worker, so both use the same names.
func newCodec(users *UserRepo) *notification.Codec {
    c := notification.NewCodec()
    notification.RegisterNotification[InvoicePaid](c, "invoice.paid")
    notification.RegisterNotification[PasswordChanged](c, "password.changed")
    c.Resolve("user", func(ctx context.Context, id string) (notification.Notifiable, error) {
        return users.Find(ctx, id) // wrap "not found" with notification.Permanent
    })
    return c
}

// Producer
notifier := notification.New(
    notification.WithQueue(notification.NewJobDispatcher(codec, func(ctx context.Context, job notification.Job) error {
        return myqueue.Push(ctx, "notifications", job.Payload, job.Delay)
    })),
    // channels...
)

// Worker
err := notifier.HandleJob(ctx, codec, payload, attempt)
if notification.IsPermanent(err) {
    // fail the job; retrying cannot help
}
```

- **One job per channel.** Each channel retries on its own, and every job of
  one `Send` shares the same `Delivery.ID`, so channel-level idempotency keys
  hold across retries. `job.ID + ":" + job.Channel` makes a good unique-job key.
- **Recipients.** A recipient that implements
  `Identifiable` (`NotifiableKey() (kind, id string)`) and has a resolver is
  stored by reference and reloaded by the worker. The worker then sees current
  data, and the queue holds no personal data. Otherwise, register the
  recipient type with `RegisterNotifiable` to store it as JSON. `Routes` is
  already registered.
- **Notifications** are stored as JSON under their registered name, so only
  exported fields travel. Keep the names stable when you rename Go types.
- **Errors.** Payloads that can't be decoded, and unknown types, are permanent.
  Resolver errors are returned unchanged, so a database outage is retried.

`HandleJob` is `codec.Decode` followed by `notifier.Deliver`, which runs exactly
one channel for a rebuilt `Delivery`. Use those two directly if your queue needs
something different.

With [go-queue](https://github.com/zahansafallwa1511/go-queue), the bridge is one
job type:

```go
type SendNotification struct {
    queue.Queueable
    Payload json.RawMessage `json:"payload"`
}

func (j *SendNotification) Handle(ctx context.Context) error {
    err := app.Notifier.HandleJob(ctx, app.Codec, j.Payload, j.Attempts())
    if notification.IsPermanent(err) {
        return queue.FailNow(err)
    }
    return err
}

dispatcher := notification.NewJobDispatcher(codec, func(ctx context.Context, job notification.Job) error {
    _, err := mgr.Dispatch(ctx, &SendNotification{Payload: job.Payload},
        queue.WithDelay(job.Delay), queue.WithTries(job.Tries), queue.WithBackoff(job.Backoff...))
    return err
})
```

Zero `Delay`, `Tries` and `Backoff` mean "use the queue's default", which is
also how go-queue reads them.

### Middleware and listeners

```go
notifier := notification.New(
    notification.WithMiddleware(func(next notification.Channel) notification.Channel {
        return notification.ChannelFunc(func(ctx context.Context, d notification.Delivery) error {
            start := time.Now()
            err := next.Send(ctx, d)
            metrics.Observe(d.Channel, time.Since(start), err)
            return err
        })
    }),
    notification.WithListener(notification.ListenerFuncs{
        Failed: func(ctx context.Context, d notification.Delivery, err error) {
            slog.ErrorContext(ctx, "notification failed", "channel", d.Channel, "err", err)
        },
    }),
)
```

Listeners see `OnSending`, `OnSent`, `OnFailed` and `OnSkipped`. A skip's reason
is `ErrNoRoute` or `ErrSkipped`.

## Mail

```go
ch := mail.NewChannel(transport,
    mail.From("Acme", "hello@acme.test"),
    mail.WithAppName("Acme"),
    mail.WithLogoURL("https://acme.test/logo.png"),
    mail.WithPrimaryColor("#4f46e5"),
)
```

The builder renders a responsive HTML email from a built-in template, plus a
plain-text alternative:

```go
mail.NewMessage().
    Subject("Payment failed").
    Greeting("Hi Ada,").
    Line("We couldn't charge your card.").   // above the button
    Action("Update card", "https://acme.test/billing").
    Line("We'll try again in three days.").  // below the button
    Failure().                               // red button, "Whoops!" greeting
    Attach("invoice.pdf", pdf).
    Tag("billing")
```

Lines are always HTML-escaped. Action URLs must be absolute `http(s)` or
`mailto` URLs. To use your own markup, call `View` (an `html/template`) or
`HTML` on the message, or replace the layout for every message with
`mail.WithLayout`. Preview or snapshot-test a message without sending it:

```go
e, err := mail.Render(msg, mail.From("Acme", "hello@acme.test"))
os.WriteFile("preview.html", []byte(e.HTML), 0o644)
```

In staging, `mail.AlwaysTo(mail.Addr("QA", "qa@acme.test"))` sends every email
to one inbox and keeps the real recipient in `X-Original-To`.

### Transports

```go
// SMTP: separate module
t, err := smtp.New("smtp.example.com", smtp.WithPort(587), smtp.WithAuth(user, pass))
t, err := smtp.New("127.0.0.1", smtp.WithPort(1025), smtp.WithTLS(smtp.TLSNone)) // Mailpit, Herd

// Amazon SES: separate module
cfg, _ := config.LoadDefaultConfig(ctx, config.WithRegion("eu-west-2"))
t := ses.New(sesv2.NewFromConfig(cfg), ses.WithConfigurationSet("transactional"))

// Resend
t := resend.New(os.Getenv("RESEND_API_KEY"))

// Development and tests
t := mail.NewLogTransport(slog.Default())
t := mail.NewArrayTransport()

// Combinators
t := mail.Failover(sesTransport, smtpTransport) // try in order
t := mail.RoundRobin(primary, secondary)         // spread the load, fail over
```

## SMS

```go
ch := sms.NewChannel(twilio.New(accountSID, authToken), sms.WithFrom("+15550100"))
ch := sms.NewChannel(twilio.New(accountSID, authToken, twilio.WithMessagingServiceSID(sid)))
ch := sms.NewChannel(vonage.New(apiKey, apiSecret), sms.WithFrom("ACME"))
```

```go
func (n OrderShipped) ToSMS(notification.Notifiable) (*sms.Message, error) {
    return sms.NewMessage("Your order #" + n.ID + " has shipped."), nil
}
```

`sms.NewLogProvider` and `sms.NewArrayProvider` are there for development and
tests. Vonage answers 200 even when it rejects a message, so the provider reads
the per-message status and reports rejections as errors.

## Slack

```go
slack.NewChannel(slack.WithToken(os.Getenv("SLACK_BOT_TOKEN")))
```

If the route is an `http(s)` URL, the message is posted to it as an incoming
webhook. Otherwise the route is treated as a channel id or name (`C0123…`,
`#alerts`) and posted with `chat.postMessage`, which needs the bot token.

```go
func (n InvoicePaid) ToSlack(notification.Notifiable) (*slack.Message, error) {
    return slack.NewMessage("Invoice paid"). // fallback text
        Header("Invoice paid").
        Section(fmt.Sprintf("*#%d* was paid by %s", n.ID, n.Customer)).
        Fields("*Amount*\n£120.00", "*Method*\nCard").
        Actions(slack.Button("View invoice", n.URL).Primary()), nil
}
```

For block types the builder doesn't cover, pass `slack.RawBlock`.

## Discord

```go
func (n DeployFinished) ToDiscord(notification.Notifiable) (*discord.Message, error) {
    return discord.NewMessage("Deploy finished").Embed(discord.Embed{
        Title: "v1.4.2 is live",
        Color: discord.ColorGreen,
        URL:   n.ReleaseURL,
    }.Field("Duration", n.Duration.String(), true)).NoMentions(), nil
}
```

The route is the channel's webhook URL. `discord.WithWait(true)` makes Discord
confirm the message was created, so that validation errors come back as errors.

## Telegram

```go
telegram.NewChannel(os.Getenv("TELEGRAM_BOT_TOKEN"))

func (n OrderShipped) ToTelegram(notification.Notifiable) (*telegram.Message, error) {
    return telegram.NewMessage("<b>Order shipped</b>\nTracking: " + n.Tracking).
        HTML().
        Row(telegram.Button("Track parcel", n.TrackingURL)), nil
}
```

The route is a chat id or `@channelname`. The bot token is part of every request
URL, so the channel keeps it out of the errors it returns.

## Microsoft Teams

Office 365 connectors are retired. Instead, add the *"Post to a channel when a
webhook request is received"* workflow in Teams and use its URL as the route.

```go
func (n IncidentOpened) ToTeams(notification.Notifiable) (*teams.Message, error) {
    return teams.NewMessage("Incident opened").
        Style(teams.ColorAttention).
        Content(n.Summary).
        Fact("Severity", n.Severity).
        Action("Open incident", n.URL), nil
}
```

Messages are sent as Adaptive Cards 1.4. For anything the builder doesn't cover,
design a card at [adaptivecards.io/designer](https://adaptivecards.io/designer)
and send it with `teams.RawCard(card)`.

## Webhooks

```go
webhook.NewChannel(webhook.WithAuth(webhook.HMACSignature(os.Getenv("WEBHOOK_SECRET"))))

func (n OrderShipped) ToWebhook(notification.Notifiable) (*webhook.Message, error) {
    return webhook.NewMessage(map[string]any{"event": "order.shipped", "order_id": n.OrderID}), nil
}
```

The available authenticators are `BearerToken`, `BasicAuth`, `APIKey`,
`APIKeyQuery`, `HMACSignature` and `Chain`, or you can implement
`webhook.Authenticator`. Each request carries the delivery id in
`X-Notification-ID` and `Idempotency-Key`. By default any 2xx response counts as
delivered; change that with `WithSuccessStatus`. Receivers verify signed
requests in one call:

```go
body, err := webhook.VerifyRequest(r, secret, 5*time.Minute) // rejects replays
if err != nil {
    http.Error(w, "bad signature", http.StatusUnauthorized)
    return
}
```

Webhook URLs are often secrets, so transport errors from every HTTP channel
redact them.

## Database (in-app inbox)

```go
store := sqlstore.New(db, sqlstore.Postgres) // or sqlstore.MySQL, sqlstore.SQLite
if err := store.CreateTable(ctx); err != nil {
    return err
}
notifier := notification.New(notification.WithChannel("database", database.NewChannel(store)))
```

Read one recipient's notifications in your handlers. Every `Inbox` operation is
scoped to that recipient:

```go
inbox := database.InboxFor(store, user)
unread, err := inbox.Unread(ctx, 20)
count, err := inbox.UnreadCount(ctx)
_, err = inbox.MarkAsRead(ctx, unread[0].ID)
_, err = inbox.MarkAllAsRead(ctx)

var data struct{ InvoiceID int `json:"invoice_id"` }
err = unread[0].Decode(&data)
```

Record ids are delivery ids, and stores ignore a duplicate insert, so a retried
delivery never shows up twice. Implement `Typer` (`DatabaseType() string`) to
keep stored type names stable when you rename a Go type. Prune old records with
a scheduled job:

```go
store.Delete(ctx, database.Query{Before: time.Now().AddDate(0, -3, 0), State: database.Read})
```

`database.NewMemoryStore()` is there for tests and prototypes. SQL timestamps
are stored as BIGINT Unix nanoseconds, because that round-trips exactly on every
driver.

## Testing your code

Depend on `notification.Sender`, then pass a fake in tests:

```go
fake := notificationtest.NewFake()
svc := billing.NewService(fake)

svc.MarkPaid(ctx, invoice)

notificationtest.AssertSentTo[billing.InvoicePaid](t, fake, customer)
notificationtest.AssertSentOn[billing.InvoicePaid](t, fake, "mail")
notificationtest.AssertNotSent[billing.Refunded](t, fake)
```

To exercise a real `Notifier` without delivering anything, register
`notificationtest.NewRecorder()` as a channel, or use `mail.NewArrayTransport()`
and `sms.NewArrayProvider()`.

## Writing a channel

Implement `notification.Channel`, and define the interface notifications
implement to support it:

```go
type Notification interface {
    ToPagerDuty(to notification.Notifiable) (*Event, error)
}

func (c *Channel) Send(ctx context.Context, d notification.Delivery) error {
    route := d.Route()
    if route == "" {
        return notification.ErrNoRoute
    }
    n, ok := d.Notification.(Notification)
    if !ok {
        return fmt.Errorf("%w: pagerduty needs ToPagerDuty", notification.ErrUnsupported)
    }
    // build and send, using d.ID as the idempotency key; wrap failures that
    // cannot succeed on retry with notification.Permanent
}
```

Return `*notification.HTTPError` for non-2xx responses, and the queue will retry
only the temporary ones.

## Writing an adapter

A new mail provider implements `mail.Transport`, and a new SMS gateway
implements `sms.Provider`. Both are one-method interfaces. A new inbox backend
implements `database.Store` and proves it with the shared conformance suite,
which the built-in stores also run:

```go
func TestStore(t *testing.T) {
    storetest.Run(t, func(t *testing.T) database.Store { return mystore.New() })
}
```

If the adapter needs a third-party SDK, give it its own module, as `mail/smtp`
does, so the core stays dependency-free. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Example

Neither example needs any services:

- [`examples/basic`](examples/basic/main.go) sends over mail, SMS and the
  inbox, with preferences, an on-demand recipient, a listener, and a queued
  retry.
- [`examples/durable`](examples/durable/main.go) passes a delivery through a
  stand-in job queue as bytes. A separate worker notifier picks it up, reloads
  the recipient by reference, and retries once.

```sh
go run ./examples/basic
go run ./examples/durable
```

## Testing

```sh
go test -race ./...
```

Everything runs without servers. The separate modules are tested from their own
directories:

```sh
cd mail/smtp && go test ./...
cd mail/ses  && go test ./...
cd sqltest   && go test ./...   # SQLite needs no server
```

SQL integration tests against Postgres and MySQL are skipped unless you set
their DSNs:

```sh
docker run -d --rm -p 5432:5432 -e POSTGRES_PASSWORD=notify postgres:16-alpine
cd sqltest && POSTGRES_DSN='postgres://postgres:notify@localhost:5432/postgres?sslmode=disable' go test ./...
```

On every push, CI runs the tests on Linux, macOS and Windows and runs the store
conformance suite against real Postgres, MySQL and SQLite.

## License

MIT.
