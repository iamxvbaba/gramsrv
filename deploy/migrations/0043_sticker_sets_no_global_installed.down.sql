UPDATE sticker_sets
SET installed = true,
    installed_date = CASE WHEN installed_date = 0 THEN 1 ELSE installed_date END
WHERE set_kind <> 'system'
  AND creator_user_id IS NULL
  AND deleted = false;
