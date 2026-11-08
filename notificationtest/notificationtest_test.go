package notificationtest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/notificationtest"
)

type customer struct{ ID int }

func (c customer) RouteNotificationFor(string, notification.Notification) string {
	return fmt.Sprint(c.ID)
}

type invoicePaid struct{ Amount int }

func (invoicePaid) Via(notification.Notifiable) []string { return []string{"mail", "database"} }

type passwordReset struct{}

func (passwordReset) Via(notification.Notifiable) []string { return []string{"mail"} }

// recordingT captures failures so assertion helpers can be tested failing.
type recordingT struct {
	testing.TB
	failed bool
}

func (r *recordingT) Helper()                  {}
func (r *recordingT) Errorf(string, ...any)    { r.failed = true }
func (r *recordingT) Fatalf(string, ...any)    { r.failed = true }
func (r *recordingT) Logf(string, ...any)      {}
func (r *recordingT) Cleanup(func())           {}
func (r *recordingT) Name() string             { return "recording" }
func (r *recordingT) Context() context.Context { return context.Background() }

func TestFakeAssertions(t *testing.T) {
	fake := notificationtest.NewFake()
	alice, bob := customer{ID: 1}, customer{ID: 2}

	notificationtest.AssertNothingSent(t, fake)

	if err := fake.Send(t.Context(), alice, invoicePaid{Amount: 42}); err != nil {
		t.Fatal(err)
	}
	if err := fake.SendNow(t.Context(), alice, invoicePaid{Amount: 7}); err != nil {
		t.Fatal(err)
	}

	notificationtest.AssertCount(t, fake, 2)
	notificationtest.AssertSentTimes[invoicePaid](t, fake, 2)
	notificationtest.AssertSentTo[invoicePaid](t, fake, alice)
	notificationtest.AssertNotSentTo[invoicePaid](t, fake, bob)
	notificationtest.AssertSentOn[invoicePaid](t, fake, "database")
	notificationtest.AssertNotSent[passwordReset](t, fake)
	notificationtest.AssertSent(t, fake, func(s notificationtest.Sent, n invoicePaid) bool {
		return n.Amount == 7 && s.Now
	})

	if got := notificationtest.Of[invoicePaid](fake); len(got) != 2 || got[0].Amount != 42 {
		t.Fatalf("Of = %+v", got)
	}

	fake.Reset()
	notificationtest.AssertNothingSent(t, fake)
}

func TestFakeAssertionsFail(t *testing.T) {
	fake := notificationtest.NewFake()
	_ = fake.Send(t.Context(), customer{ID: 1}, invoicePaid{})

	cases := map[string]func(testing.TB){
		"AssertNothingSent": func(tb testing.TB) { notificationtest.AssertNothingSent(tb, fake) },
		"AssertCount":       func(tb testing.TB) { notificationtest.AssertCount(tb, fake, 3) },
		"AssertSentTo":      func(tb testing.TB) { notificationtest.AssertSentTo[invoicePaid](tb, fake, customer{ID: 9}) },
		"AssertNotSentTo":   func(tb testing.TB) { notificationtest.AssertNotSentTo[invoicePaid](tb, fake, customer{ID: 1}) },
		"AssertSentOn":      func(tb testing.TB) { notificationtest.AssertSentOn[invoicePaid](tb, fake, "sms") },
		"AssertNotSent":     func(tb testing.TB) { notificationtest.AssertNotSent[invoicePaid](tb, fake) },
		"AssertSent":        func(tb testing.TB) { notificationtest.AssertSent[passwordReset](tb, fake) },
		"AssertSentTimes":   func(tb testing.TB) { notificationtest.AssertSentTimes[invoicePaid](tb, fake, 2) },
	}
	for name, assert := range cases {
		rt := &recordingT{TB: t}
		assert(rt)
		if !rt.failed {
			t.Errorf("%s should have failed", name)
		}
	}
}

func TestFakeFailWith(t *testing.T) {
	fake := notificationtest.NewFake()
	boom := errors.New("boom")
	fake.FailWith(boom)
	if err := fake.Send(t.Context(), customer{}, invoicePaid{}); !errors.Is(err, boom) {
		t.Fatalf("Send = %v, want boom", err)
	}
	notificationtest.AssertCount(t, fake, 1)
}

func TestRecorderWithNotifier(t *testing.T) {
	rec := notificationtest.NewRecorder()
	n := notification.New(
		notification.WithChannel("mail", rec),
		notification.WithChannel("database", rec),
	)
	if err := n.Send(t.Context(), customer{ID: 1}, invoicePaid{}); err != nil {
		t.Fatal(err)
	}
	d := rec.Deliveries()
	if len(d) != 2 || d[0].Channel != "mail" || d[1].Channel != "database" {
		t.Fatalf("deliveries = %+v", d)
	}
}
