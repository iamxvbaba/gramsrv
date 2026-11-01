package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

var ErrBoxIDAllocatorMissing = errors.New("postgres message store: Redis box id allocator is required")

type missingBoxIDAllocator struct{}

func (missingBoxIDAllocator) NextBoxID(context.Context, int64) (int, error) {
	return 0, ErrBoxIDAllocatorMissing
}

func (missingBoxIDAllocator) NextBoxIDs(context.Context, []int64) (map[int64]int, error) {
	return nil, ErrBoxIDAllocatorMissing
}

func (missingBoxIDAllocator) CurrentBoxID(context.Context, int64) (int, error) {
	return 0, ErrBoxIDAllocatorMissing
}

// MessageStore implements store.MessageStore with PostgreSQL.
type MessageStore struct {
	db                     sqlcgen.DBTX
	q                      *sqlcgen.Queries
	boxIDs                 store.BoxIDAllocator
	readModelInvalidations store.ReadModelInvalidationPublisher
	batchObserver          PlainPrivateSendBatchObserver
	log                    *zap.Logger
}

// PlainPrivateSendBatchObserver receives only fixed-shape Core capacity data;
// request identities and message contents never cross this boundary.
type PlainPrivateSendBatchObserver interface {
	PlainPrivateSendBatch(duration time.Duration, tasks int, err error)
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

// WithMessageReadModelInvalidations injects the Redis publisher used after a
// plain-send commit. The same exact keys are also carried by dispatch_outbox and
// republished by Egress, so this publisher is the read-your-write accelerator,
// not the crash-recovery fact.
func WithMessageReadModelInvalidations(publisher store.ReadModelInvalidationPublisher) MessageStoreOption {
	return func(s *MessageStore) {
		s.readModelInvalidations = publisher
	}
}

// WithPlainPrivateSendBatchObserver installs the production batch capacity
// observer. It has no effect on transaction semantics or admission.
func WithPlainPrivateSendBatchObserver(observer PlainPrivateSendBatchObserver) MessageStoreOption {
	return func(s *MessageStore) {
		s.batchObserver = observer
	}
}

// NewMessageStore creates a MessageStore on top of a pgx pool (or transaction).
func NewMessageStore(db sqlcgen.DBTX, opts ...MessageStoreOption) *MessageStore {
	s := &MessageStore{db: db, q: sqlcgen.New(db)}
	for _, opt := range opts {
		opt(s)
	}
	if s.log == nil {
		s.log = zap.NewNop()
	}
	if s.boxIDs == nil {
		s.boxIDs = missingBoxIDAllocator{}
	}
	return s
}
