package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// Identifiable is an optional [Notifiable] interface for recipients that
// should travel through a durable queue by reference. A queued delivery to an
// Identifiable recipient stores only its kind and id, and the worker reloads
// it with the function registered by [Codec.Resolve] for that kind.
//
// Prefer it for your own users and accounts: the worker sees current data —
// a changed address, a deleted account — and the queue never holds the
// recipient's personal data.
//
//	func (u User) NotifiableKey() (kind, id string) { return "user", strconv.FormatInt(u.ID, 10) }
type Identifiable interface {
	NotifiableKey() (kind, id string)
}

// ResolveFunc loads the recipient of the given kind with the given id, for
// [Codec.Resolve]. When the recipient no longer exists it should return an
// error wrapped with [Permanent], so the queue drops the job instead of
// retrying it.
type ResolveFunc func(ctx context.Context, id string) (Notifiable, error)

// Codec serializes deliveries for durable job queues, and rebuilds them in the
// worker. Go cannot rebuild a value from a type name on its own, so every
// notification type that may be queued is registered under a stable name with
// [RegisterNotification], and every recipient type either with
// [RegisterNotifiable] (stored as JSON) or, for [Identifiable] recipients,
// with [Codec.Resolve] (stored by reference).
//
//	codec := notification.NewCodec()
//	notification.RegisterNotification[InvoicePaid](codec, "invoice.paid")
//	codec.Resolve("user", func(ctx context.Context, id string) (notification.Notifiable, error) {
//		return users.Find(ctx, id)
//	})
//
// Producer and worker must register the same names; build the codec in one
// function both call. [Routes] is registered as "routes" already.
//
// Notifications and JSON-stored recipients are encoded with encoding/json, so
// only their exported fields survive, unless they implement json.Marshaler and
// json.Unmarshaler. Names are stored in the payload, so keep them stable when
// you rename or move a Go type.
//
// Register everything before the codec is used. A Codec is safe for
// concurrent use.
type Codec struct {
	mu              sync.RWMutex
	notifications   map[string]func([]byte) (Notification, error)
	notificationTyp map[reflect.Type]string
	notifiables     map[string]func([]byte) (Notifiable, error)
	notifiableTyp   map[reflect.Type]string
	resolvers       map[string]ResolveFunc
}

// payloadVersion is the format version written into every payload.
const payloadVersion = 1

type wirePayload struct {
	V            int       `json:"v"`
	ID           string    `json:"id"`
	Channel      string    `json:"channel"`
	Notification wireValue `json:"notification"`
	Notifiable   wireValue `json:"notifiable"`
}

type wireValue struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
	// Ref is the id of an Identifiable recipient, stored instead of Data.
	Ref string `json:"ref,omitempty"`
}

// NewCodec returns a codec with [Routes] registered as "routes".
func NewCodec() *Codec {
	c := &Codec{
		notifications:   map[string]func([]byte) (Notification, error){},
		notificationTyp: map[reflect.Type]string{},
		notifiables:     map[string]func([]byte) (Notifiable, error){},
		notifiableTyp:   map[reflect.Type]string{},
		resolvers:       map[string]ResolveFunc{},
	}
	RegisterNotifiable[Routes](c, "routes")
	return c
}

// RegisterNotification registers notification type T under name. Register
// the type exactly as it is sent: InvoicePaid and *InvoicePaid are different
// types. It panics if name is empty or already taken, or if T is already
// registered under another name.
func RegisterNotification[T Notification](c *Codec, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	register(c.notifications, c.notificationTyp, "notification", reflect.TypeFor[T](), name,
		func(data []byte) (Notification, error) {
			var v T
			if err := json.Unmarshal(data, &v); err != nil {
				return nil, err
			}
			return v, nil
		})
}

// RegisterNotifiable registers recipient type T under name, to be stored in
// payloads as JSON. Recipients that implement [Identifiable] and have a
// resolver for their kind are stored by reference instead. It panics like
// [RegisterNotification].
func RegisterNotifiable[T Notifiable](c *Codec, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	register(c.notifiables, c.notifiableTyp, "notifiable", reflect.TypeFor[T](), name,
		func(data []byte) (Notifiable, error) {
			var v T
			if err := json.Unmarshal(data, &v); err != nil {
				return nil, err
			}
			return v, nil
		})
}

func register[F any](byName map[string]F, byType map[reflect.Type]string, what string, t reflect.Type, name string, decode F) {
	if name == "" {
		panic(fmt.Sprintf("notification: empty %s name for %v", what, t))
	}
	if _, dup := byName[name]; dup {
		panic(fmt.Sprintf("notification: %s name %q registered twice", what, name))
	}
	if prev, dup := byType[t]; dup {
		panic(fmt.Sprintf("notification: %s type %v already registered as %q", what, t, prev))
	}
	byName[name] = decode
	byType[t] = name
}

