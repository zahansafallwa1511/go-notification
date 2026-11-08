// Package ses is a [mail.Transport] for Amazon SES, using the SES v2 API of
// the AWS SDK for Go v2.
//
// It is a separate module so that the core go-notification module stays free
// of third-party dependencies:
//
//	go get github.com/zahansafallwa1511/go-notification/mail/ses
//
// Usage:
//
//	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("eu-west-2"))
//	if err != nil {
//		return err
//	}
//	t := ses.New(sesv2.NewFromConfig(cfg), ses.WithConfigurationSet("transactional"))
//	ch := mail.NewChannel(t, mail.From("Acme", "hello@acme.test"))
//
// Messages are sent as SES "simple" content, with attachments, inline images
// and custom headers. Email tags become SES message tags with the value
// "true", and metadata entries become tags with their own value. SES allows
// only ASCII letters, digits, underscores and dashes in tags, so other
// characters are replaced with "_".
//
// Client errors such as an unverified sender or a rejected message are
// reported as permanent (see notification.Permanent); throttling and server
// errors are retryable.
package ses

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/mail"
)

// Client is the part of *sesv2.Client the transport uses. Tests can supply a
// fake.
type Client interface {
	SendEmail(ctx context.Context, in *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

var _ Client = (*sesv2.Client)(nil)

// Transport sends email through Amazon SES. It is safe for concurrent use.
type Transport struct {
	client           Client
	configurationSet string
	fromARN          string
	feedbackAddress  string
	tags             map[string]string
}

var _ mail.Transport = (*Transport)(nil)

// Option configures a [Transport].
type Option func(*Transport)

// WithConfigurationSet sends every email with the named configuration set,
// for event publishing and dedicated IP pools.
func WithConfigurationSet(name string) Option {
	return func(t *Transport) { t.configurationSet = name }
}

// WithFromARN sets the ARN of the identity authorised to send from the
// sender address, for sending authorisation.
func WithFromARN(arn string) Option {
	return func(t *Transport) { t.fromARN = arn }
}

// WithFeedbackAddress sets where bounces and complaints are forwarded.
func WithFeedbackAddress(email string) Option {
	return func(t *Transport) { t.feedbackAddress = email }
}

// WithTags adds message tags to every email.
func WithTags(tags map[string]string) Option {
	return func(t *Transport) {
		if t.tags == nil {
			t.tags = map[string]string{}
		}
		for k, v := range tags {
			t.tags[k] = v
		}
	}
}

// New returns a transport that sends with client, typically
// sesv2.NewFromConfig(cfg).
func New(client Client, opts ...Option) *Transport {
	t := &Transport{client: client}
	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}
	return t
}

// Send implements [mail.Transport].
func (t *Transport) Send(ctx context.Context, e *mail.Email) error {
	in := t.input(e)
	if _, err := t.client.SendEmail(ctx, in); err != nil {
		if permanent(err) {
			return notification.Permanent(fmt.Errorf("ses: %w", err))
		}
		return fmt.Errorf("ses: %w", err)
	}
	return nil
}

func (t *Transport) input(e *mail.Email) *sesv2.SendEmailInput {
	msg := &types.Message{
		Subject: &types.Content{Data: ptr(e.Subject), Charset: ptr("UTF-8")},
		Body:    &types.Body{},
	}
	if e.HTML != "" {
		msg.Body.Html = &types.Content{Data: ptr(e.HTML), Charset: ptr("UTF-8")}
	}
	if e.Text != "" {
		msg.Body.Text = &types.Content{Data: ptr(e.Text), Charset: ptr("UTF-8")}
	}
	for k, v := range e.Headers {
		msg.Headers = append(msg.Headers, types.MessageHeader{Name: ptr(k), Value: ptr(v)})
	}
	for _, a := range e.Attachments {
		att := types.Attachment{
			FileName:           ptr(a.Filename),
			RawContent:         a.Data,
			ContentType:        ptr(a.MIMEType()),
			ContentDisposition: types.AttachmentContentDispositionAttachment,
		}
		if a.Inline {
			att.ContentDisposition = types.AttachmentContentDispositionInline
			att.ContentId = ptr(a.CID())
		}
		msg.Attachments = append(msg.Attachments, att)
	}

	in := &sesv2.SendEmailInput{
		FromEmailAddress: ptr(e.From.String()),
		Destination: &types.Destination{
			ToAddresses:  addrs(e.To),
			CcAddresses:  addrs(e.Cc),
			BccAddresses: addrs(e.Bcc),
		},
		ReplyToAddresses: addrs(e.ReplyTo),
		Content:          &types.EmailContent{Simple: msg},
	}
	if t.configurationSet != "" {
		in.ConfigurationSetName = ptr(t.configurationSet)
	}
	if t.fromARN != "" {
		in.FromEmailAddressIdentityArn = ptr(t.fromARN)
	}
	if t.feedbackAddress != "" {
		in.FeedbackForwardingEmailAddress = ptr(t.feedbackAddress)
	}

	for k, v := range t.tags {
		in.EmailTags = append(in.EmailTags, tag(k, v))
	}
	for _, name := range e.Tags {
		in.EmailTags = append(in.EmailTags, tag(name, "true"))
	}
	for k, v := range e.Metadata {
		in.EmailTags = append(in.EmailTags, tag(k, v))
	}
	return in
}

// permanent reports whether an SES error cannot succeed on retry: a client
// fault that is not throttling.
func permanent(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorFault() != smithy.FaultClient {
		return false
	}
	code := apiErr.ErrorCode()
	for _, temp := range []string{"TooManyRequests", "Throttl", "LimitExceeded"} {
		if strings.Contains(code, temp) {
			return false
		}
	}
	return true
}

func tag(name, value string) types.MessageTag {
	return types.MessageTag{Name: ptr(sanitize(name)), Value: ptr(sanitize(value))}
}

func addrs(list []mail.Address) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.String()
	}
	return out
}

// sanitize maps a string onto the SES tag alphabet.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
}

func ptr[T any](v T) *T { return &v }
