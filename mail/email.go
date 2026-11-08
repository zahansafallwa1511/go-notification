package mail

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/mail"
	"path/filepath"
	"strings"
)

// HeaderNotificationID is the header the [Channel] sets to the
// notification's [notification.Delivery.ID]. Transports whose provider
// supports idempotency keys use it as one.
const HeaderNotificationID = "X-Notification-ID"

// Address is a mailbox: an email address and an optional display name.
type Address struct {
	Name  string
	Email string
}

// Addr returns an [Address] for email with a display name.
func Addr(name, email string) Address {
	return Address{Name: name, Email: email}
}

// ParseAddress parses an RFC 5322 address such as "a@example.com" or
// "Ada Lovelace <ada@example.com>".
func ParseAddress(s string) (Address, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return Address{}, fmt.Errorf("mail: invalid address %q: %w", s, err)
	}
	return Address{Name: a.Name, Email: a.Address}, nil
}

// String formats the address for a header, quoting and encoding the name as
// needed: "Ada Lovelace <ada@example.com>", or just the email when there is
// no name.
func (a Address) String() string {
	if a.Name == "" {
		return a.Email
	}
	return (&mail.Address{Name: a.Name, Address: a.Email}).String()
}

// IsZero reports whether the address has no email.
func (a Address) IsZero() bool { return a.Email == "" }

// Attachment is a file attached to an [Email].
type Attachment struct {
	// Filename is the name shown to the recipient.
	Filename string
	// ContentType is the MIME type. Empty means it is detected from the
	// filename, then from the content.
	ContentType string
	// Data is the file's content.
	Data []byte
	// Inline marks the attachment as embedded in the HTML body, referenced
	// as <img src="cid:ContentID">.
	Inline bool
	// ContentID identifies an inline attachment. It defaults to Filename.
	ContentID string
}

// MIMEType returns ContentType, or the type detected from the filename and
// content when it is empty.
func (a Attachment) MIMEType() string {
	if a.ContentType != "" {
		return a.ContentType
	}
	if t := mime.TypeByExtension(filepath.Ext(a.Filename)); t != "" {
		return t
	}
	return http.DetectContentType(a.Data)
}

// CID returns the content id of an inline attachment, defaulting to the
// filename.
func (a Attachment) CID() string {
	if a.ContentID != "" {
		return a.ContentID
	}
	return a.Filename
}

// Email is a fully rendered message, ready for a [Transport]. The [Channel]
// builds it from a notification's [Message]; transports only ever see Emails.
type Email struct {
	From    Address
	To      []Address
	Cc      []Address
	Bcc     []Address
	ReplyTo []Address

	Subject string
	// HTML is the HTML body. At least one of HTML and Text is set.
	HTML string
	// Text is the plain-text body.
	Text string

	Attachments []Attachment

	// Headers are extra MIME headers, such as List-Unsubscribe.
	Headers map[string]string

	// Tags and Metadata are passed to providers that support them (Resend,
	// SES, ...) for filtering and analytics. Transports without such a
	// concept ignore them.
	Tags     []string
	Metadata map[string]string
}

// Validate reports whether e can be sent: it needs a valid sender, at least
// one valid recipient, and a body.
func (e *Email) Validate() error {
	if e == nil {
		return errors.New("mail: nil email")
	}
	if e.From.IsZero() {
		return errors.New("mail: email has no sender; set mail.From on the channel or the message")
	}
	if err := validAddr("From", e.From); err != nil {
		return err
	}
	if len(e.To) == 0 && len(e.Cc) == 0 && len(e.Bcc) == 0 {
		return errors.New("mail: email has no recipients")
	}
	for field, list := range map[string][]Address{"To": e.To, "Cc": e.Cc, "Bcc": e.Bcc, "Reply-To": e.ReplyTo} {
		for _, a := range list {
			if err := validAddr(field, a); err != nil {
				return err
			}
		}
	}
	if e.HTML == "" && e.Text == "" {
		return errors.New("mail: email has no body")
	}
	for k := range e.Headers {
		if k == "" || strings.ContainsAny(k, ": \r\n") {
			return fmt.Errorf("mail: invalid header name %q", k)
		}
	}
	for k, v := range e.Headers {
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("mail: header %q contains a line break", k)
		}
	}
	if strings.ContainsAny(e.Subject, "\r\n") {
		return errors.New("mail: subject contains a line break")
	}
	return nil
}

func validAddr(field string, a Address) error {
	if _, err := mail.ParseAddress(a.Email); err != nil || strings.ContainsAny(a.Name, "\r\n") {
		return fmt.Errorf("mail: invalid %s address %q", field, a.Email)
	}
	return nil
}

// AllRecipients returns To, Cc and Bcc together.
func (e *Email) AllRecipients() []Address {
	out := make([]Address, 0, len(e.To)+len(e.Cc)+len(e.Bcc))
	out = append(out, e.To...)
	out = append(out, e.Cc...)
	return append(out, e.Bcc...)
}

// Transport sends rendered emails. It is the adapter contract for mail
// providers: implement it to send through any service, and pass it to
// [NewChannel].
//
// Implementations must be safe for concurrent use and should wrap failures
// that cannot succeed on retry (a rejected address, bad credentials) with
// notification.Permanent.
type Transport interface {
	Send(ctx context.Context, e *Email) error
}

// TransportFunc adapts a function to the [Transport] interface.
type TransportFunc func(ctx context.Context, e *Email) error

// Send implements [Transport].
func (f TransportFunc) Send(ctx context.Context, e *Email) error { return f(ctx, e) }
