package sms_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/sms"
)

type shipped struct{ from string }

func (shipped) Via(notification.Notifiable) []string { return []string{"sms"} }
func (s shipped) ToSMS(notification.Notifiable) (*sms.Message, error) {
	return sms.NewMessage("Your order shipped").Sender(s.from).AsUnicode(), nil
}

type plain struct{}

func (plain) Via(notification.Notifiable) []string { return []string{"sms"} }

func delivery(route string, n notification.Notification) notification.Delivery {
	return notification.Delivery{ID: "id-1", Channel: "sms", To: notification.Route("sms", route), Notification: n, Attempt: 1}
}

func TestChannelSends(t *testing.T) {
	p := sms.NewArrayProvider()
	ch := sms.NewChannel(p, sms.WithFrom("+15550000"))

	if err := ch.Send(t.Context(), delivery("+15550100", shipped{})); err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(t.Context(), delivery("+15550101", shipped{from: "ACME"})); err != nil {
		t.Fatal(err)
	}
	got := p.Messages()
	want := []sms.SMS{
		{To: "+15550100", From: "+15550000", Body: "Your order shipped", Unicode: true, ID: "id-1"},
		{To: "+15550101", From: "ACME", Body: "Your order shipped", Unicode: true, ID: "id-1"},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("messages = %+v, want %+v", got, want)
	}
	p.Reset()
	if len(p.Messages()) != 0 {
		t.Fatal("Reset did not clear")
	}
}

func TestChannelErrors(t *testing.T) {
	ch := sms.NewChannel(sms.NewArrayProvider())
	if err := ch.Send(t.Context(), delivery("", shipped{})); !errors.Is(err, notification.ErrNoRoute) {
		t.Fatalf("empty route: %v", err)
	}
	if err := ch.Send(t.Context(), delivery("+1", plain{})); !errors.Is(err, notification.ErrUnsupported) {
		t.Fatalf("unsupported: %v", err)
	}
	boom := errors.New("boom")
	failing := sms.NewChannel(sms.ProviderFunc(func(context.Context, sms.SMS) error { return boom }))
	if err := failing.Send(t.Context(), delivery("+1", shipped{})); !errors.Is(err, boom) {
		t.Fatalf("provider error: %v", err)
	}
}

type empty struct{ plain }

func (empty) ToSMS(notification.Notifiable) (*sms.Message, error) { return sms.NewMessage(" "), nil }

func TestEmptyContentIsPermanent(t *testing.T) {
	err := sms.NewChannel(sms.NewArrayProvider()).Send(t.Context(), delivery("+1", empty{}))
	if err == nil || !notification.IsPermanent(err) {
		t.Fatalf("err = %v, want permanent", err)
	}
}

func TestLogProvider(t *testing.T) {
	var buf bytes.Buffer
	p := sms.NewLogProvider(slog.New(slog.NewTextHandler(&buf, nil)))
	if err := sms.NewChannel(p).Send(t.Context(), delivery("+15550100", shipped{})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "+15550100") || !strings.Contains(buf.String(), "Your order shipped") {
		t.Fatalf("log = %q", buf.String())
	}
}
