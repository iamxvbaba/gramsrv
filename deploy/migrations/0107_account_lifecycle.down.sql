DROP TRIGGER IF EXISTS account_settings_ttl_trigger ON public.account_settings;
DROP FUNCTION IF EXISTS public.telesrv_account_settings_ttl_trigger();
DROP TRIGGER IF EXISTS users_account_delete_at_trigger ON public.users;
DROP FUNCTION IF EXISTS public.telesrv_users_account_delete_at_trigger();
DROP FUNCTION IF EXISTS public.telesrv_account_delete_at(timestamp with time zone, bigint, integer);

DROP TRIGGER IF EXISTS account_passwords_changed_at_trigger ON public.account_passwords;
DROP FUNCTION IF EXISTS public.telesrv_password_changed_at_trigger();
ALTER TABLE public.account_passwords DROP COLUMN IF EXISTS password_changed_at;

ALTER TABLE public.account_settings
  DROP CONSTRAINT account_settings_account_ttl_days_check,
  ADD CONSTRAINT account_settings_account_ttl_days_check CHECK (account_ttl_days > 0);

DROP TABLE IF EXISTS public.account_deletion_notifications;
DROP TABLE IF EXISTS public.account_deletion_requests;
DROP INDEX IF EXISTS public.dialogs_user_peer_reverse_idx;

DROP INDEX IF EXISTS public.users_account_delete_due_idx;
ALTER TABLE public.users DROP CONSTRAINT IF EXISTS users_deletion_state_check;
ALTER TABLE public.users
  DROP COLUMN IF EXISTS account_delete_at,
  DROP COLUMN IF EXISTS deletion_reason,
  DROP COLUMN IF EXISTS deletion_source,
  DROP COLUMN IF EXISTS deleted_at;
