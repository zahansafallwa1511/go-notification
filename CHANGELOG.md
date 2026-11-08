# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Core: `Notifier`, the `Channel` and `Sender` contracts, on-demand `Routes`,
  middleware, listeners, `ShouldSend`, and an in-process `Queue` with retries,
  backoff, delays and graceful shutdown.
- Channels: mail, SMS, Slack, Discord, Telegram, Microsoft Teams, webhook and
  database.
- Mail transports: SMTP (`mail/smtp` module), Resend, Amazon SES (`mail/ses`
  module), log, array, `Failover` and `RoundRobin`.
- SMS providers: Twilio, Vonage, log and array.
- Database stores: memory and SQL (Postgres, MySQL, SQLite), with a conformance
  suite in `database/storetest`.
- `notificationtest`: a fake `Sender` with assertions, and a recording channel.
- Durable queues through any queue library: `NewJobDispatcher` serializes
  each channel delivery into a `Job`, `Notifier.HandleJob` delivers it in the
  worker, and `Codec` registers notification and recipient types. `Identifiable`
  recipients travel by reference and are reloaded through a resolver.
- `Notifier.Deliver` runs exactly one channel for a rebuilt `Delivery`, keeping
  its ID and attempt.
- `examples/basic` and `examples/durable`: runnable demos that need no
  services.
