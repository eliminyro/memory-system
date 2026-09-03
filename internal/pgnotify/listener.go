// Package pgnotify runs one shared PostgreSQL LISTEN connection and dispatches
// notifications to per-channel reload functions. It owns the pinned connection,
// dispatch, reconnect, and health; it knows nothing about what any cache holds.
package pgnotify

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// ReloadFunc reloads a cache from the database. It MUST be idempotent: the
// listener calls it on every reconnect and a replica may get its own
// notification, so a redundant reload has to be a no-op.
type ReloadFunc func(ctx context.Context) error

// AppName tags the listener's backend in pg_stat_activity so operators can find
// (and tests can terminate) exactly this connection.
const AppName = "memory-config-listener"

const (
	defaultBaseBackoff = 500 * time.Millisecond
	defaultMaxBackoff  = 30 * time.Second
)

// Listener holds the dedicated LISTEN connection and its channel registrations.
type Listener struct {
	db     *sql.DB
	logger *slog.Logger
	regs   map[string]ReloadFunc

	baseBackoff time.Duration
	maxBackoff  time.Duration

	healthy atomic.Bool

	cancel    context.CancelFunc
	done      chan struct{}
	ready     chan error
	readyOnce sync.Once
}

// Option tunes a Listener at construction.
type Option func(*Listener)

// WithBackoff sets the reconnect backoff ramp (base doubles up to max).
func WithBackoff(base, max time.Duration) Option {
	return func(l *Listener) { l.baseBackoff, l.maxBackoff = base, max }
}

// New returns a Listener over db's pool. Register channels, then Start.
func New(db *sql.DB, logger *slog.Logger, opts ...Option) *Listener {
	if logger == nil {
		logger = slog.Default()
	}
	l := &Listener{
		db:          db,
		logger:      logger,
		regs:        map[string]ReloadFunc{},
		baseBackoff: defaultBaseBackoff,
		maxBackoff:  defaultMaxBackoff,
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Register binds a reload function to a channel. Call before Start; the set is
// read when the connection is (re)established.
func (l *Listener) Register(channel string, reload ReloadFunc) {
	l.regs[channel] = reload
}

// Start establishes LISTEN, then runs the dispatch/reconnect loop in the
// background. It returns once the first LISTEN is established (nil) or the first
// attempt fails — so a caller can guarantee LISTEN precedes any initial load.
func (l *Listener) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.done = make(chan struct{})
	l.ready = make(chan error, 1)
	go l.run(runCtx)
	select {
	case err := <-l.ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Healthy reports whether the listener currently holds a live listening connection.
func (l *Listener) Healthy() bool { return l.healthy.Load() }

// Stop cancels the loop and waits for the pinned connection to be released.
func (l *Listener) Stop() {
	if l.cancel != nil {
		l.cancel()
	}
	if l.done != nil {
		<-l.done
	}
}

func (l *Listener) run(ctx context.Context) {
	defer close(l.done)
	backoff := l.baseBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		established, err := l.session(ctx)
		l.healthy.Store(false)
		if ctx.Err() != nil {
			return
		}
		if established {
			backoff = l.baseBackoff // a live session ran; restart the ramp
		}
		l.logger.Error("config invalidation listener disconnected; reconnecting",
			"error", err, "backoff", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > l.maxBackoff {
			backoff = l.maxBackoff
		}
	}
}

// session pins a connection, LISTENs on every channel, reloads every cache, then
// blocks dispatching notifications until the connection fails or ctx ends. It
// reports whether LISTEN was established and the error that ended the session.
func (l *Listener) session(ctx context.Context) (bool, error) {
	conn, err := l.db.Conn(ctx)
	if err != nil {
		l.signalReady(err)
		return false, fmt.Errorf("pin listener connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	established := false
	rawErr := conn.Raw(func(dc any) error {
		pc, err := unwrapPgx(dc)
		if err != nil {
			l.signalReady(err)
			return err
		}
		// Tag the backend so operators/tests can identify this connection.
		if _, err := pc.Exec(ctx, "SET application_name = '"+AppName+"'"); err != nil {
			l.signalReady(err)
			return fmt.Errorf("set application_name: %w", err)
		}
		for ch := range l.regs {
			if _, err := pc.Exec(ctx, "LISTEN "+pgx.Identifier{ch}.Sanitize()); err != nil {
				l.signalReady(err)
				return fmt.Errorf("listen %q: %w", ch, err)
			}
		}
		// LISTEN established: unblock Start before reloading, so the caller's
		// initial load can never precede our LISTEN.
		established = true
		l.signalReady(nil)
		// Reload everything unconditionally — Postgres queues no notifications for
		// a disconnected client, so this recovers a change made while down.
		l.reloadAll(ctx)
		l.healthy.Store(true)
		for {
			n, err := pc.WaitForNotification(ctx)
			if err != nil {
				return err
			}
			l.dispatch(ctx, n.Channel)
		}
	})
	return established, rawErr
}

func (l *Listener) reloadAll(ctx context.Context) {
	for ch, reload := range l.regs {
		if err := reload(ctx); err != nil {
			l.logger.Error("config reload failed", "channel", ch, "error", err)
		}
	}
}

// dispatch runs the channel's reload; an unregistered channel is ignored.
func (l *Listener) dispatch(ctx context.Context, channel string) {
	reload, ok := l.regs[channel]
	if !ok {
		return
	}
	if err := reload(ctx); err != nil {
		l.logger.Error("config reload failed", "channel", channel, "error", err)
	}
}

// signalReady resolves Start's wait exactly once — nil on the first established
// LISTEN, the error on a first failed attempt; later reconnects don't re-signal.
func (l *Listener) signalReady(err error) {
	l.readyOnce.Do(func() { l.ready <- err })
}

// unwrapPgx reaches the pgx connection behind a database/sql driver conn. Valid
// only inside sql.Conn.Raw, where the driver conn is a *stdlib.Conn.
func unwrapPgx(dc any) (*pgx.Conn, error) {
	sc, ok := dc.(*stdlib.Conn)
	if !ok {
		return nil, fmt.Errorf("driver connection is %T, want *stdlib.Conn", dc)
	}
	return sc.Conn(), nil
}
