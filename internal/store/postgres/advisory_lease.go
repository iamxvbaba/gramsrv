package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// advisoryLeaseHeartbeat is how often the lease holder pings the connection
// that owns the session-level lock. A dead connection releases the lock on the
// server, so the holder must stop its job promptly after that.
const advisoryLeaseHeartbeat = 10 * time.Second

// RunWithAdvisoryLease runs fn on at most one process at a time: the caller
// that holds the session-level PostgreSQL advisory lock named key. It blocks
// until ctx ends, retrying every retry interval while another process holds
// the lease or after fn returns or the lease is lost. fn's context is cancelled
// as soon as the lock connection fails a heartbeat.
//
// The lease only avoids duplicate work. Jobs run under it must still be
// idempotent in the database, because a holder notices a lost connection at
// most one heartbeat late.
func RunWithAdvisoryLease(ctx context.Context, pool *pgxpool.Pool, key string, retry time.Duration, log *zap.Logger, fn func(context.Context) error) {
	if log == nil {
		log = zap.NewNop()
	}
	if retry <= 0 {
		retry = 30 * time.Second
	}
	for {
		held, err := runAdvisoryLeaseOnce(ctx, pool, key, log, fn)
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil && held:
			log.Warn("advisory lease job stopped", zap.String("lease", key), zap.Error(err))
		case err != nil:
			log.Warn("advisory lease acquire failed", zap.String("lease", key), zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

func runAdvisoryLeaseOnce(ctx context.Context, pool *pgxpool.Pool, key string, log *zap.Logger, fn func(context.Context) error) (bool, error) {
	if pool == nil || key == "" || fn == nil {
		return false, fmt.Errorf("advisory lease requires a pool, a key and a job")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire advisory lease connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&acquired); err != nil {
		conn.Release()
		return false, fmt.Errorf("try advisory lease %s: %w", key, err)
	}
	if !acquired {
		conn.Release()
		return false, nil
	}
	log.Info("advisory lease acquired", zap.String("lease", key))

	leaseCtx, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(advisoryLeaseHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(leaseCtx, advisoryLeaseHeartbeat)
				err := conn.Ping(pingCtx)
				pingCancel()
				if err != nil && leaseCtx.Err() == nil {
					log.Warn("advisory lease lost", zap.String("lease", key), zap.Error(err))
					cancel()
					return
				}
			}
		}
	}()
	jobErr := fn(leaseCtx)
	cancel()
	<-heartbeatDone

	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer releaseCancel()
	var released bool
	if err := conn.QueryRow(releaseCtx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key).Scan(&released); err != nil || !released {
		// Closing the session is the only other way to drop a session lock.
		_ = conn.Conn().Close(releaseCtx)
	}
	conn.Release()
	if errors.Is(jobErr, context.Canceled) {
		jobErr = nil
	}
	return true, jobErr
}
