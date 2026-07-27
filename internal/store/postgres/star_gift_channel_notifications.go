package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

const (
	maxChannelStarGiftNotificationRecipients = 256
	channelStarGiftNotificationLeaseSeconds  = 60
)

type channelStarGiftNotificationJob struct {
	SavedGiftID  int64
	TargetUserID int64
	GiftDate     int
	Action       domain.MessageStarGiftAction
	Attempts     int
}

func enqueueChannelStarGiftNotifications(
	ctx context.Context,
	tx pgx.Tx,
	savedGiftID int64,
	channelID int64,
	giftDate int,
	action *domain.MessageStarGiftAction,
) error {
	if savedGiftID <= 0 || channelID <= 0 || giftDate <= 0 || action == nil ||
		action.PeerChannelID != channelID || action.SavedID <= 0 {
		return fmt.Errorf("enqueue channel star gift notifications: invalid intent")
	}
	actionJSON, err := json.Marshal(action)
	if err != nil {
		return fmt.Errorf("encode channel star gift notification: %w", err)
	}
	_, err = tx.Exec(ctx, `
WITH candidates AS (
    SELECT creator_user_id AS user_id
    FROM channels
    WHERE id=$2 AND NOT deleted
    UNION
    SELECT user_id
    FROM channel_members
    WHERE channel_id=$2 AND status='active'
      AND (role='creator' OR (
          role='admin'
          AND COALESCE((admin_rights->>'PostMessages')::boolean,false)
      ))
), bounded AS (
    SELECT user_id FROM candidates
    WHERE user_id>0
    ORDER BY user_id
    LIMIT $5
)
INSERT INTO star_gift_channel_notification_jobs
    (saved_gift_id,target_user_id,gift_date,action,next_attempt_at)
SELECT $1,bounded.user_id,$3,$4::jsonb,$3
FROM bounded
LEFT JOIN star_gift_notification_settings settings
  ON settings.user_id=bounded.user_id AND settings.channel_id=$2
WHERE COALESCE(settings.enabled,TRUE)
ON CONFLICT(saved_gift_id,target_user_id) DO NOTHING`,
		savedGiftID, channelID, giftDate, string(actionJSON), maxChannelStarGiftNotificationRecipients)
	if err != nil {
		return fmt.Errorf("enqueue channel star gift notifications: %w", err)
	}
	return nil
}

func (s *StarGiftLifecycleStore) dispatchChannelStarGiftNotifications(
	ctx context.Context,
	now int,
	limit int,
	savedGiftID int64,
) (int, error) {
	if s == nil || s.db == nil || s.messages == nil || now <= 0 || limit <= 0 {
		return 0, domain.ErrStarGiftUnavailable
	}
	if limit > maxChannelStarGiftNotificationRecipients {
		limit = maxChannelStarGiftNotificationRecipients
	}
	jobs, err := s.claimChannelStarGiftNotificationJobs(ctx, now, limit, savedGiftID)
	if err != nil {
		return 0, err
	}
	var firstErr error
	delivered := 0
	for _, job := range jobs {
		messageID, sendErr := s.deliverChannelStarGiftNotification(ctx, job)
		if sendErr == nil {
			tag, markErr := s.db.Exec(ctx, `UPDATE star_gift_channel_notification_jobs
SET delivered_at=$3,message_id=$4,lease_until=0,last_error='',updated_at=now()
WHERE saved_gift_id=$1 AND target_user_id=$2 AND delivered_at=0`,
				job.SavedGiftID, job.TargetUserID, now, messageID)
			if markErr == nil && tag.RowsAffected() == 1 {
				delivered++
				continue
			}
			if markErr == nil {
				markErr = fmt.Errorf("channel star gift notification job disappeared")
			}
			sendErr = markErr
		}
		if firstErr == nil {
			firstErr = sendErr
		}
		retryAt := now + channelStarGiftNotificationRetrySeconds(job.Attempts)
		_, _ = s.db.Exec(ctx, `UPDATE star_gift_channel_notification_jobs
SET next_attempt_at=$3,lease_until=0,last_error=$4,updated_at=now()
WHERE saved_gift_id=$1 AND target_user_id=$2 AND delivered_at=0`,
			job.SavedGiftID, job.TargetUserID, retryAt, truncateStarGiftNotificationError(sendErr))
	}
	return delivered, firstErr
}

