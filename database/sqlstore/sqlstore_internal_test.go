package sqlstore

import (
	"testing"
	"time"

	"github.com/zahansafallwa1511/go-notification/database"
)

// The SQL itself is exercised against SQLite (and, when configured, Postgres
// and MySQL) by the sqltest module. These cases pin the dialect-specific
// rendering that SQLite alone cannot cover.

func TestRebindPostgres(t *testing.T) {
	s := New(nil, Postgres)
	where, args := s.where(database.Query{IDs: []string{"a", "b"}, NotifiableID: "u1", Before: time.Unix(0, 5)}, "read_at IS NULL")
	got := s.rebind("UPDATE n SET read_at = ?" + where)
	want := "UPDATE n SET read_at = $1 WHERE id IN ($2, $3) AND notifiable_id = $4 AND created_at < $5 AND read_at IS NULL"
	if got != want {
		t.Fatalf("rebind =\n%s\nwant\n%s", got, want)
	}
	if len(args) != 4 {
		t.Fatalf("args = %v, want 4", args)
	}
}

func TestRebindLeavesQuestionMarksForOthers(t *testing.T) {
	for _, d := range []Dialect{MySQL, SQLite} {
		if got := New(nil, d).rebind("a = ? AND b = ?"); got != "a = ? AND b = ?" {
			t.Fatalf("dialect %d rebind = %q", d, got)
		}
	}
}

func TestLimit(t *testing.T) {
	cases := []struct {
		dialect Dialect
		q       database.Query
		want    string
	}{
		{Postgres, database.Query{}, ""},
		{Postgres, database.Query{Limit: 5}, " LIMIT 5"},
		{Postgres, database.Query{Limit: 5, Offset: 10}, " LIMIT 5 OFFSET 10"},
		{Postgres, database.Query{Offset: 10}, " LIMIT ALL OFFSET 10"},
		{MySQL, database.Query{Offset: 10}, " LIMIT 18446744073709551615 OFFSET 10"},
		{SQLite, database.Query{Offset: 10}, " LIMIT -1 OFFSET 10"},
	}
	for _, tc := range cases {
		if got := New(nil, tc.dialect).limit(tc.q); got != tc.want {
			t.Errorf("dialect %d limit(%+v) = %q, want %q", tc.dialect, tc.q, got, tc.want)
		}
	}
}
