package database_test

import (
	"context"
	"errors"
	"testing"
	"time"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/database"
	"github.com/zahansafallwa1511/go-notification/database/storetest"
)

func TestMemoryStore(t *testing.T) {
	storetest.Run(t, func(*testing.T) database.Store { return database.NewMemoryStore() })
}

type member struct{ ID string }

func (m member) RouteNotificationFor(channel string, _ notification.Notification) string {
	if channel == "database" {
		return m.ID
	}
	return ""
}

type team struct{ ID string }

func (t *team) RouteNotificationFor(string, notification.Notification) string { return t.ID }
func (t *team) NotifiableType() string                                        { return "team" }

type invoicePaid struct{ Amount int }

func (invoicePaid) Via(notification.Notifiable) []string { return []string{"database"} }
func (n invoicePaid) ToDatabase(notification.Notifiable) (any, error) {
	return map[string]int{"amount": n.Amount}, nil
}

type renamed struct{ invoicePaid }

func (renamed) DatabaseType() string { return "invoice.paid" }

type mailOnly struct{}

func (mailOnly) Via(notification.Notifiable) []string { return []string{"database"} }

type broken struct{}

func (broken) Via(notification.Notifiable) []string            { return []string{"database"} }
func (broken) ToDatabase(notification.Notifiable) (any, error) { return nil, errors.New("no data") }

var now = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func clock() time.Time { return now }

func TestChannelStoresRecord(t *testing.T) {
	store := database.NewMemoryStore()
	ch := database.NewChannel(store, database.WithClock(clock))

	d := notification.Delivery{ID: "id-1", Channel: "database", To: member{ID: "42"}, Notification: invoicePaid{Amount: 10}, Attempt: 1}
	if err := ch.Send(t.Context(), d); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, _ := store.List(t.Context(), database.Query{})
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	r := got[0]
	if r.ID != "id-1" || r.NotifiableID != "42" || r.NotifiableType != "database_test.member" || r.Type != "database_test.invoicePaid" {
		t.Fatalf("record = %+v", r)
	}
	if !r.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt = %v, want %v", r.CreatedAt, now)
	}
	var data struct{ Amount int }
	if err := r.Decode(&data); err != nil || data.Amount != 10 {
		t.Fatalf("Decode = %+v, %v", data, err)
	}
}

func TestChannelTypeOverrides(t *testing.T) {
	store := database.NewMemoryStore()
	ch := database.NewChannel(store)

	d := notification.Delivery{ID: "id-1", Channel: "database", To: &team{ID: "t1"}, Notification: renamed{}}
	if err := ch.Send(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	got, _ := store.List(t.Context(), database.Query{})
	if got[0].Type != "invoice.paid" || got[0].NotifiableType != "team" {
		t.Fatalf("types = %q, %q", got[0].Type, got[0].NotifiableType)
	}
}

func TestTypeNamesDereferencePointers(t *testing.T) {
	if got := database.TypeOf(&invoicePaid{}); got != "database_test.invoicePaid" {
		t.Fatalf("TypeOf(pointer) = %q", got)
	}
	if got := database.NotifiableTypeOf(&member{}); got != "database_test.member" {
		t.Fatalf("NotifiableTypeOf(pointer) = %q", got)
	}
}

func TestChannelErrors(t *testing.T) {
	ch := database.NewChannel(database.NewMemoryStore())
	ctx := t.Context()

	err := ch.Send(ctx, notification.Delivery{ID: "x", Channel: "database", To: member{}, Notification: invoicePaid{}})
	if !errors.Is(err, notification.ErrNoRoute) {
		t.Fatalf("empty route: %v, want ErrNoRoute", err)
	}
	err = ch.Send(ctx, notification.Delivery{ID: "x", Channel: "database", To: member{ID: "1"}, Notification: mailOnly{}})
	if !errors.Is(err, notification.ErrUnsupported) {
		t.Fatalf("unsupported: %v, want ErrUnsupported", err)
	}
	err = ch.Send(ctx, notification.Delivery{ID: "x", Channel: "database", To: member{ID: "1"}, Notification: broken{}})
	if err == nil || !notification.IsPermanent(err) {
		t.Fatalf("ToDatabase failure: %v, want a permanent error", err)
	}
}

func TestRedeliveryIsIdempotent(t *testing.T) {
	store := database.NewMemoryStore()
	ch := database.NewChannel(store)
	d := notification.Delivery{ID: "same", Channel: "database", To: member{ID: "1"}, Notification: invoicePaid{}}

	for attempt := 1; attempt <= 3; attempt++ {
		d.Attempt = attempt
		if err := ch.Send(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := store.Count(t.Context(), database.Query{}); n != 1 {
		t.Fatalf("records after 3 attempts = %d, want 1", n)
	}
}

func TestNotifierAndInbox(t *testing.T) {
	store := database.NewMemoryStore()
	n := notification.New(notification.WithChannel("database", database.NewChannel(store)))
	alice, bob := member{ID: "alice"}, member{ID: "bob"}

	for i := range 3 {
		if err := n.Send(t.Context(), alice, invoicePaid{Amount: i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.Send(t.Context(), bob, invoicePaid{Amount: 9}); err != nil {
		t.Fatal(err)
	}

	inbox := database.InboxFor(store, alice)
	unread, err := inbox.Unread(t.Context(), 0)
	if err != nil || len(unread) != 3 {
		t.Fatalf("Unread = %d, %v, want 3", len(unread), err)
	}
	if c, _ := inbox.UnreadCount(t.Context()); c != 3 {
		t.Fatalf("UnreadCount = %d, want 3", c)
	}

	bobs, _ := database.InboxFor(store, bob).All(t.Context(), 0, 0)
	if changed, _ := inbox.MarkAsRead(t.Context(), unread[0].ID, bobs[0].ID); changed != 1 {
		t.Fatalf("MarkAsRead changed %d, want 1 (bob's record must be untouched)", changed)
	}
	if c, _ := inbox.UnreadCount(t.Context()); c != 2 {
		t.Fatalf("UnreadCount = %d, want 2", c)
	}
	if changed, _ := inbox.MarkAsRead(t.Context()); changed != 0 {
		t.Fatalf("MarkAsRead() with no ids changed %d, want 0", changed)
	}
	if changed, _ := inbox.MarkAllAsRead(t.Context()); changed != 2 {
		t.Fatalf("MarkAllAsRead changed %d, want 2", changed)
	}
	if changed, _ := inbox.MarkAsUnread(t.Context(), unread[1].ID); changed != 1 {
		t.Fatalf("MarkAsUnread changed %d, want 1", changed)
	}
	if deleted, _ := inbox.Delete(t.Context(), unread[2].ID); deleted != 1 {
		t.Fatalf("Delete = %d, want 1", deleted)
	}
	if cleared, _ := inbox.Clear(t.Context()); cleared != 2 {
		t.Fatalf("Clear = %d, want 2", cleared)
	}
	if c, _ := store.Count(t.Context(), database.Query{}); c != 1 {
		t.Fatalf("records left = %d, want bob's 1", c)
	}
}

func TestInboxWithoutRecipient(t *testing.T) {
	inbox := database.NewInbox(database.NewMemoryStore(), "user", "")
	ctx := context.Background()
	if _, err := inbox.All(ctx, 0, 0); !errors.Is(err, notification.ErrNoRoute) {
		t.Fatalf("All = %v, want ErrNoRoute", err)
	}
	if _, err := inbox.Clear(ctx); !errors.Is(err, notification.ErrNoRoute) {
		t.Fatalf("Clear = %v, want ErrNoRoute", err)
	}
}
