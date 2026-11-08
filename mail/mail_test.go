package mail_test

import (
	"bytes"
	"context"
	"errors"
	htmltemplate "html/template"
	"log/slog"
	"strings"
	"testing"
	texttemplate "text/template"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/mail"
)

type recipient struct{ email string }

func (r recipient) RouteNotificationFor(channel string, _ notification.Notification) string {
	if channel == "mail" {
		return r.email
	}
	return ""
}

type invoicePaid struct {
	msg *mail.Message
	err error
}

func (invoicePaid) Via(notification.Notifiable) []string { return []string{"mail"} }
func (n invoicePaid) ToMail(notification.Notifiable) (*mail.Message, error) {
	return n.msg, n.err
}

type smsOnly struct{}

func (smsOnly) Via(notification.Notifiable) []string { return []string{"mail"} }

func delivery(to notification.Notifiable, n notification.Notification) notification.Delivery {
	return notification.Delivery{ID: "id-1", Channel: "mail", To: to, Notification: n, Attempt: 1}
}

func sampleMessage() *mail.Message {
	return mail.NewMessage().
		Subject("Invoice paid").
		Greeting("Hi Ada,").
		Line("Your invoice <b>#1042</b> has been paid.").
		Action("View invoice", "https://acme.test/invoices/1042").
		Line("Thank you!")
}

