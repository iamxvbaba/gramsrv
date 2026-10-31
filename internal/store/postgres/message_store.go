package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// MessageStore implements store.MessageStore with PostgreSQL.
type MessageStore struct {
	db               sqlcgen.DBTX
	q                *sqlcgen.Queries
	boxIDs           store.BoxIDAllocator
	privateSendLanes *userLaneActor
	log              *zap.Logger
}

type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// MessageStoreOption adjusts the PostgreSQL MessageStore dependencies.
type MessageStoreOption func(*MessageStore)

// WithMessageAllocators injects the Redis-backed box_id allocator.
func WithMessageAllocators(boxIDs store.BoxIDAllocator) MessageStoreOption {
	return func(s *MessageStore) {
		s.boxIDs = boxIDs
	}
}

// WithMessageLogger injects the message store logger used to trace atomic message and pts writes.
func WithMessageLogger(log *zap.Logger) MessageStoreOption {
	return func(s *MessageStore) {
		s.log = log
	}
}

// NewMessageStore creates a MessageStore on top of a pgx pool (or transaction).
func NewMessageStore(db sqlcgen.DBTX, opts ...MessageStoreOption) *MessageStore {
	s := &MessageStore{db: db, q: sqlcgen.New(db), privateSendLanes: defaultPrivateSendLaneActor}
	for _, opt := range opts {
		opt(s)
	}
	if s.log == nil {
		s.log = zap.NewNop()
	}
	if s.boxIDs == nil {
		s.boxIDs = pgBoxIDAllocator{s: s}
	}
	return s
}
