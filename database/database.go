// Package database provides an in-app inbox channel: notifications are stored
// as records your application lists, counts and marks as read — the bell icon
// in the corner of the page.
//
// The package is split like the rest of the library:
//
//   - [Store] is the small interface every storage backend implements.
//     [MemoryStore] ships here; sqlstore persists to any database/sql database.
//   - [Channel] is the notification.Channel that writes records to a Store.
//   - [Inbox] is what your handlers call to read one recipient's records.
//
// A notification supports the channel by implementing [Notification]:
//
//	func (n InvoicePaid) ToDatabase(notification.Notifiable) (any, error) {
//		return map[string]any{"invoice_id": n.Invoice.ID, "amount": n.Invoice.Total}, nil
//	}
//
// The recipient's id on the channel is its route, so a user returns its id
// for the "database" channel:
//
//	func (u User) RouteNotificationFor(channel string, _ notification.Notification) string {
//		switch channel {
//		case "database":
//			return strconv.FormatInt(u.ID, 10)
//		case "mail":
//			return u.Email
//		}
//		return ""
//	}
//
// Wire it up and read it back:
//
//	store := database.NewMemoryStore()
//	notifier := notification.New(notification.WithChannel("database", database.NewChannel(store)))
//
//	inbox := database.InboxFor(store, user)
//	unread, err := inbox.Unread(ctx, 20)
//	count, err := inbox.UnreadCount(ctx)
//	_, err = inbox.MarkAsRead(ctx, unread[0].ID)
//
// Each record's ID is the [notification.Delivery] ID, and stores must ignore a
// second insert with an ID they already hold, so a delivery retried by a queue
// never shows up twice.
package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	notification "github.com/zahansafallwa1511/go-notification"
)

// ChannelName is the channel name [InboxFor] asks a recipient to route. Register
// the channel under this name unless you have a reason not to.
const ChannelName = "database"

// ErrUnscopedQuery is returned by [Store] methods that change or delete records
// when the [Query] names no IDs, no notifiable and no Before cut-off — a query
// that would otherwise touch every row in the table.
var ErrUnscopedQuery = errors.New("database: refusing to modify records with an unscoped query")

// Notification is implemented by notifications that can be stored in an inbox.
// ToDatabase returns the payload to keep; it is JSON-encoded into
// [Record.Data].
type Notification interface {
	ToDatabase(to notification.Notifiable) (any, error)
}

// Typer is an optional [Notification] interface naming the stored
// [Record.Type]. Without it the type is the Go type name, such as
// "billing.InvoicePaid". Implement it to keep stored types stable when you
// rename or move the Go type.
type Typer interface {
	DatabaseType() string
}

// Typed is an optional [notification.Notifiable] interface naming the stored
// [Record.NotifiableType], such as "user" or "team". Without it the type is the
// recipient's Go type name.
type Typed interface {
	NotifiableType() string
}

// Record is one stored notification.
type Record struct {
	// ID is the delivery id; unique per notification and recipient.
	ID string
	// Type identifies the kind of notification; see [Typer].
	Type string
	// NotifiableType and NotifiableID identify the recipient.
	NotifiableType string
	NotifiableID   string
	// Data is the JSON-encoded payload returned by ToDatabase.
	Data json.RawMessage
	// ReadAt is when the record was marked as read, or nil while unread.
	ReadAt *time.Time
	// CreatedAt is when the record was stored.
	CreatedAt time.Time
}

// Read reports whether the record has been marked as read.
func (r Record) Read() bool { return r.ReadAt != nil }

// Decode unmarshals the record's payload into v.
func (r Record) Decode(v any) error {
	if err := json.Unmarshal(r.Data, v); err != nil {
		return fmt.Errorf("database: decoding record %s: %w", r.ID, err)
	}
	return nil
}

// State filters records by whether they have been read.
type State int

// States a [Query] can filter by.
const (
	// All matches read and unread records. It is the zero value.
	All State = iota
	// Unread matches records that have not been read.
	Unread
	// Read matches records that have been read.
	Read
)

// Query selects records. Zero fields do not filter; every non-zero field must
// match.
type Query struct {
	// IDs restricts the query to these record ids.
	IDs []string
	// NotifiableType and NotifiableID restrict it to one recipient.
	NotifiableType string
	NotifiableID   string
	// Types restricts it to these notification types.
	Types []string
	// State filters by read state.
	State State
	// Before restricts it to records created strictly before this time,
	// which is how old records are pruned.
	Before time.Time
	// Limit and Offset page through results. They apply to List only; zero
	// Limit means no limit.
	Limit  int
	Offset int
}

// Scoped reports whether q narrows the rows it touches by IDs, NotifiableID or
// Before. Stores refuse to modify records with an unscoped query and return
// [ErrUnscopedQuery].
func (q Query) Scoped() bool {
	return len(q.IDs) > 0 || q.NotifiableID != "" || !q.Before.IsZero()
}