func (s *StarGiftLifecycleStore) claimChannelStarGiftNotificationJobs(
	ctx context.Context,
	now int,
	limit int,
	savedGiftID int64,
) ([]channelStarGiftNotificationJob, error) {
	jobs := make([]channelStarGiftNotificationJob, 0, limit)
	err := withTx(ctx, s.db, "claim channel star gift notifications", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
WITH picked AS (
    SELECT saved_gift_id,target_user_id
    FROM star_gift_channel_notification_jobs
    WHERE delivered_at=0 AND next_attempt_at<=$1 AND lease_until<$1
      AND ($3::bigint=0 OR saved_gift_id=$3)
    ORDER BY next_attempt_at,saved_gift_id,target_user_id
    FOR UPDATE SKIP LOCKED
    LIMIT $2
)
UPDATE star_gift_channel_notification_jobs job
SET attempts=job.attempts+1,
    lease_until=$1+$4,
    updated_at=now()
FROM picked
WHERE job.saved_gift_id=picked.saved_gift_id
  AND job.target_user_id=picked.target_user_id
RETURNING job.saved_gift_id,job.target_user_id,job.gift_date,job.action,job.attempts`,
			now, limit, savedGiftID, channelStarGiftNotificationLeaseSeconds)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var job channelStarGiftNotificationJob
			var actionJSON []byte
			if err := rows.Scan(&job.SavedGiftID, &job.TargetUserID, &job.GiftDate, &actionJSON, &job.Attempts); err != nil {
				return err
			}
			if err := json.Unmarshal(actionJSON, &job.Action); err != nil {
				return fmt.Errorf("decode channel star gift notification: %w", err)
			}
			if job.SavedGiftID <= 0 || job.TargetUserID <= 0 || job.GiftDate <= 0 ||
				job.Action.PeerChannelID <= 0 || job.Action.SavedID <= 0 {
				return fmt.Errorf("decode channel star gift notification: invalid intent")
			}
			jobs = append(jobs, job)
		}
		return rows.Err()
	})
	return jobs, err
}

func (s *StarGiftLifecycleStore) deliverChannelStarGiftNotification(
	ctx context.Context,
	job channelStarGiftNotificationJob,
) (int, error) {
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf(
		"telesrv:channel-star-gift-notification:v1:%d:%d",
		job.SavedGiftID, job.TargetUserID,
	)))
	action := job.Action
	request := domain.SendPrivateTextRequest{
		SenderUserID:           domain.OfficialSystemUserID,
		RecipientUserID:        job.TargetUserID,
		RandomID:               lifecycleCommandRandomID("channel-star-gift-notification", job.SavedGiftID, job.TargetUserID),
		Date:                   job.GiftDate,
		IdempotencyFingerprint: fingerprint[:],
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind:     domain.MessageServiceActionStarGift,
			StarGift: &action,
		}},
	}
	sent, err := s.messages.sendPrivateTextWithHooks(ctx, request, privateSendTxHooks{
		after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
			if sent.RecipientMessage.ID <= 0 {
				return fmt.Errorf("channel star gift notification missing recipient box")
			}
			return registerChannelNotificationMessageRef(ctx, tx, job.TargetUserID,
				sent.RecipientMessage.ID, job.SavedGiftID)
		},
	})
	if err != nil {
		return 0, err
	}
	if sent.RecipientMessage.ID <= 0 {
		return 0, fmt.Errorf("channel star gift notification replay missing recipient box")
	}
	return sent.RecipientMessage.ID, nil
}

func channelStarGiftNotificationRetrySeconds(attempt int) int {
	if attempt < 1 {
		attempt = 1
	}
	delay := attempt * attempt * 5
	if delay > 3600 {
		return 3600
	}
	return delay
}

func truncateStarGiftNotificationError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	runes := []rune(value)
	if len(runes) > 1000 {
		value = string(runes[:1000])
	}
	return value
}
