# Security Policy

## Supported versions

The latest minor release receives security fixes.

## Reporting a vulnerability

Please report security issues privately through
[GitHub's security advisory form](https://github.com/zahansafallwa1511/go-notification/security/advisories/new)
rather than opening a public issue.

## Notes on safe use

- **Credentials belong in configuration, not in notifications.** API keys, bot
  tokens and SMTP passwords are set on channels and transports at startup.
  Channels never include them in error messages; keep it that way in your own
  channels, because errors end up in logs.
- **Routes are addresses you send to.** A `RouteNotificationFor` that returns a
  user-controlled value (a webhook URL from a settings page, say) lets that user
  make your servers send requests to any address. Validate such URLs — scheme,
  host, and that they do not resolve to internal networks — before storing them.
- **Sign outgoing webhooks.** Use `webhook.HMACSignature` and have receivers
  check it with `webhook.VerifySignature`, which compares in constant time and
  rejects stale timestamps.
- **Notification content is user data.** The log transports (`mail.NewLogTransport`,
  `sms.NewLogProvider`) write full message bodies; do not enable them in
  production if bodies may contain secrets such as password reset links.
- **`WithTable` in `sqlstore` is interpolated into statements**, because SQL
  does not allow a placeholder for an identifier. Pass a constant, never user
  input.
- **The in-process queue is not durable.** Deliveries waiting in a
  `notification.Queue` are lost if the process dies. Do not rely on it for
  notifications that must arrive, such as security alerts.
