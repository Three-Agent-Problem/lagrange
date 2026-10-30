// Package pgtest gives tests a real Postgres. With LAGRANGE_TEST_DATABASE_URL set it
// uses that server; otherwise it runs initdb and postgres from the local installation
// (PATH, pg_config, or LAGRANGE_PG_BIN) in a temporary directory. Each test gets its own
// fresh database. Tests are skipped when neither is available.
package pgtest

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/three-agent-problem/lagrange/pg"
)

var (
	once     sync.Once
	baseURL  string
	startErr error
	stopFn   func()
	counter  atomic.Int64
)

// Main runs the package's tests and stops the shared server afterwards. Call it from
// TestMain: func TestMain(m *testing.M) { pgtest.Main(m) }.
func Main(m *testing.M) {
	code := m.Run()
	if stopFn != nil {
		stopFn()
	}
	os.Exit(code)
}

// URL returns the connection URL of a new, empty database.
func URL(t testing.TB) string {
	t.Helper()
	once.Do(func() { baseURL, stopFn, startErr = start() })
	if startErr != nil {
		t.Skipf("pgtest: no Postgres available: %v", startErr)
	}
	name := fmt.Sprintf("t_%d_%d", os.Getpid(), counter.Add(1))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("pgtest: create database: %v", err)
	}
	u, _ := url.Parse(baseURL)
	u.Path = "/" + name
	return u.String()
}

// Open returns a migrated pg.DB on a new database, closed when the test ends.
func Open(t testing.TB) *pg.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pg.Open(ctx, URL(t))
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func start() (string, func(), error) {
	if u := os.Getenv("LAGRANGE_TEST_DATABASE_URL"); u != "" {
		return u, nil, nil
	}
	bin, err := binDir()
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "lagrange-pgtest-")
	if err != nil {
		return "", nil, err
	}
	data := filepath.Join(dir, "data")
	if out, err := exec.Command(filepath.Join(bin, "initdb"), "-D", data, "-U", "postgres", "-A", "trust", "--no-sync", "-E", "UTF8").CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("initdb: %v: %s", err, out)
	}
	port, err := freePort()
	if err != nil {
		return "", nil, err
	}
	// The socket goes in the temp dir; a TCP port on localhost is what the tests use.
	opts := fmt.Sprintf("-p %d -k %s -c listen_addresses=127.0.0.1 -c fsync=off -c synchronous_commit=off -c full_page_writes=off -c max_connections=200", port, dir)
	if out, err := exec.Command(filepath.Join(bin, "pg_ctl"), "-D", data, "-l", filepath.Join(dir, "log"), "-o", opts, "-w", "start").CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("pg_ctl start: %v: %s", err, out)
	}
	stop := func() {
		_ = exec.Command(filepath.Join(bin, "pg_ctl"), "-D", data, "-m", "immediate", "stop").Run()
		os.RemoveAll(dir)
	}
	return fmt.Sprintf("postgres://postgres@127.0.0.1:%d/postgres?sslmode=disable", port), stop, nil
}

func binDir() (string, error) {
	if d := os.Getenv("LAGRANGE_PG_BIN"); d != "" {
		return d, nil
	}
	var dirs []string
	if p, err := exec.LookPath("postgres"); err == nil {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			dirs = append(dirs, filepath.Dir(real))
		}
	}
	if out, err := exec.Command("pg_config", "--bindir").Output(); err == nil {
		dirs = append(dirs, strings.TrimSpace(string(out)))
	}
	// initdb needs the server binary next to it (libpq-only installs have initdb alone).
	for _, d := range dirs {
		if fileExists(filepath.Join(d, "initdb")) && fileExists(filepath.Join(d, "postgres")) {
			return d, nil
		}
	}
	return "", fmt.Errorf("initdb and postgres not found (set LAGRANGE_PG_BIN or LAGRANGE_TEST_DATABASE_URL)")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
