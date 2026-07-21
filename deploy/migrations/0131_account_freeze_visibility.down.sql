DROP TABLE IF EXISTS public.account_freeze_notifications;

ALTER TABLE public.account_restrictions
  DROP CONSTRAINT IF EXISTS account_restrictions_version_check,
  DROP COLUMN IF EXISTS version;
