package store

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The store tests run against a real Postgres: embedded-postgres downloads the
// binaries once (cached in ~/.embedded-postgres-go) and starts one server for
// the package. Each test gets a database of its own from newDatabase.
// `go test -short` skips them.

// adminURL is the server's maintenance database; empty when the server is not
// running, in which case database tests skip.
var adminURL string

var databaseSeq atomic.Int64

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	os.Exit(runWithPostgres(m))
}

func runWithPostgres(m *testing.M) int {
	dir, err := os.MkdirTemp("", "plimsoll-store-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "embedded postgres:", err)
		return 1
	}
	defer os.RemoveAll(dir)

	port, err := freePort()
	if err != nil {
		fmt.Fprintln(os.Stderr, "embedded postgres:", err)
		return 1
	}
	var serverLog bytes.Buffer
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(port).
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		BinariesPath(filepath.Join(dir, "bin")).
		StartTimeout(60 * time.Second).
		Logger(&serverLog))
	if err := pg.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "embedded postgres: %v\n%s", err, serverLog.String())
		return 1
	}
	defer func() {
		if err := pg.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "embedded postgres stop:", err)
		}
	}()

	adminURL = fmt.Sprintf("postgres://postgres:postgres@localhost:%d/postgres?sslmode=disable", port)
	return m.Run()
}

func freePort() (uint32, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port), nil
}

// newDatabase creates an empty database for one test, drops it when the test
// ends, and returns its URL.
func newDatabase(t testing.TB) string {
	t.Helper()
	if adminURL == "" {
		t.Skip("embedded postgres is not running (go test -short)")
	}
	ctx := context.Background()
	name := fmt.Sprintf("store_test_%d_%d", os.Getpid(), databaseSeq.Add(1))
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect to embedded postgres: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), adminURL)
		if err != nil {
			t.Errorf("drop database %s: %v", name, err)
			return
		}
		defer conn.Close(context.Background())
		if _, err := conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	return fmt.Sprintf("postgres://postgres:postgres@localhost:%s/%s?sslmode=disable", portOf(adminURL), name)
}

// openStore opens a Store on a fresh database, applying the schema the way
// the indexer does on start.
func openStore(t testing.TB) (*Store, string) {
	t.Helper()
	url := newDatabase(t)
	s, err := Open(context.Background(), url)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Close)
	return s, url
}

func portOf(url string) string {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		panic(err)
	}
	return fmt.Sprint(cfg.Port)
}

// execSQL runs statements on the database at url outside any Store.
func execSQL(t testing.TB, url, sql string, args ...any) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %.60q: %v", sql, err)
	}
}

// testPool is a connection pool on the database at url, closed with the test.
func testPool(t testing.TB, url string) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}
