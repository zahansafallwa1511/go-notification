package database

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryStore is an in-process [Store]. It is safe for concurrent use.
//
// Records live in memory and are lost when the process exits, which makes it
// the store for tests, prototypes and single-process tools — not for an inbox
// your users expect to keep.
type MemoryStore struct {
	mu      sync.RWMutex
	records map[string]Record
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: map[string]Record{}}
}

// Insert implements [Store].
func (s *MemoryStore) Insert(_ context.Context, r Record) error {
	if r.ID == "" {
		return errors.New("database: record has no ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[r.ID]; ok {
		return nil
	}
	s.records[r.ID] = clone(r)
	return nil
}

// List implements [Store].
func (s *MemoryStore) List(_ context.Context, q Query) ([]Record, error) {
	s.mu.RLock()
	var out []Record
	for _, r := range s.records {
		if q.Matches(r) {
			out = append(out, clone(r))
		}
	}
	s.mu.RUnlock()

	slices.SortFunc(out, func(a, b Record) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})

	if q.Offset > 0 {
		if q.Offset >= len(out) {
			return nil, nil
		}
		out = out[q.Offset:]
	}
	if q.Limit > 0 && q.Limit < len(out) {
		out = out[:q.Limit]
	}
	return out, nil
}

// Count implements [Store].
func (s *MemoryStore) Count(_ context.Context, q Query) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, r := range s.records {
		if q.Matches(r) {
			n++
		}
	}
	return n, nil
}

// MarkAsRead implements [Store].
func (s *MemoryStore) MarkAsRead(_ context.Context, q Query, at time.Time) (int64, error) {
	return s.update(q, func(r *Record) bool {
		if r.ReadAt != nil {
			return false
		}
		t := at
		r.ReadAt = &t
		return true
	})
}

// MarkAsUnread implements [Store].
func (s *MemoryStore) MarkAsUnread(_ context.Context, q Query) (int64, error) {
	return s.update(q, func(r *Record) bool {
		if r.ReadAt == nil {
			return false
		}
		r.ReadAt = nil
		return true
	})
}

// Delete implements [Store].
func (s *MemoryStore) Delete(_ context.Context, q Query) (int64, error) {
	if !q.Scoped() {
		return 0, ErrUnscopedQuery
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for id, r := range s.records {
		if q.Matches(r) {
			delete(s.records, id)
			n++
		}
	}
	return n, nil
}

func (s *MemoryStore) update(q Query, change func(*Record) bool) (int64, error) {
	if !q.Scoped() {
		return 0, ErrUnscopedQuery
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for id, r := range s.records {
		if !q.Matches(r) {
			continue
		}
		if change(&r) {
			s.records[id] = r
			n++
		}
	}
	return n, nil
}

// clone copies r so callers cannot mutate what the store holds.
func clone(r Record) Record {
	if r.Data != nil {
		r.Data = append([]byte(nil), r.Data...)
	}
	if r.ReadAt != nil {
		t := *r.ReadAt
		r.ReadAt = &t
	}
	return r
}
