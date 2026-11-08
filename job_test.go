package notification_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/notificationtest"
)

// member is an Identifiable recipient, reloaded by the worker.
type member struct {
	ID    string
	Email string
}

func (m member) RouteNotificationFor(channel string, _ notification.Notification) string {
	if channel == "mail" {
		return m.Email
	}
	return ""
}

func (m member) NotifiableKey() (kind, id string) { return "member", m.ID }

// orderShipped is a serializable, queued notification with its own retry
// settings.
type orderShipped struct {
	OrderID  int
	Channels []string
}

func (o orderShipped) Via(notification.Notifiable) []string { return o.Channels }
func (orderShipped) ShouldQueue() bool                      { return true }
func (orderShipped) Tries() int                             { return 5 }
func (orderShipped) Backoff() []time.Duration               { return []time.Duration{time.Minute} }
func (orderShipped) Delay(channel string) time.Duration {
	if channel == "sms" {
		return time.Hour
	}
	return 0
}

func newCodec(t *testing.T, members map[string]member) *notification.Codec {
	t.Helper()
	c := notification.NewCodec()
	notification.RegisterNotification[orderShipped](c, "order.shipped")
	notification.RegisterNotifiable[user](c, "user")
	c.Resolve("member", func(_ context.Context, id string) (notification.Notifiable, error) {
		m, ok := members[id]
		if !ok {
			return nil, notification.Permanent(errors.New("member not found"))
		}
		return m, nil
	})
	return c
}

func TestJobDispatcherEndToEnd(t *testing.T) {
	codec := newCodec(t, nil)

	// The "external queue": only bytes and settings cross it.
	var jobs []notification.Job
	producer := notification.New(
		notification.WithChannel("mail", notification.ChannelFunc(func(context.Context, notification.Delivery) error {
			t.Fatal("producer must not deliver")
			return nil
		})),
		notification.WithChannel("sms", notification.ChannelFunc(func(context.Context, notification.Delivery) error {
			t.Fatal("producer must not deliver")
			return nil
		})),
		notification.WithQueue(notification.NewJobDispatcher(codec, func(_ context.Context, job notification.Job) error {
			jobs = append(jobs, job)
			return nil
		})),
	)

	to := user{Email: "ada@example.com", Phone: "+15550100"}
	if err := producer.Send(t.Context(), to, orderShipped{OrderID: 7, Channels: []string{"mail", "sms"}}); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("enqueued %d jobs, want one per channel", len(jobs))
	}
	if jobs[0].ID == "" || jobs[0].ID != jobs[1].ID {
		t.Fatalf("jobs must share the delivery id: %q, %q", jobs[0].ID, jobs[1].ID)
	}
	for _, j := range jobs {
		if j.Tries != 5 || !slices.Equal(j.Backoff, []time.Duration{time.Minute}) {
			t.Errorf("%s: tries %d backoff %v not carried over", j.Channel, j.Tries, j.Backoff)
		}
		want := time.Duration(0)
		if j.Channel == "sms" {
			want = time.Hour
		}
		if j.Delay != want {
			t.Errorf("%s: delay %v, want %v", j.Channel, j.Delay, want)
		}
	}

	// The worker is a separate Notifier that sees only the payloads.
	mail, sms := notificationtest.NewRecorder(), notificationtest.NewRecorder()
	worker := notification.New(
		notification.WithChannel("mail", routeChannel(mail)),
		notification.WithChannel("sms", routeChannel(sms)),
	)
	for i, j := range jobs {
		if err := worker.HandleJob(t.Context(), codec, j.Payload, i+2); err != nil {
			t.Fatal(err)
		}
	}

	for name, rec := range map[string]*notificationtest.Recorder{"mail": mail, "sms": sms} {
		ds := rec.Deliveries()
		if len(ds) != 1 {
			t.Fatalf("%s delivered %d times, want exactly once", name, len(ds))
		}
		d := ds[0]
		if d.ID != jobs[0].ID {
			t.Errorf("%s: id %q, want the original %q", name, d.ID, jobs[0].ID)
		}
		if d.To != to {
			t.Errorf("%s: recipient %#v, want %#v", name, d.To, to)
		}
		if n, ok := d.Notification.(orderShipped); !ok || n.OrderID != 7 {
			t.Errorf("%s: notification %#v not rebuilt", name, d.Notification)
		}
	}
	if a := mail.Deliveries()[0].Attempt; a != 2 {
		t.Errorf("attempt %d, want the queue's 2", a)
	}
}

