package notification_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/notificationtest"
)

type user struct {
	Email string
	Phone string
}

func (u user) RouteNotificationFor(channel string, _ notification.Notification) string {
	switch channel {
	case "mail":
		return u.Email
	case "sms":
		return u.Phone
	}
	return ""
}

type welcome struct {
	channels []string
	queued   bool
	allow    func(channel string) bool
	tries    int
}

func (w welcome) Via(notification.Notifiable) []string { return w.channels }
func (w welcome) ShouldQueue() bool                    { return w.queued }
func (w welcome) Tries() int                           { return w.tries }
func (w welcome) Backoff() []time.Duration             { return []time.Duration{time.Millisecond} }
func (w welcome) ShouldSend(_ notification.Notifiable, channel string) bool {
	return w.allow == nil || w.allow(channel)
}

// routeChannel fails with ErrNoRoute when the recipient has no address, like a
// real channel does.
func routeChannel(rec *notificationtest.Recorder) notification.Channel {
	return notification.ChannelFunc(func(ctx context.Context, d notification.Delivery) error {
		if d.Route() == "" {
			return notification.ErrNoRoute
		}
		return rec.Send(ctx, d)
	})
}

func TestSendDeliversToEveryChannel(t *testing.T) {
	mail, sms := notificationtest.NewRecorder(), notificationtest.NewRecorder()
	n := notification.New(
		notification.WithChannel("mail", routeChannel(mail)),
		notification.WithChannel("sms", routeChannel(sms)),
	)

	u := user{Email: "a@example.test", Phone: "+15550100"}
	if err := n.Send(t.Context(), u, welcome{channels: []string{"mail", "sms", "mail"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	md, sd := mail.Deliveries(), sms.Deliveries()
	if len(md) != 1 || len(sd) != 1 {
		t.Fatalf("deliveries: mail=%d sms=%d, want 1 each (duplicates in Via are ignored)", len(md), len(sd))
	}
	if md[0].ID == "" || md[0].ID != sd[0].ID {
		t.Fatalf("delivery ids %q and %q should match and be non-empty", md[0].ID, sd[0].ID)
	}
	if md[0].Route() != "a@example.test" || sd[0].Route() != "+15550100" {
		t.Fatalf("routes = %q, %q", md[0].Route(), sd[0].Route())
	}
	if md[0].Attempt != 1 {
		t.Fatalf("Attempt = %d, want 1", md[0].Attempt)
	}
}

func TestSendSkipsMissingRoute(t *testing.T) {
	sms := notificationtest.NewRecorder()
	var skipped []error
	n := notification.New(
		notification.WithChannel("sms", routeChannel(sms)),
		notification.WithListener(notification.ListenerFuncs{
			Skipped: func(_ context.Context, _ notification.Delivery, reason error) { skipped = append(skipped, reason) },
		}),
	)

	if err := n.Send(t.Context(), user{Email: "a@example.test"}, welcome{channels: []string{"sms"}}); err != nil {
		t.Fatalf("Send without a route should not fail: %v", err)
	}
	if len(sms.Deliveries()) != 0 {
		t.Fatal("channel should not have delivered")
	}
	if len(skipped) != 1 || !errors.Is(skipped[0], notification.ErrNoRoute) {
		t.Fatalf("skipped = %v, want [ErrNoRoute]", skipped)
	}
}

func TestShouldSendSkipsChannel(t *testing.T) {
	mail, sms := notificationtest.NewRecorder(), notificationtest.NewRecorder()
	n := notification.New(
		notification.WithChannel("mail", mail),
		notification.WithChannel("sms", sms),
	)

	w := welcome{channels: []string{"mail", "sms"}, allow: func(c string) bool { return c == "mail" }}
	if err := n.Send(t.Context(), user{}, w); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(mail.Deliveries()) != 1 || len(sms.Deliveries()) != 0 {
		t.Fatalf("mail=%d sms=%d, want 1 and 0", len(mail.Deliveries()), len(sms.Deliveries()))
	}
}

func TestSendReportsEachFailedChannel(t *testing.T) {
	boom := errors.New("boom")
	failing := notificationtest.NewRecorder()
	failing.FailWith(boom)
	ok := notificationtest.NewRecorder()

	var failed atomic.Int32
	n := notification.New(
		notification.WithChannel("bad", failing),
		notification.WithChannel("good", ok),
		notification.WithListener(notification.ListenerFuncs{
			Failed: func(context.Context, notification.Delivery, error) { failed.Add(1) },
		}),
	)

	err := n.Send(t.Context(), user{}, welcome{channels: []string{"bad", "missing", "good"}})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap boom", err)
	}
	if !errors.Is(err, notification.ErrUnknownChannel) {
		t.Fatalf("err = %v, want it to wrap ErrUnknownChannel", err)
	}
	var ce *notification.ChannelError
	if !errors.As(err, &ce) || ce.Channel != "bad" {
		t.Fatalf("errors.As ChannelError = %+v", ce)
	}
	if len(ok.Deliveries()) != 1 {
		t.Fatal("a failing channel must not stop the others")
	}
	if failed.Load() != 1 {
		t.Fatalf("OnFailed calls = %d, want 1", failed.Load())
	}
}

func TestMiddlewareOrder(t *testing.T) {
	var order []string
	mw := func(name string) notification.Middleware {
		return func(next notification.Channel) notification.Channel {
			return notification.ChannelFunc(func(ctx context.Context, d notification.Delivery) error {
				order = append(order, name)
				return next.Send(ctx, d)
			})
		}
	}
	rec := notificationtest.NewRecorder()
	n := notification.New(
		notification.WithChannel("mail", rec),
		notification.WithMiddleware(mw("outer"), mw("inner")),
	)
	if err := n.Send(t.Context(), user{}, welcome{channels: []string{"mail"}}); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "outer" || order[1] != "inner" {
		t.Fatalf("order = %v, want [outer inner]", order)
	}
}

func TestRoutes(t *testing.T) {
	rec := notificationtest.NewRecorder()
	n := notification.New(notification.WithChannel("mail", routeChannel(rec)))
	to := notification.Route("mail", "ops@example.test").Route("slack", "https://hooks.example.test")

	if err := n.Send(t.Context(), to, welcome{channels: []string{"mail"}}); err != nil {
		t.Fatal(err)
	}
	if got := rec.Deliveries()[0].Route(); got != "ops@example.test" {
		t.Fatalf("route = %q", got)
	}
}

func TestSendAll(t *testing.T) {
	rec := notificationtest.NewRecorder()
	n := notification.New(notification.WithChannel("mail", rec))
	users := []user{{Email: "a@example.test"}, {Email: "b@example.test"}}

	if err := notification.SendAll(t.Context(), n, users, welcome{channels: []string{"mail"}}); err != nil {
		t.Fatal(err)
	}
	d := rec.Deliveries()
	if len(d) != 2 || d[0].ID == d[1].ID {
		t.Fatalf("want 2 deliveries with distinct ids, got %+v", d)
	}
}

func TestNilArguments(t *testing.T) {
	n := notification.New()
	if err := n.Send(t.Context(), nil, welcome{}); err == nil {
		t.Fatal("nil recipient should fail")
	}
	if err := n.Send(t.Context(), user{}, nil); err == nil {
		t.Fatal("nil notification should fail")
	}
}

func TestNewIDIsUUIDv4(t *testing.T) {
	id := notification.NewID()
	if len(id) != 36 || id[14] != '4' {
		t.Fatalf("NewID() = %q, want a v4 UUID", id)
	}
	if id == notification.NewID() {
		t.Fatal("ids should differ")
	}
}

func TestQueuedNotificationRunsInBackground(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var got []notification.Delivery
	ch := notification.ChannelFunc(func(_ context.Context, d notification.Delivery) error {
		<-release
		mu.Lock()
		defer mu.Unlock()
		got = append(got, d)
		return nil
	})

	q := notification.NewQueue(notification.WithWorkers(2))
	n := notification.New(notification.WithChannel("mail", ch), notification.WithQueue(q))

	ctx, cancel := context.WithCancel(t.Context())
	if err := n.Send(ctx, user{}, welcome{channels: []string{"mail"}, queued: true}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	cancel() // a finished request must not cancel queued deliveries
	close(release)

	if err := n.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(got))
	}
}

func TestSendNowBypassesQueue(t *testing.T) {
	var dispatched atomic.Int32
	q := notification.DispatcherFunc(func(context.Context, notification.Delivery, notification.SendFunc) error {
		dispatched.Add(1)
		return nil
	})
	rec := notificationtest.NewRecorder()
	n := notification.New(notification.WithChannel("mail", rec), notification.WithQueue(q))

	if err := n.SendNow(t.Context(), user{}, welcome{channels: []string{"mail"}, queued: true}); err != nil {
		t.Fatal(err)
	}
	if dispatched.Load() != 0 || len(rec.Deliveries()) != 1 {
		t.Fatalf("dispatched=%d delivered=%d, want 0 and 1", dispatched.Load(), len(rec.Deliveries()))
	}
}

func TestQueueRetriesUntilSuccess(t *testing.T) {
	var attempts []int
	var mu sync.Mutex
	ch := notification.ChannelFunc(func(_ context.Context, d notification.Delivery) error {
		mu.Lock()
		defer mu.Unlock()
		attempts = append(attempts, d.Attempt)
		if d.Attempt < 3 {
			return errors.New("temporary")
		}
		return nil
	})

	var final error
	q := notification.NewQueue(
		notification.WithBackoff(time.Millisecond),
		notification.WithErrorHandler(func(_ context.Context, _ notification.Delivery, err error) { final = err }),
	)
	n := notification.New(notification.WithChannel("mail", ch), notification.WithQueue(q))

	if err := n.Send(t.Context(), user{}, welcome{channels: []string{"mail"}, queued: true, tries: 5}); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 3 || attempts[2] != 3 {
		t.Fatalf("attempts = %v, want [1 2 3]", attempts)
	}
	if final != nil {
		t.Fatalf("error handler called with %v for a delivery that succeeded", final)
	}
}

func TestQueueGivesUp(t *testing.T) {
	cases := map[string]struct {
		err          error
		tries        int
		wantAttempts int32
	}{
		"exhausted": {err: errors.New("down"), tries: 2, wantAttempts: 2},
		"permanent": {err: notification.Permanent(errors.New("bad address")), tries: 5, wantAttempts: 1},
		"http 400":  {err: &notification.HTTPError{Service: "x", StatusCode: 400}, tries: 5, wantAttempts: 1},
		"http 503":  {err: &notification.HTTPError{Service: "x", StatusCode: 503}, tries: 3, wantAttempts: 3},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int32
			ch := notification.ChannelFunc(func(context.Context, notification.Delivery) error {
				attempts.Add(1)
				return tc.err
			})
			var final atomic.Value
			q := notification.NewQueue(
				notification.WithTries(tc.tries),
				notification.WithBackoff(time.Millisecond),
				notification.WithErrorHandler(func(_ context.Context, _ notification.Delivery, err error) { final.Store(err) }),
			)
			n := notification.New(notification.WithChannel("mail", ch), notification.WithQueue(q))
			if err := n.Send(t.Context(), user{}, welcome{channels: []string{"mail"}, queued: true}); err != nil {
				t.Fatal(err)
			}
			if err := q.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := attempts.Load(); got != tc.wantAttempts {
				t.Fatalf("attempts = %d, want %d", got, tc.wantAttempts)
			}
			if err, _ := final.Load().(error); !errors.Is(err, tc.err) {
				t.Fatalf("error handler got %v, want %v", err, tc.err)
			}
		})
	}
}

