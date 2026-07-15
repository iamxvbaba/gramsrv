ALTER TABLE account_send_restrictions RENAME TO account_restrictions;

ALTER INDEX account_send_restrictions_frozen_idx
	RENAME TO account_restrictions_frozen_idx;

ALTER TABLE account_restrictions
	ADD COLUMN frozen_since timestamptz,
	ADD COLUMN frozen_until timestamptz,
	ADD COLUMN appeal_url text NOT NULL DEFAULT '';

-- Existing send-frozen rows become valid account freezes explicitly during
-- migration; read paths never synthesize the missing state.
UPDATE account_restrictions
SET frozen_since = updated_at,
	frozen_until = updated_at + interval '7 days',
	appeal_url = 'https://t.me/SpamBot'
WHERE frozen;

UPDATE account_restrictions
SET frozen_since = NULL,
	frozen_until = NULL,
	appeal_url = ''
WHERE NOT frozen;

ALTER TABLE account_restrictions
	ADD CONSTRAINT account_restrictions_freeze_shape_check CHECK (
		(frozen AND frozen_since IS NOT NULL AND frozen_until IS NOT NULL
			AND frozen_until > frozen_since
			AND frozen_until <= to_timestamp(2147483647)
			AND appeal_url <> '' AND octet_length(appeal_url) <= 2048)
		OR
		(NOT frozen AND frozen_since IS NULL AND frozen_until IS NULL
			AND appeal_url = '')
	);
