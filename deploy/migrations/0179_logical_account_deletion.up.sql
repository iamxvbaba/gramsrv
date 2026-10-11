-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
-- Human account deletion is a logical user tombstone.  The deleted user keeps
-- all relationship/history rows, so the users UPDATE must not fan out through
-- every reverse contact, dialog and channel membership.  A dedicated event
-- invalidates the base-user and RPC projection caches as one coarse boundary.

-- Collectible phone ownership is an account asset, not the editable users.phone
-- identity field.  Logical deletion preserves it; physical user deletion keeps
-- the separate 0171 release trigger.
DROP TRIGGER IF EXISTS users_release_collectible_phone_on_soft_delete ON public.users;

CREATE OR REPLACE FUNCTION public.telesrv_notify_user_base_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    changed_id BIGINT;
    projection_changed BOOLEAN;
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.deleted_at IS NULL
       AND NEW.deleted_at IS NOT NULL THEN
        PERFORM telesrv_bump_read_model_version('user_deleted', NEW.id, 'user', NEW.id);
        RETURN NULL;
    END IF;

    IF TG_OP = 'DELETE' THEN
        changed_id := OLD.id;
        projection_changed := true;
    ELSE
        changed_id := NEW.id;
        IF TG_OP = 'INSERT' THEN
            projection_changed := true;
        ELSE
            projection_changed :=
                OLD.access_hash IS DISTINCT FROM NEW.access_hash OR
                OLD.phone IS DISTINCT FROM NEW.phone OR
                OLD.first_name IS DISTINCT FROM NEW.first_name OR
                OLD.last_name IS DISTINCT FROM NEW.last_name OR
                OLD.username IS DISTINCT FROM NEW.username OR
                OLD.country_code IS DISTINCT FROM NEW.country_code OR
                OLD.verified IS DISTINCT FROM NEW.verified OR
                OLD.support IS DISTINCT FROM NEW.support OR
                OLD.about IS DISTINCT FROM NEW.about OR
                OLD.default_history_ttl_period IS DISTINCT FROM NEW.default_history_ttl_period OR
                OLD.is_bot IS DISTINCT FROM NEW.is_bot OR
                OLD.bot_info_version IS DISTINCT FROM NEW.bot_info_version OR
                OLD.premium_expires_at IS DISTINCT FROM NEW.premium_expires_at OR
                OLD.emoji_status_document_id IS DISTINCT FROM NEW.emoji_status_document_id OR
                OLD.emoji_status_until IS DISTINCT FROM NEW.emoji_status_until OR
                OLD.color_set IS DISTINCT FROM NEW.color_set OR
                OLD.color IS DISTINCT FROM NEW.color OR
                OLD.color_background_emoji_id IS DISTINCT FROM NEW.color_background_emoji_id OR
                OLD.profile_color_set IS DISTINCT FROM NEW.profile_color_set OR
                OLD.profile_color IS DISTINCT FROM NEW.profile_color OR
                OLD.profile_color_background_emoji_id IS DISTINCT FROM NEW.profile_color_background_emoji_id;
        END IF;
    END IF;

    IF projection_changed THEN
        PERFORM telesrv_bump_read_model_version('user_base', changed_id, 'user', changed_id);
        IF TG_OP = 'INSERT' THEN
            PERFORM telesrv_bump_read_model_version('contact_account', changed_id, 'user', changed_id);
        END IF;
        PERFORM telesrv_bump_read_model_version('contact_account', c.user_id, 'user', c.user_id)
        FROM contacts c
        WHERE c.contact_user_id = changed_id;
        PERFORM telesrv_bump_private_dialog_light_for_user(changed_id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.telesrv_notify_user_channel_participants_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    changed_id BIGINT;
    old_id BIGINT;
    projection_changed BOOLEAN;
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.deleted_at IS NULL
       AND NEW.deleted_at IS NOT NULL THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'DELETE' THEN
        changed_id := OLD.id;
        projection_changed := true;
    ELSE
        changed_id := NEW.id;
        IF TG_OP = 'INSERT' THEN
            projection_changed := true;
            PERFORM telesrv_bump_read_model_version('contact_account', changed_id, 'user', changed_id);
        ELSE
            old_id := OLD.id;
            projection_changed :=
                OLD.access_hash IS DISTINCT FROM NEW.access_hash OR
                OLD.phone IS DISTINCT FROM NEW.phone OR
                OLD.first_name IS DISTINCT FROM NEW.first_name OR
                OLD.last_name IS DISTINCT FROM NEW.last_name OR
                OLD.username IS DISTINCT FROM NEW.username OR
                OLD.country_code IS DISTINCT FROM NEW.country_code OR
                OLD.verified IS DISTINCT FROM NEW.verified OR
                OLD.support IS DISTINCT FROM NEW.support OR
                OLD.about IS DISTINCT FROM NEW.about OR
                OLD.default_history_ttl_period IS DISTINCT FROM NEW.default_history_ttl_period OR
                OLD.is_bot IS DISTINCT FROM NEW.is_bot OR
                OLD.bot_info_version IS DISTINCT FROM NEW.bot_info_version OR
                OLD.premium_expires_at IS DISTINCT FROM NEW.premium_expires_at OR
                OLD.emoji_status_document_id IS DISTINCT FROM NEW.emoji_status_document_id OR
                OLD.emoji_status_until IS DISTINCT FROM NEW.emoji_status_until OR
                OLD.color_set IS DISTINCT FROM NEW.color_set OR
                OLD.color IS DISTINCT FROM NEW.color OR
                OLD.color_background_emoji_id IS DISTINCT FROM NEW.color_background_emoji_id OR
                OLD.profile_color_set IS DISTINCT FROM NEW.profile_color_set OR
                OLD.profile_color IS DISTINCT FROM NEW.profile_color OR
                OLD.profile_color_background_emoji_id IS DISTINCT FROM NEW.profile_color_background_emoji_id;
            IF old_id IS DISTINCT FROM changed_id THEN
                PERFORM telesrv_bump_channel_participants_for_user(old_id);
            END IF;
        END IF;
    END IF;

    IF projection_changed THEN
        PERFORM telesrv_bump_channel_participants_for_user(changed_id);
    END IF;
    RETURN NULL;
END;
$$;
