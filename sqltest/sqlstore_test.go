package sqltest_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/database"
	"github.com/zahansafallwa1511/go-notification/database/sqlstore"
	"github.com/zahansafallwa1511/go-notification/database/storetest"
)

func sqliteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "notifications.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// SQLite serialises writes; one connection keeps concurrent inserts from
	// failing with SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func newStore(t *testing.T, db *sql.DB, dialect sqlstore.Dialect, opts ...sqlstore.Option) *sqlstore.Store {
	t.Helper()
	store := sqlstore.New(db, dialect, opts...)
	if err := store.CreateTable(t.Context()); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	// Twice, to prove it is idempotent.
	if err := store.CreateTable(t.Context()); err != nil {
		t.Fatalf("second CreateTable: %v", err)
	}
	return store
}

func TestSQLite(t *testing.T) {
	storetest.Run(t, func(t *testing.T) database.Store {
		return newStore(t, sqliteDB(t), sqlstore.SQLite)
	})
}

var tableSeq atomic.Int64

// serverStore gives each subtest its own table on a shared server and drops it
// afterwards, so subtests never see each other's rows.
func serverStore(t *testing.T, db *sql.DB, dialect sqlstore.Dialect) database.Store {
	t.Helper()
	table := fmt.Sprintf("notifications_test_%d_%d", os.Getpid(), tableSeq.Add(1))
	store := newStore(t, db, dialect, sqlstore.WithTable(table))
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+table) })
	return store
}

func openServer(t *testing.T, driver, env string) *sql.DB {
	t.Helper()
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skipf("%s not set", env)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("open %s: %v", driver, err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("ping %s: %v", driver, err)
	}
	return db
}

func TestPostgres(t *testing.T) {
	db := openServer(t, "postgres", "POSTGRES_DSN")
	storetest.Run(t, func(t *testing.T) database.Store { return serverStore(t, db, sqlstore.Postgres) })
}

func TestMySQL(t *testing.T) {
	db := openServer(t, "mysql", "MYSQL_DSN")
	storetest.Run(t, func(t *testing.T) database.Store { return serverStore(t, db, sqlstore.MySQL) })
}

type user struct{ ID int }

func (u user) RouteNotificationFor(channel string, _ notification.Notification) string {
	if channel == database.ChannelName {
		return fmt.Sprint(u.ID)
	}
	return ""
}

func (user) NotifiableType() string { return "user" }

type commentPosted struct {
	Post   string `json:"post"`
	Author string `json:"author"`
}

func (commentPosted) Via(notification.Notifiable) []string { return []string{database.ChannelName} }
func (commentPosted) DatabaseType() string                 { return "comment.posted" }
func (c commentPosted) ToDatabase(notification.Notifiable) (any, error) {
	return c, nil
}

func TestEndToEnd(t *testing.T) {
	store := newStore(t, sqliteDB(t), sqlstore.SQLite)
	notifier := notification.New(notification.WithChannel(database.ChannelName, database.NewChannel(store)))

	alice := user{ID: 7}
	if err := notifier.Send(t.Context(), alice, commentPosted{Post: "hello-world", Author: "bob"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	inbox := database.InboxFor(store, alice)
	unread, err := inbox.Unread(t.Context(), 10)
	if err != nil {
		t.Fatalf("Unread: %v", err)
	}
	if len(unread) != 1 {
		t.Fatalf("unread = %d, want 1", len(unread))
	}
	r := unread[0]
	if r.Type != "comment.posted" || r.NotifiableType != "user" || r.NotifiableID != "7" {
		t.Fatalf("record = %+v", r)
	}
	var got commentPosted
	if err := r.Decode(&got); err != nil || got.Author != "bob" {
		t.Fatalf("Decode = %+v, %v", got, err)
	}

	if n, err := inbox.MarkAllAsRead(t.Context()); err != nil || n != 1 {
		t.Fatalf("MarkAllAsRead = %d, %v", n, err)
	}
	if c, err := inbox.UnreadCount(t.Context()); err != nil || c != 0 {
		t.Fatalf("UnreadCount = %d, %v", c, err)
	}
}