func TestIdentifiableRecipientTravelsByReference(t *testing.T) {
	current := map[string]member{"42": {ID: "42", Email: "new@example.com"}}
	codec := newCodec(t, current)

	payload, err := codec.Encode(notification.Delivery{
		ID: "d1", Channel: "mail",
		To:           member{ID: "42", Email: "old@example.com"},
		Notification: orderShipped{OrderID: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("example.com")) {
		t.Fatalf("payload holds the recipient's data: %s", payload)
	}

	d, err := codec.Decode(t.Context(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.To.(member).Email; got != "new@example.com" {
		t.Fatalf("recipient not reloaded: %q", got)
	}

	// A recipient that no longer exists is dropped, not retried.
	delete(current, "42")
	_, err = codec.Decode(t.Context(), payload)
	if err == nil || !notification.IsPermanent(err) {
		t.Fatalf("missing recipient: %v, want permanent", err)
	}
}

func TestCodecRoundTripsRoutesAndPointers(t *testing.T) {
	c := notification.NewCodec()
	notification.RegisterNotification[*orderShipped](c, "order.shipped")

	to := notification.Route("mail", "ops@acme.test")
	payload, err := c.Encode(notification.Delivery{ID: "d1", Channel: "mail", To: to, Notification: &orderShipped{OrderID: 3}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.Decode(t.Context(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "d1" || d.Channel != "mail" || d.Attempt != 1 {
		t.Fatalf("delivery fields: %+v", d)
	}
	if d.Route() != "ops@acme.test" {
		t.Fatalf("routes not rebuilt: %#v", d.To)
	}
	if n, ok := d.Notification.(*orderShipped); !ok || n.OrderID != 3 {
		t.Fatalf("pointer notification not rebuilt: %#v", d.Notification)
	}
}

func TestEncodeRejectsUnregisteredTypes(t *testing.T) {
	c := notification.NewCodec()
	_, err := c.Encode(notification.Delivery{To: user{}, Notification: orderShipped{}})
	if err == nil || !strings.Contains(err.Error(), "RegisterNotification") {
		t.Fatalf("unregistered notification: %v", err)
	}

	notification.RegisterNotification[orderShipped](c, "order.shipped")
	_, err = c.Encode(notification.Delivery{To: user{}, Notification: orderShipped{}})
	if err == nil || !strings.Contains(err.Error(), "RegisterNotifiable") {
		t.Fatalf("unregistered recipient: %v", err)
	}

	// Identifiable without a resolver falls back to registration, and fails
	// without one.
	_, err = c.Encode(notification.Delivery{To: member{ID: "1"}, Notification: orderShipped{}})
	if err == nil {
		t.Fatal("identifiable recipient without resolver or registration encoded")
	}
}

func TestJobDispatcherReportsEncodeFailureAtSend(t *testing.T) {
	enqueued := false
	n := notification.New(
		notification.WithChannel("mail", notificationtest.NewRecorder()),
		notification.WithQueue(notification.NewJobDispatcher(notification.NewCodec(), func(context.Context, notification.Job) error {
			enqueued = true
			return nil
		})),
	)
	err := n.Send(t.Context(), user{Email: "a@b.c"}, orderShipped{Channels: []string{"mail"}})
	var ce *notification.ChannelError
	if !errors.As(err, &ce) || ce.Channel != "mail" {
		t.Fatalf("got %v, want a ChannelError for mail", err)
	}
	if enqueued {
		t.Fatal("enqueued a delivery that could not be encoded")
	}
}

func TestDecodeErrorsArePermanent(t *testing.T) {
	c := notification.NewCodec()
	unknown, _ := json.Marshal(map[string]any{
		"v": 1, "id": "x", "channel": "mail",
		"notification": map[string]any{"type": "nope"},
		"notifiable":   map[string]any{"type": "routes", "data": map[string]string{}},
	})
	for name, payload := range map[string][]byte{
		"garbage":      []byte("{not json"),
		"version":      []byte(`{"v":99}`),
		"unknown type": unknown,
	} {
		_, err := c.Decode(t.Context(), payload)
		if err == nil || !notification.IsPermanent(err) {
			t.Errorf("%s: %v, want permanent", name, err)
		}
		if err := notification.New().HandleJob(t.Context(), c, payload, 1); !notification.IsPermanent(err) {
			t.Errorf("%s via HandleJob: %v, want permanent", name, err)
		}
	}
}

func TestResolverErrorsAreRetryable(t *testing.T) {
	c := notification.NewCodec()
	notification.RegisterNotification[orderShipped](c, "order.shipped")
	down := errors.New("database down")
	c.Resolve("member", func(context.Context, string) (notification.Notifiable, error) { return nil, down })

	payload, err := c.Encode(notification.Delivery{ID: "d", Channel: "mail", To: member{ID: "1"}, Notification: orderShipped{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Decode(t.Context(), payload)
	if !errors.Is(err, down) || notification.IsPermanent(err) {
		t.Fatalf("got %v, want the retryable resolver error", err)
	}
}

func TestDeliverRunsOneChannel(t *testing.T) {
	mail, sms := notificationtest.NewRecorder(), notificationtest.NewRecorder()
	var skipped []error
	n := notification.New(
		notification.WithChannel("mail", routeChannel(mail)),
		notification.WithChannel("sms", routeChannel(sms)),
		notification.WithListener(notification.ListenerFuncs{
			Skipped: func(_ context.Context, _ notification.Delivery, reason error) { skipped = append(skipped, reason) },
		}),
	)
	to := user{Email: "a@b.c", Phone: "+1"}
	w := welcome{channels: []string{"mail", "sms"}}

	if err := n.Deliver(t.Context(), notification.Delivery{ID: "keep", Channel: "mail", To: to, Notification: w, Attempt: 3}); err != nil {
		t.Fatal(err)
	}
	if len(sms.Deliveries()) != 0 {
		t.Fatal("Deliver ran a channel other than d.Channel")
	}
	d := mail.Deliveries()[0]
	if d.ID != "keep" || d.Attempt != 3 {
		t.Fatalf("id/attempt not kept: %+v", d)
	}

	// ShouldSend and missing routes skip, returning nil.
	deny := welcome{allow: func(string) bool { return false }}
	if err := n.Deliver(t.Context(), notification.Delivery{Channel: "mail", To: to, Notification: deny}); err != nil {
		t.Fatal(err)
	}
	if err := n.Deliver(t.Context(), notification.Delivery{Channel: "sms", To: user{}, Notification: w}); err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 2 || !errors.Is(skipped[0], notification.ErrSkipped) || !errors.Is(skipped[1], notification.ErrNoRoute) {
		t.Fatalf("skips: %v", skipped)
	}

	err := n.Deliver(t.Context(), notification.Delivery{Channel: "fax", To: to, Notification: w})
	if !errors.Is(err, notification.ErrUnknownChannel) || !notification.IsPermanent(err) {
		t.Fatalf("unknown channel: %v", err)
	}
	if err := n.Deliver(t.Context(), notification.Delivery{Channel: "mail", Notification: w}); !notification.IsPermanent(err) {
		t.Fatalf("nil recipient: %v", err)
	}
}

func TestRegisterPanicsOnConflicts(t *testing.T) {
	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: did not panic", name)
			}
		}()
		fn()
	}
	c := notification.NewCodec()
	notification.RegisterNotification[orderShipped](c, "order.shipped")
	mustPanic("same name", func() { notification.RegisterNotification[welcome](c, "order.shipped") })
	mustPanic("same type", func() { notification.RegisterNotification[orderShipped](c, "other") })
	mustPanic("empty name", func() { notification.RegisterNotification[welcome](c, "") })
	mustPanic("routes taken", func() { notification.RegisterNotifiable[user](c, "routes") })
	c.Resolve("member", func(context.Context, string) (notification.Notifiable, error) { return nil, nil })
	mustPanic("resolver twice", func() {
		c.Resolve("member", func(context.Context, string) (notification.Notifiable, error) { return nil, nil })
	})
}
