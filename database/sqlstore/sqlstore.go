// Package sqlstore provides a [database.Store] backed by a SQL database — the
// equivalent of Laravel's notifications table.
//
// It uses only database/sql, so any driver works. Three dialects ship with it —
// [Postgres], [MySQL] and [SQLite] — because they differ in placeholder syntax,
// in how an insert that ignores duplicates is spelled, and in DDL.
//
//	store := sqlstore.New(db, sqlstore.Postgres)
//	if err := store.CreateTable(ctx); err != nil {
//		return err
//	}
//	notifier := notification.New(
//		notification.WithChannel("database", database.NewChannel(store)),
//	)
//
// Call [Store.CreateTable] once, or run the equivalent migration yourself.
// Prune old records by deleting with a Before cut-off:
//
//	store.Delete(ctx, database.Query{Before: time.Now().AddDate(0, -3, 0), State: database.Read})
//
// # Schema
//
// Timestamps (created_at, read_at) are stored as BIGINT Unix nanoseconds in
// UTC. Every driver round-trips an integer exactly, whereas TIMESTAMP columns
// lose precision or time zones differently from driver to driver (and SQLite
// has no time type at all). The payload is stored as TEXT, byte for byte, so
// it reads back exactly as written.
package sqlstore

import (
	"context"
	databasesql "database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zahansafallwa1511/go-notification/database"
)

// Dialect names the SQL flavour of the target database.
type Dialect int

// Supported SQL dialects.
const (
	// Postgres selects the PostgreSQL dialect ($1 placeholders, ON CONFLICT).
	Postgres Dialect = iota

	// MySQL selects the MySQL dialect (? placeholders, ON DUPLICATE KEY).
	MySQL

	// SQLite selects the SQLite dialect (? placeholders, ON CONFLICT).
	SQLite
)

// DB is the subset of [database/sql.DB] the store uses. *sql.DB and *sql.Tx
// satisfy it, as does any wrapper with the same shape.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (databasesql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *databasesql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*databasesql.Rows, error)
}

// Store is a SQL-backed [database.Store]. It is safe for concurrent use as far
// as the underlying DB is.
type Store struct {
	db      DB
	dialect Dialect
	table   string
}

var _ database.Store = (*Store)(nil)

// Option configures a [Store].
type Option func(*Store)

// WithTable sets the table name. The default is "notifications". The name is
// interpolated into statements rather than bound as a parameter, because SQL
// does not allow a placeholder there — so pass a constant, never user input.
func WithTable(name string) Option {
	return func(s *Store) { s.table = name }
}

// New returns a store backed by db.
func New(db DB, dialect Dialect, opts ...Option) *Store {
	s := &Store{db: db, dialect: dialect, table: "notifications"}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Table returns the table name the store uses.
func (s *Store) Table() string { return s.table }

// CreateTable creates the table and its recipient index if they do not exist.
// It is safe to call on every boot.
func (s *Store) CreateTable(ctx context.Context) error {
	switch s.dialect {
	case MySQL:
		// MySQL has no CREATE INDEX IF NOT EXISTS, so the index is declared
		// inline where re-running the statement is harmless.
		_, err := s.db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id VARCHAR(36) NOT NULL PRIMARY KEY,
			type VARCHAR(255) NOT NULL,
			notifiable_type VARCHAR(191) NOT NULL,
			notifiable_id VARCHAR(191) NOT NULL,
			data LONGTEXT NOT NULL,
			read_at BIGINT NULL,
			created_at BIGINT NOT NULL,
			INDEX %s_notifiable_index (notifiable_type, notifiable_id, created_at)
		)`, s.table, s.table))
		if err != nil {
			return fmt.Errorf("sqlstore: creating table: %w", err)
		}
		return nil
	default:
		_, err := s.db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id VARCHAR(36) NOT NULL PRIMARY KEY,
			type VARCHAR(255) NOT NULL,
			notifiable_type VARCHAR(255) NOT NULL,
			notifiable_id VARCHAR(255) NOT NULL,
			data TEXT NOT NULL,
			read_at BIGINT NULL,
			created_at BIGINT NOT NULL
		)`, s.table))
		if err != nil {
			return fmt.Errorf("sqlstore: creating table: %w", err)
		}
		_, err = s.db.ExecContext(ctx, fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS %s_notifiable_index ON %s (notifiable_type, notifiable_id, created_at)`,
			s.table, s.table))
		if err != nil {
			return fmt.Errorf("sqlstore: creating index: %w", err)
		}
		return nil
	}
}

// Insert implements [database.Store]. A record whose ID already exists is
// ignored.
func (s *Store) Insert(ctx context.Context, r database.Record) error {
	if r.ID == "" {
		return errors.New("sqlstore: record has no ID")
	}
	data := string(r.Data)
	if data == "" {
		data = "null"
	}
	var readAt any
	if r.ReadAt != nil {
		readAt = r.ReadAt.UnixNano()
	}

	insert := fmt.Sprintf(`INSERT INTO %s (id, type, notifiable_type, notifiable_id, data, read_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, s.table)
	switch s.dialect {
	case MySQL:
		insert += ` ON DUPLICATE KEY UPDATE id = id`
	default:
		insert += ` ON CONFLICT (id) DO NOTHING`
	}
	_, err := s.db.ExecContext(ctx, s.rebind(insert),
		r.ID, r.Type, r.NotifiableType, r.NotifiableID, data, readAt, r.CreatedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("sqlstore: insert: %w", err)
	}
	return nil
}

