ALTER TABLE account_restrictions
	DROP CONSTRAINT IF EXISTS account_restrictions_freeze_shape_check,
	DROP COLUMN IF EXISTS frozen_since,
	DROP COLUMN IF EXISTS frozen_until,
	DROP COLUMN IF EXISTS appeal_url;

ALTER INDEX account_restrictions_frozen_idx
	RENAME TO account_send_restrictions_frozen_idx;

ALTER TABLE account_restrictions RENAME TO account_send_restrictions;
