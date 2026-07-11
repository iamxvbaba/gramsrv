package postgres

import (
	"context"
	"fmt"
	"time"
)

// DeleteExpiredLoginCodeDeliveries seek-deletes compact idempotency receipts
// whose corresponding opaque codes are no longer usable. Message/update facts
// are deliberately retained; only the replay receipt is ephemeral.
func (s *MessageStore) DeleteExpiredLoginCodeDeliveries(ctx context.Context, expiredBefore time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	tag, err := s.db.Exec(ctx, `
WITH doomed AS (
  SELECT delivery_key
  FROM login_code_message_deliveries
  WHERE expires_at <= $1
  ORDER BY expires_at, delivery_key
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
DELETE FROM login_code_message_deliveries AS d
USING doomed
WHERE d.delivery_key = doomed.delivery_key`, expiredBefore.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired login code delivery receipts: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
