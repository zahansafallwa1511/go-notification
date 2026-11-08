// Package sqltest runs the database/storetest conformance suite against
// database/sqlstore on real databases.
//
// It is a separate module so the SQL drivers it needs never become
// dependencies of go-notification itself. SQLite runs with no setup;
// PostgreSQL and MySQL run when POSTGRES_DSN or MYSQL_DSN point at a server:
//
//	cd sqltest
//	go test ./...
//	POSTGRES_DSN='postgres://postgres:secret@localhost:5432/postgres?sslmode=disable' go test ./...
//	MYSQL_DSN='root:secret@tcp(localhost:3306)/notifications' go test ./...
package sqltest