// List implements [database.Store].
func (s *Store) List(ctx context.Context, q database.Query) ([]database.Record, error) {
	where, args := s.where(q)
	stmt := fmt.Sprintf(`SELECT id, type, notifiable_type, notifiable_id, data, read_at, created_at FROM %s%s ORDER BY created_at DESC, id DESC%s`,
		s.table, where, s.limit(q))

	rows, err := s.db.QueryContext(ctx, s.rebind(stmt), args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []database.Record
	for rows.Next() {
		var (
			r         database.Record
			data      string
			readAt    databasesql.NullInt64
			createdAt int64
		)
		if err := rows.Scan(&r.ID, &r.Type, &r.NotifiableType, &r.NotifiableID, &data, &readAt, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlstore: scanning: %w", err)
		}
		r.Data = []byte(data)
		r.CreatedAt = time.Unix(0, createdAt).UTC()
		if readAt.Valid {
			t := time.Unix(0, readAt.Int64).UTC()
			r.ReadAt = &t
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: list: %w", err)
	}
	return out, nil
}

// Count implements [database.Store].
func (s *Store) Count(ctx context.Context, q database.Query) (int, error) {
	where, args := s.where(q)
	var n int
	err := s.db.QueryRowContext(ctx, s.rebind(fmt.Sprintf(`SELECT COUNT(*) FROM %s%s`, s.table, where)), args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: count: %w", err)
	}
	return n, nil
}

// MarkAsRead implements [database.Store].
func (s *Store) MarkAsRead(ctx context.Context, q database.Query, at time.Time) (int64, error) {
	if !q.Scoped() {
		return 0, database.ErrUnscopedQuery
	}
	where, args := s.where(q, "read_at IS NULL")
	stmt := fmt.Sprintf(`UPDATE %s SET read_at = ?%s`, s.table, where)
	return s.exec(ctx, "mark as read", stmt, append([]any{at.UnixNano()}, args...))
}

// MarkAsUnread implements [database.Store].
func (s *Store) MarkAsUnread(ctx context.Context, q database.Query) (int64, error) {
	if !q.Scoped() {
		return 0, database.ErrUnscopedQuery
	}
	where, args := s.where(q, "read_at IS NOT NULL")
	return s.exec(ctx, "mark as unread", fmt.Sprintf(`UPDATE %s SET read_at = NULL%s`, s.table, where), args)
}

// Delete implements [database.Store].
func (s *Store) Delete(ctx context.Context, q database.Query) (int64, error) {
	if !q.Scoped() {
		return 0, database.ErrUnscopedQuery
	}
	where, args := s.where(q)
	return s.exec(ctx, "delete", fmt.Sprintf(`DELETE FROM %s%s`, s.table, where), args)
}

func (s *Store) exec(ctx context.Context, op, stmt string, args []any) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.rebind(stmt), args...)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: %s: %w", op, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlstore: %s: %w", op, err)
	}
	return n, nil
}

// where builds the WHERE clause for q plus any extra conditions, written with
// ? placeholders.
func (s *Store) where(q database.Query, extra ...string) (string, []any) {
	var conds []string
	var args []any
	in := func(col string, values []string) {
		conds = append(conds, col+" IN ("+strings.TrimSuffix(strings.Repeat("?, ", len(values)), ", ")+")")
		for _, v := range values {
			args = append(args, v)
		}
	}
	if len(q.IDs) > 0 {
		in("id", q.IDs)
	}
	if q.NotifiableType != "" {
		conds = append(conds, "notifiable_type = ?")
		args = append(args, q.NotifiableType)
	}
	if q.NotifiableID != "" {
		conds = append(conds, "notifiable_id = ?")
		args = append(args, q.NotifiableID)
	}
	if len(q.Types) > 0 {
		in("type", q.Types)
	}
	switch q.State {
	case database.Unread:
		conds = append(conds, "read_at IS NULL")
	case database.Read:
		conds = append(conds, "read_at IS NOT NULL")
	}
	if !q.Before.IsZero() {
		conds = append(conds, "created_at < ?")
		args = append(args, q.Before.UnixNano())
	}
	conds = append(conds, extra...)
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// limit renders LIMIT/OFFSET. MySQL and SQLite need a LIMIT whenever there is
// an OFFSET, so "no limit" is spelled per dialect. The values are integers
// formatted by the store, never caller strings.
func (s *Store) limit(q database.Query) string {
	if q.Limit <= 0 && q.Offset <= 0 {
		return ""
	}
	lim := fmt.Sprint(q.Limit)
	if q.Limit <= 0 {
		switch s.dialect {
		case MySQL:
			lim = "18446744073709551615"
		case SQLite:
			lim = "-1"
		default:
			lim = "ALL"
		}
	}
	out := " LIMIT " + lim
	if q.Offset > 0 {
		out += fmt.Sprintf(" OFFSET %d", q.Offset)
	}
	return out
}

// rebind rewrites the ? placeholders of query into the dialect's own form.
func (s *Store) rebind(query string) string {
	if s.dialect != Postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
