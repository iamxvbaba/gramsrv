CREATE OR REPLACE FUNCTION telesrv_notify_account_settings_read_model()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    owner_id bigint;
BEGIN
    owner_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.user_id ELSE NEW.user_id END;
    IF owner_id IS NOT NULL AND owner_id > 0 THEN
        PERFORM telesrv_bump_read_model_version(
            'account_settings',
            owner_id,
            'user',
            owner_id
        );
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

DROP TRIGGER IF EXISTS account_settings_read_model_changed ON account_settings;
CREATE TRIGGER account_settings_read_model_changed
AFTER INSERT OR UPDATE OR DELETE ON account_settings
FOR EACH ROW
EXECUTE FUNCTION telesrv_notify_account_settings_read_model();
