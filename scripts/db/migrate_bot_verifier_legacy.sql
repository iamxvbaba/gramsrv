-- Recover verifier settings from the pre-0155 table shape, if it is present.
-- This is intentionally idempotent and does nothing on a current schema.
-- Review the transaction output before committing in production.
BEGIN;

DO $$
DECLARE
    legacy_table CONSTANT text := 'public.bot_verifier_settings_legacy_0155';
    current_table CONSTANT text := 'public.bot_verifier_settings';
    has_legacy boolean;
    has_old_shape boolean;
BEGIN
    has_legacy := to_regclass(legacy_table) IS NOT NULL;
    IF to_regclass(current_table) IS NULL THEN
        RAISE EXCEPTION 'current bot_verifier_settings table is missing; apply migration 0155 first';
    END IF;

    IF NOT has_legacy THEN
        SELECT EXISTS (
            SELECT 1
            FROM information_schema.columns
            WHERE table_schema = 'public'
              AND table_name = 'bot_verifier_settings'
              AND column_name = 'bot_user_id'
        ) INTO has_old_shape;
        IF has_old_shape THEN
            RAISE EXCEPTION 'legacy bot_verifier_settings is still the active table; rename it to bot_verifier_settings_legacy_0155, then apply migration 0155 before rerunning this utility';
        END IF;
        RAISE NOTICE 'no bot_verifier_settings_legacy_0155 table found; nothing to migrate';
        RETURN;
    END IF;

    EXECUTE $sql$
        INSERT INTO public.bot_verifier_settings
            (bot_id, icon_document_id, company_name, default_description,
             can_modify_custom_description, enabled, granted_by, grant_reason,
             created_at, updated_at)
        SELECT bot_user_id, icon, company, custom_description,
               can_modify_custom_description, enabled, 'legacy-0155', '',
               created_at, updated_at
        FROM public.bot_verifier_settings_legacy_0155
        ON CONFLICT (bot_id) DO UPDATE SET
            icon_document_id = EXCLUDED.icon_document_id,
            company_name = EXCLUDED.company_name,
            default_description = EXCLUDED.default_description,
            can_modify_custom_description = EXCLUDED.can_modify_custom_description,
            enabled = EXCLUDED.enabled,
            updated_at = EXCLUDED.updated_at
    $sql$;
END
$$;

COMMIT;