// Resolve registers fn to load [Identifiable] recipients of the given kind.
// It panics if kind is empty or already has a resolver.
func (c *Codec) Resolve(kind string, fn ResolveFunc) {
	if kind == "" || fn == nil {
		panic("notification: Resolve needs a kind and a function")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, dup := c.resolvers[kind]; dup {
		panic(fmt.Sprintf("notification: resolver for %q registered twice", kind))
	}
	c.resolvers[kind] = fn
}

// Encode serializes d — its ID, channel, notification and recipient — into a
// payload for a job queue. The attempt count is not stored: the queue owns
// it. Encode fails if the notification's or the recipient's type is not
// registered.
func (c *Codec) Encode(d Delivery) ([]byte, error) {
	if d.Notification == nil || d.To == nil {
		return nil, errors.New("notification: cannot encode a delivery without notification and recipient")
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	p := wirePayload{V: payloadVersion, ID: d.ID, Channel: d.Channel}

	nt := reflect.TypeOf(d.Notification)
	name, ok := c.notificationTyp[nt]
	if !ok {
		return nil, fmt.Errorf("notification: type %v is not registered; call RegisterNotification[%v]", nt, nt)
	}
	data, err := json.Marshal(d.Notification)
	if err != nil {
		return nil, fmt.Errorf("notification: encoding %s: %w", name, err)
	}
	p.Notification = wireValue{Type: name, Data: data}

	if p.Notifiable, err = c.encodeNotifiable(d.To); err != nil {
		return nil, err
	}
	return json.Marshal(p)
}

func (c *Codec) encodeNotifiable(to Notifiable) (wireValue, error) {
	if id, ok := to.(Identifiable); ok {
		kind, key := id.NotifiableKey()
		if _, ok := c.resolvers[kind]; ok {
			if key == "" {
				return wireValue{}, fmt.Errorf("notification: %v returned an empty id from NotifiableKey", reflect.TypeOf(to))
			}
			return wireValue{Type: kind, Ref: key}, nil
		}
	}
	t := reflect.TypeOf(to)
	name, ok := c.notifiableTyp[t]
	if !ok {
		return wireValue{}, fmt.Errorf("notification: recipient type %v is not registered; call RegisterNotifiable[%v], or implement Identifiable and register a resolver with Codec.Resolve", t, t)
	}
	data, err := json.Marshal(to)
	if err != nil {
		return wireValue{}, fmt.Errorf("notification: encoding recipient %s: %w", name, err)
	}
	return wireValue{Type: name, Data: data}, nil
}

// Decode rebuilds a delivery from a payload made by [Codec.Encode], loading an
// [Identifiable] recipient through its resolver. Its Attempt is 1; set it from
// the queue.
//
// A payload that cannot be decoded — malformed, from an unknown format
// version, or naming an unregistered type — is a permanent error (see
// [IsPermanent]). Resolver errors are returned unchanged.
func (c *Codec) Decode(ctx context.Context, payload []byte) (Delivery, error) {
	var p wirePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return Delivery{}, Permanent(fmt.Errorf("notification: decoding payload: %w", err))
	}
	if p.V != payloadVersion {
		return Delivery{}, Permanent(fmt.Errorf("notification: unsupported payload version %d", p.V))
	}

	c.mu.RLock()
	decodeN, okN := c.notifications[p.Notification.Type]
	decodeTo, okTo := c.notifiables[p.Notifiable.Type]
	resolve, okR := c.resolvers[p.Notifiable.Type]
	c.mu.RUnlock()

	if !okN {
		return Delivery{}, Permanent(fmt.Errorf("notification: unknown notification type %q", p.Notification.Type))
	}
	n, err := decodeN(p.Notification.Data)
	if err != nil {
		return Delivery{}, Permanent(fmt.Errorf("notification: decoding %s: %w", p.Notification.Type, err))
	}

	var to Notifiable
	switch {
	case p.Notifiable.Ref != "":
		if !okR {
			return Delivery{}, Permanent(fmt.Errorf("notification: no resolver for recipient kind %q", p.Notifiable.Type))
		}
		if to, err = resolve(ctx, p.Notifiable.Ref); err != nil {
			return Delivery{}, fmt.Errorf("notification: resolving %s %s: %w", p.Notifiable.Type, p.Notifiable.Ref, err)
		}
		if to == nil {
			return Delivery{}, Permanent(fmt.Errorf("notification: resolver for %q returned no recipient for %s", p.Notifiable.Type, p.Notifiable.Ref))
		}
	case okTo:
		if to, err = decodeTo(p.Notifiable.Data); err != nil {
			return Delivery{}, Permanent(fmt.Errorf("notification: decoding recipient %s: %w", p.Notifiable.Type, err))
		}
	default:
		return Delivery{}, Permanent(fmt.Errorf("notification: unknown recipient type %q", p.Notifiable.Type))
	}

	return Delivery{ID: p.ID, Channel: p.Channel, To: to, Notification: n, Attempt: 1}, nil
}
