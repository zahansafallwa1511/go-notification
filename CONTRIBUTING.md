# Contributing

Thanks for considering a contribution.

## Getting set up

The repository holds four modules: the dependency-free core (with every
channel that needs nothing beyond the standard library), plus three satellite
modules that need third-party code:

| Module | Why it is separate |
|---|---|
| `mail/smtp` | depends on `github.com/wneessen/go-mail` |
| `mail/ses` | depends on the AWS SDK |
| `sqltest` | runs the SQL store against real database drivers |

A workspace ties them together for local development:

```sh
go work init . ./mail/smtp ./mail/ses ./sqltest
go build ./...
```

`go.work` is deliberately not committed — it would break consumers.

## Running the tests

```sh
go test -race ./...                       # core and built-in channels, no network
(cd mail/smtp && go test -race ./...)     # in-process fake SMTP server
(cd mail/ses && go test -race ./...)      # fake SES client
(cd sqltest && go test -race ./...)       # SQLite runs with no server
```

The SQL store tests against Postgres and MySQL are skipped unless you point
them at a server:

```sh
docker run -d --rm -p 55432:5432 -e POSTGRES_PASSWORD=notify postgres:16-alpine
docker run -d --rm -p 33307:3306 -e MYSQL_ROOT_PASSWORD=notify -e MYSQL_DATABASE=notify mysql:8

cd sqltest && \
  POSTGRES_DSN='postgres://postgres:notify@localhost:55432/postgres?sslmode=disable' \
  MYSQL_DSN='root:notify@tcp(localhost:33307)/notify?parseTime=true' \
  go test ./...
```

CI runs all of them on every push.

## The one rule

**The core module must keep an empty `require` block.** It is why adapters
that need a third-party library live in their own modules. CI fails the build
if a dependency appears in the root `go.mod`.

## Adding a channel

A channel is anything that implements `notification.Channel`:

```go
type Channel interface {
    Send(ctx context.Context, d notification.Delivery) error
}
```

Follow the shape of the built-in channels:

1. Define the interface a notification implements to support your channel,
   e.g. `ToPagerDuty(to notification.Notifiable) (*Message, error)`.
2. Read the address with `d.Route()`. Return `notification.ErrNoRoute` when it
   is empty, and an error wrapping `notification.ErrUnsupported` when the
   notification does not implement your interface.
3. Wrap errors that cannot succeed on retry with `notification.Permanent`.
   HTTP channels get this for free from `internal/httpx`.
4. Accept `WithHTTPClient` and `WithBaseURL` options so the channel can be
   tested against `httptest.Server`.

## Adding a mail transport, SMS provider or database store

These are the adapter contracts inside a channel:

| Contract | Implemented by |
|---|---|
| `mail.Transport` | SMTP, Resend, SES, log, array, failover |
| `sms.Provider` | Twilio, Vonage, log, array |
| `database.Store` | memory, `sqlstore` |

A database store proves itself against the shared conformance suite:

```go
func TestStore(t *testing.T) {
    storetest.Run(t, func(t *testing.T) database.Store { return mystore.New() })
}
```

## Before opening a pull request

```sh
gofmt -l .        # must print nothing
go vet ./...
go test -race ./...
```

Document every exported identifier — the reference page is the library's user
interface. Explain *why* in comments where the reason is not obvious from the
code; the code already says what it does.
