// Package pg keeps Lagrange's state in Postgres: the a2a-go task store, push config
// store, work queue and event queue that cluster mode needs, plus the tables the
// platform itself uses (users, addresses, tokens, task links, activity).
//
// Postgres is the only dependency. The work queue uses FOR UPDATE SKIP LOCKED with
// leases, and LISTEN/NOTIFY wakes waiting workers and live views without polling.
package pg

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Notification channels.
const (
	// ChannelWork is notified when a work item is written.
	ChannelWork = "lagrange_work"
	// ChannelActivity is notified with the seq of each new activity row.
	ChannelActivity = "lagrange_activity"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB is a connection pool plus a listener that fans out notifications.
type DB struct {
	Pool *pgxpool.Pool

	mu   sync.Mutex
	subs map[string]map[chan string]struct{}
	stop context.CancelFunc
	done chan struct{}
}

// Open connects to url, applies migrations and starts listening for notifications.
func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("pg: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	db := &DB{Pool: pool, subs: map[string]map[chan string]struct{}{}, done: make(chan struct{})}
	lctx, cancel := context.WithCancel(context.Background())
	db.stop = cancel
	go db.listen(lctx)
	return db, nil
}

// Close stops the listener and closes the pool.
func (db *DB) Close() {
	db.stop()
	<-db.done
	db.Pool.Close()
}

// Subscribe returns a channel that receives the payload of every notification on
// channel, and a function that unsubscribes. Slow receivers miss notifications rather
// than block others, so treat a notification as a hint to re-read.
func (db *DB) Subscribe(channel string) (<-chan string, func()) {
	ch := make(chan string, 64)
	db.mu.Lock()
	if db.subs[channel] == nil {
		db.subs[channel] = map[chan string]struct{}{}
	}
	db.subs[channel][ch] = struct{}{}
	db.mu.Unlock()
	return ch, func() {
		db.mu.Lock()
		delete(db.subs[channel], ch)
		db.mu.Unlock()
	}
}

// Notify sends payload on channel.
func (db *DB) Notify(ctx context.Context, channel, payload string) error {
	_, err := db.Pool.Exec(ctx, "SELECT pg_notify($1, $2)", channel, payload)
	return err
}

func (db *DB) listen(ctx context.Context) {
	defer close(db.done)
	for ctx.Err() == nil {
		if err := db.listenOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("pg: listener stopped, reconnecting", "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}
}

func (db *DB) listenOnce(ctx context.Context) error {
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	for _, ch := range []string{ChannelWork, ChannelActivity} {
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{ch}.Sanitize()); err != nil {
			return err
		}
	}
	// Anything may have happened while we were not listening.
	db.fanout(ChannelWork, "")
	db.fanout(ChannelActivity, "")
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			// The connection is in an unknown state; don't put it back in the pool.
			conn.Hijack().Close(context.Background())
			return err
		}
		db.fanout(n.Channel, n.Payload)
	}
}

func (db *DB) fanout(channel, payload string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	for ch := range db.subs[channel] {
		select {
		case ch <- payload:
		default:
		}
	}
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("pg: migrations table: %w", err)
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(strings.TrimPrefix(name, "migrations/"), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("pg: migration %s has no numeric prefix", name)
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			// Serialize concurrent starts of several replicas.
			if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7210523)"); err != nil {
				return err
			}
			var done bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", version).Scan(&done); err != nil {
				return err
			}
			if done {
				return nil
			}
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version)
			return err
		}); err != nil {
			return fmt.Errorf("pg: migration %s: %w", name, err)
		}
	}
	return nil
}

// IsUniqueViolation reports whether err is a unique constraint violation.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// IsForeignKeyViolation reports whether err is a foreign key violation.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