func TestQueueRecoversPanics(t *testing.T) {
	ch := notification.ChannelFunc(func(context.Context, notification.Delivery) error { panic("kaboom") })
	var final atomic.Value
	q := notification.NewQueue(notification.WithErrorHandler(func(_ context.Context, _ notification.Delivery, err error) {
		final.Store(err)
	}))
	n := notification.New(notification.WithChannel("mail", ch), notification.WithQueue(q))
	if err := n.Send(t.Context(), user{}, welcome{channels: []string{"mail"}, queued: true}); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err, _ := final.Load().(error); err == nil || !notification.IsPermanent(err) {
		t.Fatalf("final error = %v, want a permanent panic error", err)
	}
}

type delayed struct{ welcome }

func (delayed) Delay(string) time.Duration { return 20 * time.Millisecond }

func TestQueueDelay(t *testing.T) {
	var sentAt atomic.Int64
	ch := notification.ChannelFunc(func(context.Context, notification.Delivery) error {
		sentAt.Store(time.Now().UnixNano())
		return nil
	})
	q := notification.NewQueue()
	n := notification.New(notification.WithChannel("mail", ch), notification.WithQueue(q))

	start := time.Now()
	if err := n.Send(t.Context(), user{}, delayed{welcome{channels: []string{"mail"}, queued: true}}); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Duration(sentAt.Load() - start.UnixNano()); elapsed < 20*time.Millisecond {
		t.Fatalf("sent after %v, want at least 20ms", elapsed)
	}
}

