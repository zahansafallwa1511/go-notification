// Package smtp is a [mail.Transport] that sends through any SMTP server,
// built on github.com/wneessen/go-mail.
//
// It is a separate module so that the core go-notification module stays free
// of third-party dependencies:
//
//	go get github.com/zahansafallwa1511/go-notification/mail/smtp
//
// Usage:
//
//	t, err := smtp.New("smtp.example.com",
//		smtp.WithPort(587),
//		smtp.WithAuth(os.Getenv("SMTP_USER"), os.Getenv("SMTP_PASS")),
//	)
//	if err != nil {
//		return err
//	}
//	ch := mail.NewChannel(t, mail.From("Acme", "hello@acme.test"))
//
// For local development against Mailpit, Mailhog or Herd's mail server:
//
//	t, err := smtp.New("127.0.0.1", smtp.WithPort(1025), smtp.WithTLS(smtp.TLSNone))
//
// Each Send opens its own connection, so a Transport is safe for concurrent
// use. SMTP 5xx replies are reported as permanent errors (see
// notification.Permanent); 4xx replies and network failures are retryable.
package smtp

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"
	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/mail"
)

// TLSPolicy says how the connection is encrypted.
type TLSPolicy int

// TLS policies.
const (
	// TLSMandatory upgrades the connection with STARTTLS and fails if the
	// server does not support it. This is the default.
	TLSMandatory TLSPolicy = iota
	// TLSOpportunistic uses STARTTLS when the server offers it and plain
	// text otherwise.
	TLSOpportunistic
	// TLSNone never encrypts. Use it only for local development servers.
	TLSNone
	// TLSImplicit connects over TLS from the start ("SMTPS", usually port
	// 465).
	TLSImplicit
)

// AuthMechanism names a SASL authentication mechanism.
type AuthMechanism string

// Authentication mechanisms.
const (
	// AuthAuto picks the strongest mechanism the server offers. This is
	// the default when credentials are set.
	AuthAuto AuthMechanism = AuthMechanism(gomail.SMTPAuthAutoDiscover)
	// AuthPlain is the PLAIN mechanism.
	AuthPlain AuthMechanism = AuthMechanism(gomail.SMTPAuthPlain)
	// AuthLogin is the LOGIN mechanism.
	AuthLogin AuthMechanism = AuthMechanism(gomail.SMTPAuthLogin)
	// AuthCRAMMD5 is the CRAM-MD5 mechanism.
	AuthCRAMMD5 AuthMechanism = AuthMechanism(gomail.SMTPAuthCramMD5)
)

// Transport sends email over SMTP.
type Transport struct {
	host      string
	port      int
	username  string
	password  string
	mechanism AuthMechanism
	policy    TLSPolicy
	tlsConfig *tls.Config
	timeout   time.Duration
	helo      string
}

var _ mail.Transport = (*Transport)(nil)

// Option configures a [Transport].
type Option func(*Transport)

// WithPort sets the server port. The default is 587, or 465 with
// [TLSImplicit].
func WithPort(port int) Option {
	return func(t *Transport) { t.port = port }
}

// WithAuth sets the credentials. Without them no authentication is
// attempted.
func WithAuth(username, password string) Option {
	return func(t *Transport) { t.username, t.password = username, password }
}

// WithAuthMechanism forces a SASL mechanism instead of [AuthAuto].
func WithAuthMechanism(m AuthMechanism) Option {
	return func(t *Transport) { t.mechanism = m }
}

// WithTLS sets the [TLSPolicy]. The default is [TLSMandatory].
func WithTLS(p TLSPolicy) Option {
	return func(t *Transport) { t.policy = p }
}

// WithTLSConfig sets the TLS configuration, for custom roots or client
// certificates. ServerName defaults to the host.
func WithTLSConfig(c *tls.Config) Option {
	return func(t *Transport) { t.tlsConfig = c }
}

// WithTimeout bounds connecting and each SMTP command. The default is 15s.
func WithTimeout(d time.Duration) Option {
	return func(t *Transport) { t.timeout = d }
}

// WithHELO sets the name sent in EHLO/HELO. The default is the local
// hostname.
func WithHELO(name string) Option {
	return func(t *Transport) { t.helo = name }
}

// New returns a transport for the SMTP server at host. It validates the
// configuration but does not connect.
func New(host string, opts ...Option) (*Transport, error) {
	t := &Transport{host: host, timeout: 15 * time.Second, mechanism: AuthAuto}
	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}
	if t.port == 0 {
		t.port = 587
		if t.policy == TLSImplicit {
			t.port = 465
		}
	}
	if _, err := t.client(); err != nil {
		return nil, err
	}
	return t, nil
}

