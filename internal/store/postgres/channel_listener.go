package postgres

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// channelChangeNotifyChannel is the pg_notify channel name of the channels trigger (migration 0117); it must match the SQL.
const channelChangeNotifyChannel = "telesrv_channel_changed"

const (
	channelListenerInitialBackoff = 500 * time.Millisecond
	channelListenerMaxBackoff     = 30 * time.Second
)

// ChannelChangeListener uses a dedicated PG connection to LISTEN for channels change notifications and invalidate ChannelRowCache in real time.
//
// Strong-consistency guarantee: every (re)connection first establishes LISTEN, then flushes the whole table—any change committed during the disconnection window whose NOTIFY was not delivered
// is cleared by this flush; changes committed after LISTEN is established are precisely deleted by NOTIFY. The flush covers the seam
// between the two, so the cache is always strongly consistent with the channels table.
type ChannelChangeListener struct {
	dsn   string
	cache *ChannelRowCache
	log   *zap.Logger
}

// NewChannelChangeListener creates the listener; when cache is nil, Run returns immediately (no listener is needed if the cache is disabled).
func NewChannelChangeListener(dsn string, cache *ChannelRowCache, log *zap.Logger) *ChannelChangeListener {
	if log == nil {
		log = zap.NewNop()
	}
	return &ChannelChangeListener{dsn: dsn, cache: cache, log: log}
}

// Run blocks running the listen loop until ctx is cancelled (start it in a separate goroutine). Disconnects auto-reconnect (exponential backoff).
func (l *ChannelChangeListener) Run(ctx context.Context) {
	if l == nil || l.cache == nil {
		return
	}
	backoff := channelListenerInitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		err := l.listenAndConsume(ctx)
		if ctx.Err() != nil {
			return
		}
		l.log.Warn("channel change listener disconnected; reconnecting",
			zap.Error(err), zap.Duration("backoff", backoff))
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff *= 2
		if backoff > channelListenerMaxBackoff {
			backoff = channelListenerMaxBackoff
		}
	}
}

// listenAndConsume opens one connection, LISTENs, flushes, then consumes notifications until an error or ctx cancellation.
// The backoff is not reset before a notification is successfully consumed; Run controls the reconnect pacing.
func (l *ChannelChangeListener) listenAndConsume(ctx context.Context) error {
	conn, err := connectPostgresAdmitted(ctx, l.dsn)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, "LISTEN "+channelChangeNotifyChannel); err != nil {
		return err
	}
	// LISTEN is ready: clear the cache, discarding entries that may have changed during the disconnected window but whose notifications were not delivered.
	l.cache.flush()
	l.log.Info("channel change listener ready", zap.String("notify_channel", channelChangeNotifyChannel))

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if notification == nil {
			continue
		}
		id, perr := strconv.ParseInt(strings.TrimSpace(notification.Payload), 10, 64)
		if perr != nil || id == 0 {
			continue
		}
		l.cache.delete(id)
	}
}

// sleepCtx sleeps for d or returns early when ctx is cancelled; returning false means ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
