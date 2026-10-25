-- v2 编号 0235；移植自 main 0206_dialog_read_model_deadlock_lock（main 0009095c / 4cedb93e）。函数体与 v2 0221
-- 的 telesrv_bump_dialog_light 一致后加锁；v2 批量成员路径（0222/0223）不经过本函数。见 docs/main-v2-sync.md。
-- Concurrent channel/private dialog mutations (mark-mentions-read, reaction
-- read receipts, dialog upserts) fire the dialog read-model triggers. Each
-- transaction bumps read_model_versions rows for 'dialog_light' and then
-- 'dialog_owner'; two such transactions can lock those two rows in inverted
-- order relative to the channel_dialogs / channel_unread_mentions rows they
-- already hold, producing an AB-BA deadlock (Postgres aborts one side with
-- 40P01). dialog_owner was added in 0195, widening the lock set and exposing
-- the race.
--
-- The advisory lock is owner-scoped, not (owner, peer): the dialog_owner row
-- is a single row shared by every peer of the owner, so per-peer advisory
-- locks still deadlock when each of two transactions bumps two peers of the
-- same owner -- txn A holds advisory(owner, A) + dialog_owner, txn B holds
-- advisory(owner, B) and waits on dialog_owner, then A waits on
-- advisory(owner, B). Serializing all dialog read-model writers of one owner
-- behind one key makes the lock ordering total per owner and Postgres reports
-- no 40P01. Every per-row dialog trigger funnels into telesrv_bump_dialog_light,
-- so a single lock covers channel dialogs, private dialogs, channel-membership
-- dialog bumps and contact bumps. The membership batch path keeps its
-- deterministic ORDER BY lock order (0196) and does not contend through this
-- helper; the lock is reentrant within a transaction, so a statement that
-- bumps several peers of the same owner acquires it only once.

CREATE OR REPLACE FUNCTION public.telesrv_bump_dialog_light(
    p_owner_user_id bigint,
    p_peer_type text,
    p_peer_id bigint
)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF COALESCE(p_owner_user_id, 0) = 0
       OR COALESCE(p_peer_id, 0) = 0
       OR COALESCE(p_peer_type, '') = ''
    THEN
        RETURN;
    END IF;

    PERFORM pg_advisory_xact_lock(
        hashtextextended('telesrv_dialog_owner:' || p_owner_user_id, 0)
    );

    PERFORM public.telesrv_bump_read_model_version(
        'dialog_light', p_owner_user_id, p_peer_type, p_peer_id
    );
    PERFORM public.telesrv_bump_dialog_owner(p_owner_user_id);
END;
$$;