// client builds a go-mail client. A fresh one per send keeps the Transport
// safe for concurrent use.
func (t *Transport) client() (*gomail.Client, error) {
	opts := []gomail.Option{
		gomail.WithPort(t.port),
		gomail.WithTimeout(t.timeout),
	}
	switch t.policy {
	case TLSMandatory:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	case TLSOpportunistic:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSOpportunistic))
	case TLSNone:
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	case TLSImplicit:
		opts = append(opts, gomail.WithSSL())
	default:
		return nil, fmt.Errorf("smtp: unknown TLS policy %d", t.policy)
	}
	if t.tlsConfig != nil {
		cfg := t.tlsConfig.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName = t.host
		}
		opts = append(opts, gomail.WithTLSConfig(cfg))
	}
	if t.helo != "" {
		opts = append(opts, gomail.WithHELO(t.helo))
	}
	if t.username != "" || t.password != "" {
		opts = append(opts,
			gomail.WithSMTPAuth(gomail.SMTPAuthType(t.mechanism)),
			gomail.WithUsername(t.username),
			gomail.WithPassword(t.password),
		)
	}
	c, err := gomail.NewClient(t.host, opts...)
	if err != nil {
		return nil, fmt.Errorf("smtp: %w", err)
	}
	return c, nil
}

// Send implements [mail.Transport].
func (t *Transport) Send(ctx context.Context, e *mail.Email) error {
	msg, err := buildMsg(e)
	if err != nil {
		return notification.Permanent(err)
	}
	c, err := t.client()
	if err != nil {
		return notification.Permanent(err)
	}
	if err := c.DialAndSendWithContext(ctx, msg); err != nil {
		var se *gomail.SendError
		// Only a 5xx reply from the server is a permanent rejection; network
		// failures carry no code and are worth retrying.
		if errors.As(err, &se) && se.ErrorCode() >= 500 {
			return notification.Permanent(fmt.Errorf("smtp: %w", err))
		}
		return fmt.Errorf("smtp: %w", err)
	}
	return nil
}

func buildMsg(e *mail.Email) (*gomail.Msg, error) {
	m := gomail.NewMsg()
	if err := m.FromFormat(e.From.Name, e.From.Email); err != nil {
		return nil, fmt.Errorf("smtp: from: %w", err)
	}
	for _, a := range e.To {
		if err := m.AddToFormat(a.Name, a.Email); err != nil {
			return nil, fmt.Errorf("smtp: to: %w", err)
		}
	}
	for _, a := range e.Cc {
		if err := m.AddCcFormat(a.Name, a.Email); err != nil {
			return nil, fmt.Errorf("smtp: cc: %w", err)
		}
	}
	for _, a := range e.Bcc {
		if err := m.AddBccFormat(a.Name, a.Email); err != nil {
			return nil, fmt.Errorf("smtp: bcc: %w", err)
		}
	}
	if len(e.ReplyTo) > 0 {
		list := make([]string, len(e.ReplyTo))
		for i, a := range e.ReplyTo {
			list[i] = a.String()
		}
		if err := m.SetAddrHeader(gomail.HeaderReplyTo, list...); err != nil {
			return nil, fmt.Errorf("smtp: reply-to: %w", err)
		}
	}
	m.Subject(e.Subject)
	m.SetMessageID()
	m.SetDate()
	for k, v := range e.Headers {
		m.SetGenHeader(gomail.Header(k), v)
	}

	switch {
	case e.Text != "" && e.HTML != "":
		m.SetBodyString(gomail.TypeTextPlain, e.Text)
		m.AddAlternativeString(gomail.TypeTextHTML, e.HTML)
	case e.HTML != "":
		m.SetBodyString(gomail.TypeTextHTML, e.HTML)
	default:
		m.SetBodyString(gomail.TypeTextPlain, e.Text)
	}

	for _, a := range e.Attachments {
		ct := gomail.WithFileContentType(gomail.ContentType(a.MIMEType()))
		if a.Inline {
			if err := m.EmbedReader(a.Filename, bytes.NewReader(a.Data), ct, gomail.WithFileContentID("<"+strings.Trim(a.CID(), "<>")+">")); err != nil {
				return nil, fmt.Errorf("smtp: embedding %s: %w", a.Filename, err)
			}
			continue
		}
		if err := m.AttachReader(a.Filename, bytes.NewReader(a.Data), ct); err != nil {
			return nil, fmt.Errorf("smtp: attaching %s: %w", a.Filename, err)
		}
	}
	return m, nil
}