func TestQueueCloseTimeoutCancelsInFlight(t *testing.T) {
	started := make(chan struct{})
	ch := notification.ChannelFunc(func(ctx context.Context, _ notification.Delivery) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	q := notification.NewQueue()
	n := notification.New(notification.WithChannel("mail", ch), notification.WithQueue(q))
	if err := n.Send(t.Context(), user{}, welcome{channels: []string{"mail"}, queued: true}); err != nil {
		t.Fatal(err)
	}
	<-started

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := q.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want DeadlineExceeded", err)
	}
	if err := q.Dispatch(t.Context(), notification.Delivery{}, nil); !errors.Is(err, notification.ErrQueueClosed) {
		t.Fatalf("Dispatch after Close = %v, want ErrQueueClosed", err)
	}
}

func TestIsPermanent(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil":         {nil, false},
		"plain":       {errors.New("x"), false},
		"permanent":   {notification.Permanent(errors.New("x")), true},
		"no route":    {notification.ErrNoRoute, true},
		"unsupported": {notification.ErrUnsupported, true},
		"429":         {&notification.HTTPError{StatusCode: 429}, false},
		"401":         {&notification.HTTPError{StatusCode: 401}, true},
		"500":         {&notification.HTTPError{StatusCode: 500}, false},
	}
	for name, tc := range cases {
		if got := notification.IsPermanent(tc.err); got != tc.want {
			t.Errorf("%s: IsPermanent = %v, want %v", name, got, tc.want)
		}
	}
	if notification.Permanent(nil) != nil {
		t.Error("Permanent(nil) should be nil")
	}
}
