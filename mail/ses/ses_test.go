package ses_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/mail"
	"github.com/zahansafallwa1511/go-notification/mail/ses"
)

type fakeClient struct {
	in  *sesv2.SendEmailInput
	err error
}

func (f *fakeClient) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.in = in
	if f.err != nil {
		return nil, f.err
	}
	return &sesv2.SendEmailOutput{MessageId: new(string)}, nil
}

func email() *mail.Email {
	return &mail.Email{
		From:     mail.Addr("Acme", "hello@acme.test"),
		To:       []mail.Address{mail.Addr("", "ada@example.test")},
		Bcc:      []mail.Address{mail.Addr("", "audit@example.test")},
		ReplyTo:  []mail.Address{mail.Addr("", "support@acme.test")},
		Subject:  "Invoice paid",
		HTML:     "<p>Paid</p>",
		Text:     "Paid",
		Headers:  map[string]string{mail.HeaderNotificationID: "n-1"},
		Tags:     []string{"billing"},
		Metadata: map[string]string{"invoice.id": "1042"},
		Attachments: []mail.Attachment{
			{Filename: "invoice.pdf", Data: []byte("%PDF")},
			{Filename: "logo.png", Data: []byte("png"), Inline: true},
		},
	}
}

func TestSend(t *testing.T) {
	fc := &fakeClient{}
	tr := ses.New(fc, ses.WithConfigurationSet("tx"), ses.WithFromARN("arn:aws:ses:x"), ses.WithTags(map[string]string{"app": "acme"}))
	if err := tr.Send(t.Context(), email()); err != nil {
		t.Fatal(err)
	}
	in := fc.in
	if *in.FromEmailAddress != `"Acme" <hello@acme.test>` || *in.ConfigurationSetName != "tx" || *in.FromEmailAddressIdentityArn != "arn:aws:ses:x" {
		t.Errorf("input = %+v", in)
	}
	if in.Destination.ToAddresses[0] != "ada@example.test" || in.Destination.BccAddresses[0] != "audit@example.test" || in.ReplyToAddresses[0] != "support@acme.test" {
		t.Errorf("destination = %+v", in.Destination)
	}
	msg := in.Content.Simple
	if *msg.Subject.Data != "Invoice paid" || *msg.Body.Html.Data != "<p>Paid</p>" || *msg.Body.Text.Data != "Paid" {
		t.Errorf("message = %+v", msg)
	}
	if len(msg.Headers) != 1 || *msg.Headers[0].Name != mail.HeaderNotificationID {
		t.Errorf("headers = %+v", msg.Headers)
	}
	if len(msg.Attachments) != 2 {
		t.Fatalf("attachments = %d", len(msg.Attachments))
	}
	if a := msg.Attachments[0]; *a.ContentType != "application/pdf" || a.ContentDisposition != types.AttachmentContentDispositionAttachment {
		t.Errorf("attachment = %+v", a)
	}
	if a := msg.Attachments[1]; a.ContentDisposition != types.AttachmentContentDispositionInline || *a.ContentId != "logo.png" {
		t.Errorf("inline = %+v", a)
	}
	tags := map[string]string{}
	for _, tg := range in.EmailTags {
		tags[*tg.Name] = *tg.Value
	}
	if tags["app"] != "acme" || tags["billing"] != "true" || tags["invoice_id"] != "1042" {
		t.Errorf("tags = %v", tags)
	}
}

func TestSendErrors(t *testing.T) {
	cases := map[string]struct {
		err       error
		permanent bool
	}{
		"rejected":   {&smithy.GenericAPIError{Code: "MessageRejected", Fault: smithy.FaultClient}, true},
		"throttled":  {&smithy.GenericAPIError{Code: "TooManyRequestsException", Fault: smithy.FaultClient}, false},
		"server":     {&smithy.GenericAPIError{Code: "InternalFailure", Fault: smithy.FaultServer}, false},
		"network":    {errors.New("dial tcp: timeout"), false},
		"badrequest": {&types.BadRequestException{Message: new(string)}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ses.New(&fakeClient{err: tc.err}).Send(t.Context(), email())
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want it to wrap %v", err, tc.err)
			}
			if got := notification.IsPermanent(err); got != tc.permanent {
				t.Fatalf("IsPermanent = %v, want %v", got, tc.permanent)
			}
		})
	}
}
