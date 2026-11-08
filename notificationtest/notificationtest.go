// Package notificationtest provides test doubles for code that sends
// notifications.
//
// [Fake] stands in for a [notification.Sender]. Code under test depends on the
// Sender interface; the test hands it a Fake and asserts on what was sent,
// without any channel running:
//
//	fake := notificationtest.NewFake()
//	svc := billing.NewService(fake)
//
//	svc.MarkPaid(ctx, invoice)
//
//	notificationtest.AssertSentTo[billing.InvoicePaid](t, fake, customer)
//
// [Recorder] is a [notification.Channel] that records deliveries instead of
// sending them, for tests that exercise a real [notification.Notifier].
package notificationtest

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	notification "github.com/zahansafallwa1511/go-notification"
)

// Sent is one notification recorded by a [Fake].
type Sent struct {
	To           notification.Notifiable
	Notification notification.Notification
	// Channels is what Notification.Via returned for To.
	Channels []string
	// Now reports whether it was sent with SendNow.
	Now bool
}

// HasChannel reports whether s was routed to channel.
func (s Sent) HasChannel(channel string) bool {
	for _, c := range s.Channels {
		if c == channel {
			return true
		}
	}
	return false
}

// Fake is a [notification.Sender] that records instead of sending. It is safe
// for concurrent use.
type Fake struct {
	mu   sync.Mutex
	sent []Sent
	err  error
}

var _ notification.Sender = (*Fake)(nil)

// NewFake returns an empty Fake.
func NewFake() *Fake { return &Fake{} }

// FailWith makes every later Send and SendNow return err, after recording.
// Pass nil to succeed again.
func (f *Fake) FailWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Send implements [notification.Sender].
func (f *Fake) Send(_ context.Context, to notification.Notifiable, n notification.Notification) error {
	return f.record(to, n, false)
}

// SendNow implements [notification.Sender].
func (f *Fake) SendNow(_ context.Context, to notification.Notifiable, n notification.Notification) error {
	return f.record(to, n, true)
}

func (f *Fake) record(to notification.Notifiable, n notification.Notification, now bool) error {
	var channels []string
	if n != nil && to != nil {
		channels = n.Via(to)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, Sent{To: to, Notification: n, Channels: channels, Now: now})
	return f.err
}

// Sent returns everything recorded so far, in order.
func (f *Fake) Sent() []Sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Sent(nil), f.sent...)
}

// Reset forgets everything recorded.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = nil
}

// Of returns the recorded notifications of type T, in order.
func Of[T notification.Notification](f *Fake) []T {
	var out []T
	for _, s := range f.Sent() {
		if n, ok := s.Notification.(T); ok {
			out = append(out, n)
		}
	}
	return out
}

// Matching returns the recorded sends whose notification is a T and for which
// every match function reports true.
func Matching[T notification.Notification](f *Fake, match ...func(s Sent, n T) bool) []Sent {
	var out []Sent
	for _, s := range f.Sent() {
		n, ok := s.Notification.(T)
		if !ok {
			continue
		}
		keep := true
		for _, m := range match {
			if !m(s, n) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, s)
		}
	}
	return out
}

// AssertSent fails the test unless at least one T was sent that satisfies
// every match function.
func AssertSent[T notification.Notification](t testing.TB, f *Fake, match ...func(s Sent, n T) bool) {
	t.Helper()
	if len(Matching(f, match...)) == 0 {
		t.Errorf("expected a %s to be sent; sent: %s", typeName[T](), summary(f))
	}
}

// AssertSentTimes fails the test unless exactly times T were sent.
func AssertSentTimes[T notification.Notification](t testing.TB, f *Fake, times int) {
	t.Helper()
	if got := len(Matching[T](f)); got != times {
		t.Errorf("expected %s to be sent %d times, got %d; sent: %s", typeName[T](), times, got, summary(f))
	}
}

// AssertSentTo fails the test unless a T was sent to to. Recipients are
// compared with reflect.DeepEqual.
func AssertSentTo[T notification.Notification](t testing.TB, f *Fake, to notification.Notifiable) {
	t.Helper()
	if len(Matching(f, sentTo[T](to))) == 0 {
		t.Errorf("expected a %s to be sent to %v; sent: %s", typeName[T](), to, summary(f))
	}
}

// AssertNotSentTo fails the test if a T was sent to to.
func AssertNotSentTo[T notification.Notification](t testing.TB, f *Fake, to notification.Notifiable) {
	t.Helper()
	if len(Matching(f, sentTo[T](to))) > 0 {
		t.Errorf("expected no %s to be sent to %v", typeName[T](), to)
	}
}

// AssertSentOn fails the test unless a T was routed to channel.
func AssertSentOn[T notification.Notification](t testing.TB, f *Fake, channel string) {
	t.Helper()
	on := func(s Sent, _ T) bool { return s.HasChannel(channel) }
	if len(Matching(f, on)) == 0 {
		t.Errorf("expected a %s to be sent on %q; sent: %s", typeName[T](), channel, summary(f))
	}
}

// AssertNotSent fails the test if any T was sent.
func AssertNotSent[T notification.Notification](t testing.TB, f *Fake) {
	t.Helper()
	if n := len(Matching[T](f)); n > 0 {
		t.Errorf("expected no %s to be sent, got %d", typeName[T](), n)
	}
}

// AssertCount fails the test unless exactly n notifications were sent.
func AssertCount(t testing.TB, f *Fake, n int) {
	t.Helper()
	if got := len(f.Sent()); got != n {
		t.Errorf("expected %d notifications to be sent, got %d; sent: %s", n, got, summary(f))
	}
}

// AssertNothingSent fails the test if anything was sent.
func AssertNothingSent(t testing.TB, f *Fake) {
	t.Helper()
	if got := len(f.Sent()); got != 0 {
		t.Errorf("expected nothing to be sent, got %d: %s", got, summary(f))
	}
}

func sentTo[T notification.Notification](to notification.Notifiable) func(Sent, T) bool {
	return func(s Sent, _ T) bool { return reflect.DeepEqual(s.To, to) }
}

func typeName[T any]() string {
	return reflect.TypeFor[T]().String()
}

func summary(f *Fake) string {
	sent := f.Sent()
	if len(sent) == 0 {
		return "nothing"
	}
	out := ""
	for i, s := range sent {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%T%v", s.Notification, s.Channels)
	}
	return out
}

// Recorder is a [notification.Channel] that records deliveries instead of
// sending them. It is safe for concurrent use.
//
//	rec := notificationtest.NewRecorder()
//	n := notification.New(notification.WithChannel("mail", rec))
type Recorder struct {
	mu         sync.Mutex
	deliveries []notification.Delivery
	err        error
}

var _ notification.Channel = (*Recorder)(nil)

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// FailWith makes later deliveries return err, after recording. Pass nil to
// succeed again.
func (r *Recorder) FailWith(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// Send implements [notification.Channel].
func (r *Recorder) Send(_ context.Context, d notification.Delivery) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deliveries = append(r.deliveries, d)
	return r.err
}

// Deliveries returns everything recorded so far, in order.
func (r *Recorder) Deliveries() []notification.Delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notification.Delivery(nil), r.deliveries...)
}
