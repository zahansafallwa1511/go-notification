package database

import (
	"context"
	"time"

	notification "github.com/zahansafallwa1511/go-notification"
)

// Inbox reads and updates the records of one recipient. Every operation is
// scoped to that recipient, so ids belonging to someone else are never
// touched. Create one per request with [InboxFor] or [NewInbox]; it is cheap.
type Inbox struct {
	store          Store
	notifiableType string
	notifiableID   string
	now            func() time.Time
}

// NewInbox returns the inbox of the recipient identified by notifiableType and
// notifiableID, the values the [Channel] stored. An empty notifiableType
// matches records of any type with that id.
func NewInbox(store Store, notifiableType, notifiableID string, opts ...Option) *Inbox {
	c := newConfig(opts)
	return &Inbox{store: store, notifiableType: notifiableType, notifiableID: notifiableID, now: c.now}
}

// InboxFor returns the inbox of to, using the same type and id the [Channel]
// stores: [NotifiableTypeOf] and to's route for [ChannelName]. The route is
// requested with a nil notification.
func InboxFor(store Store, to notification.Notifiable, opts ...Option) *Inbox {
	return NewInbox(store, NotifiableTypeOf(to), to.RouteNotificationFor(ChannelName, nil), opts...)
}

// query returns a Query scoped to the inbox's recipient, or ErrNoRoute when
// the inbox has no recipient id.
func (b *Inbox) query() (Query, error) {
	if b.notifiableID == "" {
		return Query{}, notification.ErrNoRoute
	}
	return Query{NotifiableType: b.notifiableType, NotifiableID: b.notifiableID}, nil
}

// All returns the recipient's records, newest first. A zero limit means no
// limit.
func (b *Inbox) All(ctx context.Context, limit, offset int) ([]Record, error) {
	q, err := b.query()
	if err != nil {
		return nil, err
	}
	q.Limit, q.Offset = limit, offset
	return b.store.List(ctx, q)
}

// Unread returns the recipient's unread records, newest first. A zero limit
// means no limit.
func (b *Inbox) Unread(ctx context.Context, limit int) ([]Record, error) {
	q, err := b.query()
	if err != nil {
		return nil, err
	}
	q.State, q.Limit = Unread, limit
	return b.store.List(ctx, q)
}

// UnreadCount returns how many unread records the recipient has.
func (b *Inbox) UnreadCount(ctx context.Context) (int, error) {
	q, err := b.query()
	if err != nil {
		return 0, err
	}
	q.State = Unread
	return b.store.Count(ctx, q)
}

// MarkAsRead marks the given records as read and returns how many changed.
// With no ids it does nothing; use [Inbox.MarkAllAsRead] for that.
func (b *Inbox) MarkAsRead(ctx context.Context, ids ...string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	q, err := b.query()
	if err != nil {
		return 0, err
	}
	q.IDs = ids
	return b.store.MarkAsRead(ctx, q, b.now().UTC())
}

// MarkAllAsRead marks every unread record of the recipient as read.
func (b *Inbox) MarkAllAsRead(ctx context.Context) (int64, error) {
	q, err := b.query()
	if err != nil {
		return 0, err
	}
	return b.store.MarkAsRead(ctx, q, b.now().UTC())
}

// MarkAsUnread marks the given records as unread again. With no ids it does
// nothing.
func (b *Inbox) MarkAsUnread(ctx context.Context, ids ...string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	q, err := b.query()
	if err != nil {
		return 0, err
	}
	q.IDs = ids
	return b.store.MarkAsUnread(ctx, q)
}

// Delete removes the given records. With no ids it does nothing; use
// [Inbox.Clear] to remove everything.
func (b *Inbox) Delete(ctx context.Context, ids ...string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	q, err := b.query()
	if err != nil {
		return 0, err
	}
	q.IDs = ids
	return b.store.Delete(ctx, q)
}

// Clear removes every record of the recipient.
func (b *Inbox) Clear(ctx context.Context) (int64, error) {
	q, err := b.query()
	if err != nil {
		return 0, err
	}
	return b.store.Delete(ctx, q)
}
