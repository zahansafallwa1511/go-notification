// Package storetest provides a conformance suite for [database.Store]
// implementations.
//
// A store — including one you write outside this module — proves it honours
// the contract by calling [Run] with a factory:
//
//	func TestStore(t *testing.T) {
//		storetest.Run(t, func(t *testing.T) database.Store {
//			return mystore.New()
//		})
//	}
package storetest

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/zahansafallwa1511/go-notification/database"
)

// Factory builds a store for one subtest. It must return an empty store: the
// suite assumes no record it did not insert exists.
type Factory func(t *testing.T) database.Store

// base is the creation time of the first fixture record. Later records are a
// minute apart, so ordering never depends on ties.
var base = time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)

// record builds a fixture for user "u1" created i minutes after base.
func record(id string, i int) database.Record {
	return database.Record{
		ID:             id,
		Type:           "app.Welcome",
		NotifiableType: "app.User",
		NotifiableID:   "u1",
		Data:           json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)),
		CreatedAt:      base.Add(time.Duration(i) * time.Minute),
	}
}

// Run exercises the [database.Store] contract. Each case runs as a subtest, so
// a failure names the behaviour that broke.
func Run(t *testing.T, newStore Factory) {
	t.Helper()

	t.Run("InsertList", func(t *testing.T) {
		s := newStore(t)
		want := record("a", 0)
		mustInsert(t, s, want)

		got := mustList(t, s, database.Query{})
		if len(got) != 1 {
			t.Fatalf("List returned %d records, want 1", len(got))
		}
		r := got[0]
		if r.ID != want.ID || r.Type != want.Type || r.NotifiableType != want.NotifiableType || r.NotifiableID != want.NotifiableID {
			t.Fatalf("List = %+v, want %+v", r, want)
		}
		if !r.CreatedAt.Equal(want.CreatedAt) {
			t.Fatalf("CreatedAt = %v, want %v (must round-trip exactly)", r.CreatedAt, want.CreatedAt)
		}
		if r.ReadAt != nil || r.Read() {
			t.Fatalf("new record should be unread, ReadAt = %v", r.ReadAt)
		}
		var data struct{ N int }
		if err := r.Decode(&data); err != nil || data.N != 0 {
			t.Fatalf("Decode = %+v, %v", data, err)
		}
	})

	t.Run("InsertDuplicateIsNoop", func(t *testing.T) {
		s := newStore(t)
		mustInsert(t, s, record("a", 0))
		dup := record("a", 5)
		dup.Data = json.RawMessage(`{"n":99}`)
		if err := s.Insert(t.Context(), dup); err != nil {
			t.Fatalf("Insert of an existing ID = %v, want nil", err)
		}
		got := mustList(t, s, database.Query{})
		if len(got) != 1 {
			t.Fatalf("List returned %d records after duplicate insert, want 1", len(got))
		}
		if string(got[0].Data) != `{"n":0}` {
			t.Fatalf("duplicate insert overwrote data: %s", got[0].Data)
		}
	})

	t.Run("NewestFirstAndPaging", func(t *testing.T) {
		s := newStore(t)
		for i, id := range []string{"a", "b", "c", "d", "e"} {
			mustInsert(t, s, record(id, i))
		}
		assertIDs(t, mustList(t, s, database.Query{}), "e", "d", "c", "b", "a")
		assertIDs(t, mustList(t, s, database.Query{Limit: 2}), "e", "d")
		assertIDs(t, mustList(t, s, database.Query{Limit: 2, Offset: 2}), "c", "b")
		assertIDs(t, mustList(t, s, database.Query{Offset: 3}), "b", "a")
		assertIDs(t, mustList(t, s, database.Query{Offset: 10}))
	})

	t.Run("Filters", func(t *testing.T) {
		s := newStore(t)
		a := record("a", 0)
		b := record("b", 1)
		b.Type = "app.InvoicePaid"
		c := record("c", 2)
		c.NotifiableID = "u2"
		d := record("d", 3)
		d.NotifiableType = "app.Team"
		for _, r := range []database.Record{a, b, c, d} {
			mustInsert(t, s, r)
		}

		assertIDs(t, mustList(t, s, database.Query{NotifiableID: "u1"}), "d", "b", "a")
		assertIDs(t, mustList(t, s, database.Query{NotifiableType: "app.User", NotifiableID: "u1"}), "b", "a")
		assertIDs(t, mustList(t, s, database.Query{Types: []string{"app.InvoicePaid"}}), "b")
		assertIDs(t, mustList(t, s, database.Query{Types: []string{"app.InvoicePaid", "app.Welcome"}, NotifiableID: "u2"}), "c")
		assertIDs(t, mustList(t, s, database.Query{IDs: []string{"a", "c", "missing"}}), "c", "a")
		assertIDs(t, mustList(t, s, database.Query{Before: base.Add(2 * time.Minute)}), "b", "a")

		if n := mustCount(t, s, database.Query{NotifiableID: "u1"}); n != 3 {
			t.Fatalf("Count = %d, want 3", n)
		}
		if n := mustCount(t, s, database.Query{NotifiableID: "u1", Limit: 1}); n != 3 {
			t.Fatalf("Count with Limit = %d, want 3 (Limit must be ignored)", n)
		}
	})

	t.Run("MarkAsReadAndUnread", func(t *testing.T) {
		s := newStore(t)
		for i, id := range []string{"a", "b", "c"} {
			mustInsert(t, s, record(id, i))
		}
		at := base.Add(time.Hour)

		n, err := s.MarkAsRead(t.Context(), database.Query{IDs: []string{"a", "b"}}, at)
		if err != nil || n != 2 {
			t.Fatalf("MarkAsRead = %d, %v, want 2", n, err)
		}
		n, err = s.MarkAsRead(t.Context(), database.Query{IDs: []string{"a"}}, at.Add(time.Hour))
		if err != nil || n != 0 {
			t.Fatalf("MarkAsRead on a read record = %d, %v, want 0", n, err)
		}

		read := mustList(t, s, database.Query{State: database.Read})
		assertIDs(t, read, "b", "a")
		for _, r := range read {
			if r.ReadAt == nil || !r.ReadAt.Equal(at) {
				t.Fatalf("ReadAt = %v, want %v (re-marking must not move it)", r.ReadAt, at)
			}
		}
		assertIDs(t, mustList(t, s, database.Query{State: database.Unread}), "c")
		if c := mustCount(t, s, database.Query{NotifiableID: "u1", State: database.Unread}); c != 1 {
			t.Fatalf("unread Count = %d, want 1", c)
		}

		n, err = s.MarkAsUnread(t.Context(), database.Query{NotifiableID: "u1"})
		if err != nil || n != 2 {
			t.Fatalf("MarkAsUnread = %d, %v, want 2", n, err)
		}
		if c := mustCount(t, s, database.Query{State: database.Unread}); c != 3 {
			t.Fatalf("unread Count after MarkAsUnread = %d, want 3", c)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		s := newStore(t)
		for i, id := range []string{"a", "b", "c"} {
			mustInsert(t, s, record(id, i))
		}
		other := record("x", 3)
		other.NotifiableID = "u2"
		mustInsert(t, s, other)

		n, err := s.Delete(t.Context(), database.Query{IDs: []string{"a"}})
		if err != nil || n != 1 {
			t.Fatalf("Delete by id = %d, %v, want 1", n, err)
		}
		n, err = s.Delete(t.Context(), database.Query{Before: base.Add(2 * time.Minute)})
		if err != nil || n != 1 {
			t.Fatalf("Delete before = %d, %v, want 1", n, err)
		}
		n, err = s.Delete(t.Context(), database.Query{NotifiableID: "u1"})
		if err != nil || n != 1 {
			t.Fatalf("Delete by notifiable = %d, %v, want 1", n, err)
		}
		assertIDs(t, mustList(t, s, database.Query{}), "x")
	})

	t.Run("UnscopedQueriesRejected", func(t *testing.T) {
		s := newStore(t)
		mustInsert(t, s, record("a", 0))

		for name, q := range map[string]database.Query{
			"zero":       {},
			"type only":  {NotifiableType: "app.User"},
			"types only": {Types: []string{"app.Welcome"}},
			"state only": {State: database.Unread},
			"empty ids":  {IDs: []string{}},
		} {
			if _, err := s.MarkAsRead(t.Context(), q, base); !errors.Is(err, database.ErrUnscopedQuery) {
				t.Errorf("MarkAsRead(%s) = %v, want ErrUnscopedQuery", name, err)
			}
			if _, err := s.MarkAsUnread(t.Context(), q); !errors.Is(err, database.ErrUnscopedQuery) {
				t.Errorf("MarkAsUnread(%s) = %v, want ErrUnscopedQuery", name, err)
			}
			if _, err := s.Delete(t.Context(), q); !errors.Is(err, database.ErrUnscopedQuery) {
				t.Errorf("Delete(%s) = %v, want ErrUnscopedQuery", name, err)
			}
		}
		if n := mustCount(t, s, database.Query{}); n != 1 {
			t.Fatalf("Count after rejected queries = %d, want 1", n)
		}
	})

	t.Run("ReturnedRecordsAreCopies", func(t *testing.T) {
		s := newStore(t)
		mustInsert(t, s, record("a", 0))
		got := mustList(t, s, database.Query{})
		got[0].Data[0] = 'X'
		again := mustList(t, s, database.Query{})
		if string(again[0].Data) != `{"n":0}` {
			t.Fatalf("mutating a returned record changed the store: %s", again[0].Data)
		}
	})

	t.Run("ConcurrentInserts", func(t *testing.T) {
		s := newStore(t)
		const n = 20
		var wg sync.WaitGroup
		errs := make(chan error, 2*n)
		for i := range n {
			wg.Add(2)
			go func() {
				defer wg.Done()
				errs <- s.Insert(t.Context(), record(fmt.Sprintf("r%02d", i), i))
			}()
			go func() {
				defer wg.Done()
				errs <- s.Insert(t.Context(), record("contested", 100))
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent Insert: %v", err)
			}
		}
		if c := mustCount(t, s, database.Query{}); c != n+1 {
			t.Fatalf("Count = %d, want %d", c, n+1)
		}
	})
}

func mustInsert(t *testing.T, s database.Store, r database.Record) {
	t.Helper()
	if err := s.Insert(t.Context(), r); err != nil {
		t.Fatalf("Insert(%s): %v", r.ID, err)
	}
}

func mustList(t *testing.T, s database.Store, q database.Query) []database.Record {
	t.Helper()
	got, err := s.List(t.Context(), q)
	if err != nil {
		t.Fatalf("List(%+v): %v", q, err)
	}
	return got
}

func mustCount(t *testing.T, s database.Store, q database.Query) int {
	t.Helper()
	n, err := s.Count(t.Context(), q)
	if err != nil {
		t.Fatalf("Count(%+v): %v", q, err)
	}
	return n
}

func assertIDs(t *testing.T, got []database.Record, want ...string) {
	t.Helper()
	ids := make([]string, len(got))
	for i, r := range got {
		ids[i] = r.ID
	}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range ids {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}
