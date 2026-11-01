-- Bot profile changes (name/about/description/commands/menu_button) all bump
-- users.bot_info_version (BumpBotInfoVersion, see bot.sql). The group-info page's
-- ChannelFull.bot_info is cached by the RPC layer's channelFullBotInfoCache
-- (key = viewer+channel, which cannot locate by bot), and its cross-instance
-- invalidation previously hung only on channel_base/channel_member events -- a
-- bot changing its own profile (via the local BotFather path, or another
-- instance's bots.* RPC) would not invalidate that cache, leaving the bot's
-- about/commands stale for up to 30 minutes (TTL) in the group-info page.
--
-- Here a separate 'bot_full' read-model event is emitted for bot profile changes;
-- ReadModelChangeListener flushes channelFullBotInfoCache upon receipt (covering
-- both the local BotFather path and the cross-instance bots.* RPC path).
-- This complements the existing user_base event (which covers the RPC projection /
-- Redis user:base / bot profile caches) without touching the user_base hot path.

CREATE FUNCTION public.telesrv_notify_bot_full_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.is_bot AND (OLD.bot_info_version IS DISTINCT FROM NEW.bot_info_version) THEN
        PERFORM telesrv_bump_read_model_version('bot_full', NEW.id, 'user', NEW.id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER users_bot_full_read_model_changed
    AFTER UPDATE ON public.users
    FOR EACH ROW
    EXECUTE FUNCTION public.telesrv_notify_bot_full_read_model();
