-- v2 编号 0242；移植自 main 0213_private_dialog_ttl_snapshot（main e5971805），见 docs/main-v2-sync.md。
-- Existing dialog zero is a deliberate disabled timer. Only a new private
-- conversation inherits its initiator's default; both dialog projections agree.
-- Callers hold the ordered user locks while creating dialog/message rows.
CREATE FUNCTION private_dialog_initial_ttl(sender_id bigint, recipient_id bigint)
RETURNS integer LANGUAGE sql STABLE AS $$
  SELECT CASE WHEN sender_id = recipient_id THEN 0 ELSE COALESCE(
    (SELECT ttl_period FROM dialogs WHERE user_id = sender_id AND peer_type = 'user' AND peer_id = recipient_id),
    (SELECT ttl_period FROM dialogs WHERE user_id = recipient_id AND peer_type = 'user' AND peer_id = sender_id),
    (SELECT default_history_ttl_period FROM users WHERE id = sender_id AND deleted_at IS NULL)
  ) END;
$$;
