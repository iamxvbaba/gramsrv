package rpc

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/postresponse"
)

const (
	defaultBootstrapBatch    = 100
	defaultBootstrapInterval = 200 * time.Millisecond
	defaultBootstrapLease    = 30 * time.Second
)

type BootstrapUpdateDispatcher struct {
	router          *Router
	log             *zap.Logger
	batch           int
	interval        time.Duration
	maxIdleInterval time.Duration
	leaseTimeout    time.Duration
}

type BootstrapUpdateOption func(*BootstrapUpdateDispatcher)

func WithBootstrapUpdateBatch(n int) BootstrapUpdateOption {
	return func(d *BootstrapUpdateDispatcher) {
		if n > 0 {
			d.batch = n
		}
	}
}

func WithBootstrapUpdateInterval(interval time.Duration) BootstrapUpdateOption {
	return func(d *BootstrapUpdateDispatcher) {
		if interval > 0 {
			d.interval = interval
		}
	}
}

func WithBootstrapUpdateLease(timeout time.Duration) BootstrapUpdateOption {
	return func(d *BootstrapUpdateDispatcher) {
		if timeout > 0 {
			d.leaseTimeout = timeout
		}
	}
}

func NewBootstrapUpdateDispatcher(router *Router, log *zap.Logger, opts ...BootstrapUpdateOption) *BootstrapUpdateDispatcher {
	if log == nil {
		log = zap.NewNop()
	}
	d := &BootstrapUpdateDispatcher{
		router:          router,
		log:             log,
		batch:           defaultBootstrapBatch,
		interval:        defaultBootstrapInterval,
		maxIdleInterval: defaultIdleDispatchMaxInterval,
		leaseTimeout:    defaultBootstrapLease,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	return d
}

func (d *BootstrapUpdateDispatcher) Run(ctx context.Context) {
	if d == nil || d.router == nil {
		return
	}
	runIdleBackoffLoop(ctx, d.interval, d.maxIdleInterval, d.DispatchOnce)
}

func (d *BootstrapUpdateDispatcher) DispatchOnce(ctx context.Context) bool {
	return d.router.publishReadyBootstrapUpdates(ctx, d.batch, d.leaseTimeout, d.log) > 0
}

func (r *Router) enqueueLoginMessageBootstrap(ctx context.Context, msg domain.Message) {
	if r.deps.BootstrapUpdates == nil || msg.OwnerUserID == 0 || msg.ID == 0 {
		return
	}
	authKeyID, hasAuthKeyID := AuthKeyIDFrom(ctx)
	sessionID, hasSessionID := SessionIDFrom(ctx)
	if !hasAuthKeyID || !hasSessionID {
		return
	}
	if _, err := r.deps.BootstrapUpdates.EnqueueLoginMessage(ctx, domain.BootstrapUpdateJob{
		Kind:         domain.BootstrapUpdateJobLoginMessage,
		UserID:       msg.OwnerUserID,
		AuthKeyID:    authKeyID,
		SessionID:    sessionID,
		MessageBoxID: msg.ID,
		Status:       domain.BootstrapUpdateJobPending,
	}); err != nil {
		r.log.Warn("enqueue bootstrap login message", zap.Int64("user_id", msg.OwnerUserID), zap.Int("message_id", msg.ID), zap.Error(err))
	}
}

func (r *Router) registerBootstrapAfterBaseline(ctx context.Context, userID int64) {
	if r.deps.BootstrapUpdates == nil || userID == 0 {
		return
	}
	authKeyID, hasAuthKeyID := AuthKeyIDFrom(ctx)
	sessionID, hasSessionID := SessionIDFrom(ctx)
	if !hasAuthKeyID || !hasSessionID {
		return
	}
	postresponse.Register(ctx, func() {
		cbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ready, err := r.deps.BootstrapUpdates.MarkReadyForSession(cbCtx, userID, authKeyID, sessionID)
		if err != nil {
			r.log.Warn("mark bootstrap updates ready", zap.Int64("user_id", userID), zap.Int64("session_id", sessionID), zap.Error(err))
			return
		}
		if ready == 0 {
			return
		}
		r.publishReadyBootstrapUpdates(cbCtx, ready, defaultBootstrapLease, r.log.Named("bootstrap"))
	})
}

func (r *Router) publishReadyBootstrapUpdates(ctx context.Context, batch int, leaseTimeout time.Duration, log *zap.Logger) int {
	if r.deps.BootstrapUpdates == nil || r.deps.Updates == nil || r.deps.Messages == nil {
		return 0
	}
	if log == nil {
		log = zap.NewNop()
	}
	jobs, err := r.deps.BootstrapUpdates.ClaimReady(ctx, batch, leaseTimeout)
	if err != nil {
		log.Warn("claim bootstrap updates", zap.Error(err))
		return 0
	}
	for _, job := range jobs {
		if err := r.publishBootstrapUpdateJob(ctx, job); err != nil {
			log.Warn("publish bootstrap update",
				zap.Int64("job_id", job.ID),
				zap.Int64("user_id", job.UserID),
				zap.String("kind", string(job.Kind)),
				zap.Error(err),
			)
			_ = r.deps.BootstrapUpdates.MarkFailed(ctx, job.ID, err.Error())
		}
	}
	return len(jobs)
}

func (r *Router) publishBootstrapUpdateJob(ctx context.Context, job domain.BootstrapUpdateJob) error {
	switch job.Kind {
	case domain.BootstrapUpdateJobLoginMessage:
		return r.publishBootstrapLoginMessage(ctx, job)
	default:
		return fmt.Errorf("unknown bootstrap update kind %q", job.Kind)
	}
}

func (r *Router) publishBootstrapLoginMessage(ctx context.Context, job domain.BootstrapUpdateJob) error {
	list, err := r.deps.Messages.GetMessages(ctx, job.UserID, []int{job.MessageBoxID})
	if err != nil {
		return fmt.Errorf("load bootstrap login message: %w", err)
	}
	if len(list.Messages) == 0 {
		return fmt.Errorf("bootstrap login message missing: box_id=%d", job.MessageBoxID)
	}
	msg := list.Messages[0]
	if msg.OwnerUserID != job.UserID || msg.ID != job.MessageBoxID {
		return fmt.Errorf("bootstrap login message mismatch: owner=%d id=%d", msg.OwnerUserID, msg.ID)
	}
	if _, _, err := r.deps.Updates.PublishNewMessage(ctx, job.UserID, msg); err != nil {
		return fmt.Errorf("publish bootstrap login message update: %w", err)
	}
	if err := r.deps.BootstrapUpdates.MarkPublished(ctx, job.ID); err != nil {
		return err
	}
	return nil
}
