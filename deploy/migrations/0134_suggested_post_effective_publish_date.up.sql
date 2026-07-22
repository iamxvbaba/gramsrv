-- TDesktop omits schedule_date for "Publish Now", while the approval action
-- renderer always formats an absolute publication date.  Backfill rows written
-- by the initial lifecycle implementation and keep current/history/difference
-- projections on the same effective timestamp.
UPDATE channel_messages m
SET suggested_post = jsonb_set(
        m.suggested_post,
        '{ScheduleDate}',
        to_jsonb(a.created_at),
        true
    )
FROM suggested_post_approvals a
WHERE a.schedule_date = 0
  AND a.state IN ('scheduled', 'published', 'completed', 'refunded')
  AND m.channel_id = a.monoforum_id
  AND m.id = a.suggestion_message_id
  AND COALESCE((m.suggested_post->>'Accepted')::boolean, false)
  AND COALESCE((m.suggested_post->>'ScheduleDate')::integer, 0) = 0;

UPDATE channel_messages m
SET action = jsonb_set(
        m.action,
        '{SuggestedPostScheduleDate}',
        to_jsonb(a.created_at),
        true
    )
FROM suggested_post_approvals a
WHERE a.schedule_date = 0
  AND a.state IN ('scheduled', 'published', 'completed', 'refunded')
  AND m.channel_id = a.monoforum_id
  AND m.id = a.approval_service_message_id
  AND m.action->>'Type' = 'suggested_post_approval'
  AND NOT COALESCE((m.action->>'SuggestedPostRejected')::boolean, false)
  AND NOT COALESCE((m.action->>'SuggestedPostBalanceTooLow')::boolean, false)
  AND COALESCE((m.action->>'SuggestedPostScheduleDate')::integer, 0) = 0;

UPDATE channel_update_events e
SET payload = jsonb_set(
        e.payload,
        '{message,SuggestedPost,ScheduleDate}',
        to_jsonb(a.created_at),
        true
    )
FROM suggested_post_approvals a
WHERE a.schedule_date = 0
  AND a.state IN ('scheduled', 'published', 'completed', 'refunded')
  AND e.channel_id = a.monoforum_id
  AND e.message_id = a.suggestion_message_id
  AND e.event_type = 'edit_channel_message'
  AND COALESCE((e.payload #>> '{message,SuggestedPost,Accepted}')::boolean, false)
  AND COALESCE((e.payload #>> '{message,SuggestedPost,ScheduleDate}')::integer, 0) = 0;

UPDATE channel_update_events e
SET payload = jsonb_set(
        e.payload,
        '{message,Action,SuggestedPostScheduleDate}',
        to_jsonb(a.created_at),
        true
    )
FROM suggested_post_approvals a
WHERE a.schedule_date = 0
  AND a.state IN ('scheduled', 'published', 'completed', 'refunded')
  AND e.channel_id = a.monoforum_id
  AND e.message_id = a.approval_service_message_id
  AND e.event_type = 'new_channel_message'
  AND e.payload #>> '{message,Action,Type}' = 'suggested_post_approval'
  AND NOT COALESCE((e.payload #>> '{message,Action,SuggestedPostRejected}')::boolean, false)
  AND NOT COALESCE((e.payload #>> '{message,Action,SuggestedPostBalanceTooLow}')::boolean, false)
  AND COALESCE((e.payload #>> '{message,Action,SuggestedPostScheduleDate}')::integer, 0) = 0;

UPDATE suggested_post_approvals
SET schedule_date = created_at,
    updated_at = GREATEST(updated_at, created_at)
WHERE schedule_date = 0
  AND state IN ('scheduled', 'published', 'completed', 'refunded');

ALTER TABLE suggested_post_approvals
    DROP CONSTRAINT suggested_post_approvals_shape_check;

ALTER TABLE suggested_post_approvals
    ADD CONSTRAINT suggested_post_approvals_shape_check CHECK (
        monoforum_id>0 AND suggestion_message_id>0 AND parent_channel_id>0 AND
        actor_user_id>0 AND payer_user_id>0 AND created_at>0 AND updated_at>=created_at AND
        state IN ('balance_low','rejected','scheduled','published','completed','refunded') AND
        price_kind IN ('','stars','ton') AND price_amount>=0 AND price_nanos BETWEEN 0 AND 999999999 AND
        ((price_kind='' AND price_amount=0 AND price_nanos=0) OR
         (price_kind='stars' AND price_amount>0) OR
         (price_kind='ton' AND price_amount>0 AND price_nanos=0)) AND
        schedule_date>=0 AND
        (state IN ('balance_low','rejected') OR schedule_date>0) AND
        approval_service_message_id>=0 AND published_message_id>=0 AND
        settlement_due>=0 AND final_service_message_id>=0
    );