func TestRenderBuilderMessage(t *testing.T) {
	e, err := mail.Render(sampleMessage(), mail.From("Acme", "hello@acme.test"), mail.WithAppName("Acme"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Subject != "Invoice paid" || e.From.Email != "hello@acme.test" {
		t.Fatalf("subject/from = %q / %v", e.Subject, e.From)
	}
	for _, want := range []string{
		"Hi Ada,",
		"Your invoice &lt;b&gt;#1042&lt;/b&gt; has been paid.",
		`href="https://acme.test/invoices/1042"`,
		">View invoice</a>",
		"Thank you!",
		"Acme",
		"#2d3748",
	} {
		if !strings.Contains(e.HTML, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	if strings.Contains(e.HTML, "<b>#1042</b>") {
		t.Error("line text must be escaped")
	}
	// The intro comes before the button and the outro after it.
	if i, j := strings.Index(e.HTML, "has been paid"), strings.Index(e.HTML, "Thank you!"); i > strings.Index(e.HTML, ">View invoice<") || j < strings.Index(e.HTML, ">View invoice<") {
		t.Error("lines are not placed around the action")
	}

	wantText := "Hi Ada,\n\nYour invoice <b>#1042</b> has been paid.\n\nView invoice: https://acme.test/invoices/1042\n\nThank you!\n\nRegards,\nAcme\n"
	if e.Text != wantText {
		t.Errorf("Text =\n%q\nwant\n%q", e.Text, wantText)
	}
}

func TestRenderLevelsAndDefaults(t *testing.T) {
	e, err := mail.Render(mail.NewMessage().Failure().Line("x").Action("Retry", "https://a.test"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.HTML, "Whoops!") || !strings.Contains(e.HTML, "#e53e3e") {
		t.Error("error level should greet with Whoops! and use the error colour")
	}
	e, _ = mail.Render(mail.NewMessage().Success().Action("Go", "https://a.test"), mail.WithTheme(mail.Theme{SuccessColor: "#00ff00"}))
	if !strings.Contains(e.HTML, "#00ff00") {
		t.Error("custom success colour not used")
	}
	e, _ = mail.Render(mail.NewMessage().Action("Go", "https://a.test"), mail.WithPrimaryColor("red;background:url(x)"))
	if strings.Contains(e.HTML, "url(x)") || !strings.Contains(e.HTML, "#2d3748") {
		t.Error("an invalid colour must fall back to the default")
	}
}

func TestRenderRejectsUnsafeActionURLs(t *testing.T) {
	for _, u := range []string{"javascript:alert(1)", "/relative", "data:text/html,x", "https://"} {
		if _, err := mail.Render(mail.NewMessage().Action("Click", u)); err == nil {
			t.Errorf("Action(%q) should be rejected", u)
		}
	}
	if _, err := mail.Render(mail.NewMessage().Action("Mail us", "mailto:help@acme.test")); err != nil {
		t.Errorf("mailto: %v", err)
	}
}

func TestRenderRawAndViews(t *testing.T) {
	e, err := mail.Render(mail.NewMessage().Subject("s").HTML("<p>raw</p>"))
	if err != nil {
		t.Fatal(err)
	}
	if e.HTML != "<p>raw</p>" || e.Text != "" {
		t.Fatalf("raw html: HTML=%q Text=%q", e.HTML, e.Text)
	}

	ht := htmltemplate.Must(htmltemplate.New("v").Parse(`<p>Hello {{.Name}}</p>`))
	tt := texttemplate.Must(texttemplate.New("t").Parse(`Hello {{.Name}}`))
	e, err = mail.Render(mail.NewMessage().View(ht, "", map[string]string{"Name": "<Ada>"}).TextView(tt, "", map[string]string{"Name": "Ada"}))
	if err != nil {
		t.Fatal(err)
	}
	if e.HTML != "<p>Hello &lt;Ada&gt;</p>" || e.Text != "Hello Ada" {
		t.Fatalf("views: HTML=%q Text=%q", e.HTML, e.Text)
	}

	// A view plus builder lines: the lines still produce the text body.
	e, _ = mail.Render(mail.NewMessage().View(ht, "", map[string]string{"Name": "x"}).Line("from lines"))
	if !strings.Contains(e.Text, "from lines") {
		t.Fatalf("Text = %q", e.Text)
	}
}

func TestRenderAddressesHeadersAttachments(t *testing.T) {
	msg := mail.NewMessage().Subject("s").Line("x").
		From("Billing", "billing@acme.test").
		ReplyTo("Support", "support@acme.test").
		Cc("", "cc@acme.test").
		Bcc("", "bcc@acme.test").
		Header("X-Thing", "1").
		Tag("billing").
		Metadata("invoice", "1042").
		Attach("invoice.pdf", []byte("%PDF-1.4")).
		AttachInline("logo.png", []byte("\x89PNG\r\n\x1a\n"))

	e, err := mail.Render(msg, mail.From("Acme", "hello@acme.test"), mail.ReplyTo("", "noreply@acme.test"), mail.WithHeaders(map[string]string{"X-Thing": "0", "X-App": "acme"}))
	if err != nil {
		t.Fatal(err)
	}
	if e.From.Email != "billing@acme.test" {
		t.Errorf("message From should win, got %v", e.From)
	}
	if len(e.ReplyTo) != 1 || e.ReplyTo[0].Email != "support@acme.test" {
		t.Errorf("ReplyTo = %v", e.ReplyTo)
	}
	if e.Headers["X-Thing"] != "1" || e.Headers["X-App"] != "acme" {
		t.Errorf("Headers = %v", e.Headers)
	}
	if len(e.Cc) != 1 || len(e.Bcc) != 1 || e.Tags[0] != "billing" || e.Metadata["invoice"] != "1042" {
		t.Errorf("cc/bcc/tags/metadata = %v %v %v %v", e.Cc, e.Bcc, e.Tags, e.Metadata)
	}
	if len(e.Attachments) != 2 {
		t.Fatalf("attachments = %d", len(e.Attachments))
	}
	if got := e.Attachments[0].MIMEType(); got != "application/pdf" {
		t.Errorf("pdf type = %q", got)
	}
	if a := e.Attachments[1]; !a.Inline || a.CID() != "logo.png" || a.MIMEType() != "image/png" {
		t.Errorf("inline attachment = %+v (%s)", a, a.MIMEType())
	}
}

func TestRenderDoesNotMutateMessage(t *testing.T) {
	msg := sampleMessage().Header("A", "1")
	e, _ := mail.Render(msg)
	e.Headers["A"] = "changed"
	e2, _ := mail.Render(msg)
	if e2.Headers["A"] != "1" {
		t.Fatal("rendering shares maps with the message")
	}
}

func TestChannelSends(t *testing.T) {
	tr := mail.NewArrayTransport()
	ch := mail.NewChannel(tr, mail.From("Acme", "hello@acme.test"))

	err := ch.Send(t.Context(), delivery(recipient{"Ada Lovelace <ada@example.test>"}, invoicePaid{msg: sampleMessage()}))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := tr.Last()
	if !ok {
		t.Fatal("nothing sent")
	}
	if len(e.To) != 1 || e.To[0].Email != "ada@example.test" || e.To[0].Name != "Ada Lovelace" {
		t.Errorf("To = %v", e.To)
	}
	if e.Headers[mail.HeaderNotificationID] != "id-1" {
		t.Errorf("notification id header = %q", e.Headers[mail.HeaderNotificationID])
	}
}

func TestChannelErrors(t *testing.T) {
	ch := mail.NewChannel(mail.NewArrayTransport(), mail.From("", "hello@acme.test"))

	cases := map[string]struct {
		d     notification.Delivery
		check func(error) bool
	}{
		"no route":       {delivery(recipient{""}, invoicePaid{msg: sampleMessage()}), func(err error) bool { return errors.Is(err, notification.ErrNoRoute) }},
		"unsupported":    {delivery(recipient{"a@example.test"}, smsOnly{}), func(err error) bool { return errors.Is(err, notification.ErrUnsupported) }},
		"bad address":    {delivery(recipient{"not an address"}, invoicePaid{msg: sampleMessage()}), notification.IsPermanent},
		"nil message":    {delivery(recipient{"a@example.test"}, invoicePaid{}), notification.IsPermanent},
		"ToMail failure": {delivery(recipient{"a@example.test"}, invoicePaid{err: errors.New("db down")}), func(err error) bool { return err != nil && !notification.IsPermanent(err) }},
	}
	for name, tc := range cases {
		if err := ch.Send(t.Context(), tc.d); !tc.check(err) {
			t.Errorf("%s: unexpected error %v", name, err)
		}
	}

	noSender := mail.NewChannel(mail.NewArrayTransport())
	err := noSender.Send(t.Context(), delivery(recipient{"a@example.test"}, invoicePaid{msg: sampleMessage()}))
	if !notification.IsPermanent(err) || !strings.Contains(err.Error(), "sender") {
		t.Errorf("missing sender: %v", err)
	}
}

func TestChannelAlwaysTo(t *testing.T) {
	tr := mail.NewArrayTransport()
	ch := mail.NewChannel(tr, mail.From("", "hello@acme.test"), mail.AlwaysTo(mail.Addr("Dev", "dev@acme.test")))

	msg := sampleMessage().Cc("", "boss@example.test").Bcc("", "audit@example.test")
	if err := ch.Send(t.Context(), delivery(recipient{"ada@example.test"}, invoicePaid{msg: msg})); err != nil {
		t.Fatal(err)
	}
	e, _ := tr.Last()
	if len(e.To) != 1 || e.To[0].Email != "dev@acme.test" || len(e.Cc) != 0 || len(e.Bcc) != 0 {
		t.Fatalf("AlwaysTo not applied: to=%v cc=%v bcc=%v", e.To, e.Cc, e.Bcc)
	}
	if e.Headers["X-Original-To"] != "ada@example.test" {
		t.Errorf("X-Original-To = %q", e.Headers["X-Original-To"])
	}
}

func TestThroughNotifier(t *testing.T) {
	tr := mail.NewArrayTransport()
	n := notification.New(notification.WithChannel("mail", mail.NewChannel(tr, mail.From("", "hello@acme.test"))))
	if err := n.Send(t.Context(), recipient{"ada@example.test"}, invoicePaid{msg: sampleMessage()}); err != nil {
		t.Fatal(err)
	}
	if len(tr.Emails()) != 1 {
		t.Fatal("expected one email")
	}
}

func TestValidate(t *testing.T) {
	ok := func() *mail.Email {
		return &mail.Email{From: mail.Addr("", "a@example.test"), To: []mail.Address{mail.Addr("", "b@example.test")}, Text: "hi"}
	}
	if err := ok().Validate(); err != nil {
		t.Fatalf("valid email: %v", err)
	}
	mutations := map[string]func(e *mail.Email){
		"no from":        func(e *mail.Email) { e.From = mail.Address{} },
		"bad to":         func(e *mail.Email) { e.To = []mail.Address{mail.Addr("", "nope")} },
		"no recipients":  func(e *mail.Email) { e.To = nil },
		"no body":        func(e *mail.Email) { e.Text = "" },
		"header newline": func(e *mail.Email) { e.Headers = map[string]string{"X": "a\r\nBcc: evil@x.test"} },
		"subject break":  func(e *mail.Email) { e.Subject = "a\nb" },
		"bad header key": func(e *mail.Email) { e.Headers = map[string]string{"X Y": "1"} },
	}
	for name, mutate := range mutations {
		e := ok()
		mutate(e)
		if e.Validate() == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestAddress(t *testing.T) {
	a, err := mail.ParseAddress(" Ada Lovelace <ada@example.test> ")
	if err != nil || a.Name != "Ada Lovelace" || a.Email != "ada@example.test" {
		t.Fatalf("ParseAddress = %+v, %v", a, err)
	}
	if got := a.String(); got != `"Ada Lovelace" <ada@example.test>` {
		t.Errorf("String = %q", got)
	}
	if got := mail.Addr("", "x@example.test").String(); got != "x@example.test" {
		t.Errorf("String without name = %q", got)
	}
	if _, err := mail.ParseAddress("nope"); err == nil {
		t.Error("expected an error")
	}
}

func TestFailover(t *testing.T) {
	email := &mail.Email{}
	down := mail.TransportFunc(func(context.Context, *mail.Email) error { return errors.New("down") })
	rejected := mail.TransportFunc(func(context.Context, *mail.Email) error {
		return notification.Permanent(errors.New("rejected"))
	})
	backup := mail.NewArrayTransport()

	if err := mail.Failover(down, backup).Send(t.Context(), email); err != nil {
		t.Fatalf("failover to backup: %v", err)
	}
	if len(backup.Emails()) != 1 {
		t.Fatal("backup did not receive the email")
	}

	err := mail.Failover(rejected, down).Send(t.Context(), email)
	if err == nil || notification.IsPermanent(err) {
		t.Fatalf("mixed failures should be retryable, got %v", err)
	}
	err = mail.Failover(rejected, rejected).Send(t.Context(), email)
	if !notification.IsPermanent(err) {
		t.Fatalf("all permanent failures should be permanent, got %v", err)
	}
	if err := mail.Failover().Send(t.Context(), email); err == nil {
		t.Fatal("empty failover should fail")
	}
}

func TestRoundRobin(t *testing.T) {
	a, b := mail.NewArrayTransport(), mail.NewArrayTransport()
	rr := mail.RoundRobin(a, b)
	for range 4 {
		if err := rr.Send(t.Context(), &mail.Email{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(a.Emails()) != 2 || len(b.Emails()) != 2 {
		t.Fatalf("distribution a=%d b=%d, want 2/2", len(a.Emails()), len(b.Emails()))
	}

	a.FailWith(errors.New("down"))
	for range 2 {
		if err := rr.Send(t.Context(), &mail.Email{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.Emails()) != 4 {
		t.Fatalf("b should take over, has %d", len(b.Emails()))
	}
}

func TestLogTransport(t *testing.T) {
	var buf bytes.Buffer
	tr := mail.NewLogTransport(slog.New(slog.NewTextHandler(&buf, nil)))
	e := &mail.Email{From: mail.Addr("", "a@example.test"), To: []mail.Address{mail.Addr("", "b@example.test")}, Subject: "Hi", Text: "body text"}
	if err := tr.Send(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "subject=Hi") || !strings.Contains(out, "body text") {
		t.Fatalf("log output = %s", out)
	}
	buf.Reset()
	_ = tr.WithoutBody().Send(t.Context(), e)
	if strings.Contains(buf.String(), "body text") {
		t.Fatal("WithoutBody still logged the body")
	}
}