// Matches reports whether r satisfies every filter of q, ignoring Limit and
// Offset. Stores that filter in memory can use it.
func (q Query) Matches(r Record) bool {
	if len(q.IDs) > 0 && !contains(q.IDs, r.ID) {
		return false
	}
	if q.NotifiableType != "" && q.NotifiableType != r.NotifiableType {
		return false
	}
	if q.NotifiableID != "" && q.NotifiableID != r.NotifiableID {
		return false
	}
	if len(q.Types) > 0 && !contains(q.Types, r.Type) {
		return false
	}
	switch q.State {
	case Unread:
		if r.ReadAt != nil {
			return false
		}
	case Read:
		if r.ReadAt == nil {
			return false
		}
	}
	if !q.Before.IsZero() && !r.CreatedAt.Before(q.Before) {
		return false
	}
	return true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Store is the contract implemented by inbox storage backends.
//
// Implementations must be safe for concurrent use. List returns records newest
// first. MarkAsRead changes only unread records and MarkAsUnread only read
// ones; both, and Delete, return how many records they changed and must return
// [ErrUnscopedQuery] for a query that is not [Query.Scoped].
//
// Implement this interface to add a backend, then prove it with
// [github.com/zahansafallwa1511/go-notification/database/storetest].Run, the
// same conformance suite the built-in stores run.
type Store interface {
	// Insert stores r. Inserting an ID the store already holds is a no-op
	// that returns nil, which makes retried deliveries idempotent.
	Insert(ctx context.Context, r Record) error

	// List returns the records matching q, newest first, paged by q.Limit
	// and q.Offset.
	List(ctx context.Context, q Query) ([]Record, error)

	// Count returns how many records match q, ignoring Limit and Offset.
	Count(ctx context.Context, q Query) (int, error)

	// MarkAsRead sets ReadAt to at on the unread records matching q.
	MarkAsRead(ctx context.Context, q Query, at time.Time) (int64, error)

	// MarkAsUnread clears ReadAt on the read records matching q.
	MarkAsUnread(ctx context.Context, q Query) (int64, error)

	// Delete removes the records matching q.
	Delete(ctx context.Context, q Query) (int64, error)
}

// Option configures a [Channel] or an [Inbox].
type Option func(*config)

type config struct {
	now func() time.Time
}

// WithClock replaces the clock used for CreatedAt and ReadAt. The default is
// time.Now.
func WithClock(now func() time.Time) Option {
	return func(c *config) { c.now = now }
}

func newConfig(opts []Option) config {
	c := config{now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(&c)
		}
	}
	return c
}

// Channel is a [notification.Channel] that stores notifications in a [Store].
type Channel struct {
	store Store
	now   func() time.Time
}

var _ notification.Channel = (*Channel)(nil)

// NewChannel returns a channel writing to store.
//
//	notification.WithChannel("database", database.NewChannel(store))
func NewChannel(store Store, opts ...Option) *Channel {
	c := newConfig(opts)
	return &Channel{store: store, now: c.now}
}

// Send implements [notification.Channel]. The recipient's route on the channel
// becomes [Record.NotifiableID]; an empty route returns
// [notification.ErrNoRoute].
func (c *Channel) Send(ctx context.Context, d notification.Delivery) error {
	id := d.Route()
	if id == "" {
		return notification.ErrNoRoute
	}
	n, ok := d.Notification.(Notification)
	if !ok {
		return fmt.Errorf("%w: %T does not implement database.Notification", notification.ErrUnsupported, d.Notification)
	}
	payload, err := n.ToDatabase(d.To)
	if err != nil {
		return notification.Permanent(fmt.Errorf("database: ToDatabase: %w", err))
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return notification.Permanent(fmt.Errorf("database: encoding payload: %w", err))
	}

	recordID := d.ID
	if recordID == "" {
		recordID = notification.NewID()
	}
	return c.store.Insert(ctx, Record{
		ID:             recordID,
		Type:           TypeOf(d.Notification),
		NotifiableType: NotifiableTypeOf(d.To),
		NotifiableID:   id,
		Data:           data,
		CreatedAt:      c.now().UTC(),
	})
}

// TypeOf returns the [Record.Type] stored for n: its DatabaseType when it
// implements [Typer], otherwise its Go type name.
func TypeOf(n notification.Notification) string {
	if t, ok := n.(Typer); ok {
		return t.DatabaseType()
	}
	return goTypeName(n)
}

// NotifiableTypeOf returns the [Record.NotifiableType] stored for to: its
// NotifiableType when it implements [Typed], otherwise its Go type name.
func NotifiableTypeOf(to notification.Notifiable) string {
	if t, ok := to.(Typed); ok {
		return t.NotifiableType()
	}
	return goTypeName(to)
}

// goTypeName returns the package-qualified name of v's type, with pointers
// dereferenced: "billing.InvoicePaid" for both InvoicePaid and *InvoicePaid.
func goTypeName(v any) string {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return ""
	}
	return t.String()
}
