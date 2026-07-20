-- 0001_init: telesrv 全量初始 schema（由 0001-0140 迁移链压缩而来，2026-06-19）。
-- 全新项目无生产数据，故不保留逐迁移演进史；本文件 = 迁移链终态 schema（规范形式）+ 规范 seed。
-- 生成：干净库跑满 0001-0140 → 去除 0077 遗留死表 dispatch_outbox_partitioned_legacy →
--       pg_dump --schema-only + --column-inserts seed；剔除 psql 元命令与 search_path 空重置。
-- 功能与旧迁移链终态等价（已用全套 PG 集成测试对照验证）。回退见 0001_init.down.sql。

-- 触发器函数体内为非限定调用，须在 public 下解析。
SET search_path = public;

--
-- PostgreSQL database dump
--


-- Dumped from database version 17.10
-- Dumped by pg_dump version 17.10

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: pg_trgm; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;


--
-- Name: EXTENSION pg_trgm; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pg_trgm IS 'text similarity measurement and index searching based on trigrams';


--
-- Name: telesrv_adjust_channel_media_category_count(bigint, smallint, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_adjust_channel_media_category_count(p_channel_id bigint, p_category smallint, p_delta integer) RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF p_channel_id IS NULL OR p_channel_id = 0
       OR p_category IS NULL OR p_category <= 0
       OR p_delta = 0 THEN
        RETURN;
    END IF;

    IF p_delta > 0 THEN
        INSERT INTO channel_media_category_counts (channel_id, category, media_count, updated_at)
        VALUES (p_channel_id, p_category, p_delta, now())
        ON CONFLICT (channel_id, category) DO UPDATE SET
            media_count = channel_media_category_counts.media_count + EXCLUDED.media_count,
            updated_at = now();
        RETURN;
    END IF;

    UPDATE channel_media_category_counts
    SET media_count = GREATEST(0, media_count + p_delta),
        updated_at = now()
    WHERE channel_id = p_channel_id
      AND category = p_category;

    DELETE FROM channel_media_category_counts
    WHERE channel_id = p_channel_id
      AND category = p_category
      AND media_count <= 0;
END;
$$;


--
-- Name: telesrv_adjust_private_media_category_count(bigint, bigint, smallint, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_adjust_private_media_category_count(p_owner_user_id bigint, p_peer_id bigint, p_category smallint, p_delta integer) RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF p_owner_user_id IS NULL OR p_owner_user_id = 0
       OR p_peer_id IS NULL OR p_peer_id = 0
       OR p_category IS NULL OR p_category <= 0
       OR p_delta = 0 THEN
        RETURN;
    END IF;

    IF p_delta > 0 THEN
        INSERT INTO private_media_category_counts (owner_user_id, peer_id, category, media_count, updated_at)
        VALUES (p_owner_user_id, p_peer_id, p_category, p_delta, now())
        ON CONFLICT (owner_user_id, peer_id, category) DO UPDATE SET
            media_count = private_media_category_counts.media_count + EXCLUDED.media_count,
            updated_at = now();
        RETURN;
    END IF;

    UPDATE private_media_category_counts
    SET media_count = GREATEST(0, media_count + p_delta),
        updated_at = now()
    WHERE owner_user_id = p_owner_user_id
      AND peer_id = p_peer_id
      AND category = p_category;

    DELETE FROM private_media_category_counts
    WHERE owner_user_id = p_owner_user_id
      AND peer_id = p_peer_id
      AND category = p_category
      AND media_count <= 0;
END;
$$;


--
-- Name: telesrv_bump_channel_participants_for_user(bigint); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_bump_channel_participants_for_user(p_user_id bigint) RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF COALESCE(p_user_id, 0) = 0 THEN
        RETURN;
    END IF;

    PERFORM telesrv_bump_read_model_version('channel_participants', 0, 'channel', member_channels.channel_id)
    FROM (
        SELECT DISTINCT channel_id
        FROM channel_members
        WHERE user_id = p_user_id
    ) AS member_channels;
END;
$$;


--
-- Name: telesrv_bump_contact_accounts_for_user(bigint); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_bump_contact_accounts_for_user(p_user_id bigint) RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF COALESCE(p_user_id, 0) = 0 THEN
        RETURN;
    END IF;

    PERFORM telesrv_bump_read_model_version('contact_account', c.user_id, 'user', c.user_id)
    FROM contacts c
    WHERE c.contact_user_id = p_user_id;
END;
$$;


--
-- Name: telesrv_bump_dialog_light(bigint, text, bigint); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_bump_dialog_light(p_owner_user_id bigint, p_peer_type text, p_peer_id bigint) RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF COALESCE(p_owner_user_id, 0) = 0 OR COALESCE(p_peer_id, 0) = 0 OR COALESCE(p_peer_type, '') = '' THEN
        RETURN;
    END IF;

    PERFORM telesrv_bump_read_model_version('dialog_light', p_owner_user_id, p_peer_type, p_peer_id);
END;
$$;


--
-- Name: telesrv_bump_private_dialog_light_for_user(bigint); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_bump_private_dialog_light_for_user(p_user_id bigint) RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF COALESCE(p_user_id, 0) = 0 THEN
        RETURN;
    END IF;

    PERFORM telesrv_bump_dialog_light(d.user_id, d.peer_type, d.peer_id)
    FROM dialogs d
    WHERE d.peer_type = 'user'
      AND d.peer_id = p_user_id;
END;
$$;


--
-- Name: telesrv_bump_read_model_version(text, bigint, text, bigint); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_bump_read_model_version(p_model text, p_owner_user_id bigint, p_peer_type text DEFAULT ''::text, p_peer_id bigint DEFAULT 0) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    next_version BIGINT;
    next_hash BIGINT;
BEGIN
    INSERT INTO read_model_versions (model, owner_user_id, peer_type, peer_id, version, hash, updated_at)
    VALUES (
        p_model,
        COALESCE(p_owner_user_id, 0),
        COALESCE(p_peer_type, ''),
        COALESCE(p_peer_id, 0),
        1,
        telesrv_random_read_model_hash(),
        now()
    )
    ON CONFLICT (model, owner_user_id, peer_type, peer_id) DO UPDATE SET
        version = read_model_versions.version + 1,
        hash = telesrv_random_read_model_hash(),
        updated_at = EXCLUDED.updated_at
    RETURNING version, hash INTO next_version, next_hash;

    PERFORM pg_notify(
        'telesrv_read_model_changed',
        json_build_object(
            'model', p_model,
            'owner_user_id', COALESCE(p_owner_user_id, 0),
            'peer_type', COALESCE(p_peer_type, ''),
            'peer_id', COALESCE(p_peer_id, 0),
            'version', next_version,
            'hash', next_hash
        )::text
    );
END;
$$;


--
-- Name: telesrv_maintain_channel_media_category_counts(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_maintain_channel_media_category_counts() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF telesrv_visible_channel_media_message(OLD.channel_id, OLD.id) THEN
            PERFORM telesrv_adjust_channel_media_category_count(OLD.channel_id, OLD.category, -1);
        END IF;
        RETURN NULL;
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF telesrv_visible_channel_media_message(NEW.channel_id, NEW.id) THEN
            PERFORM telesrv_adjust_channel_media_category_count(NEW.channel_id, NEW.category, 1);
        END IF;
        RETURN NULL;
    END IF;

    IF (OLD.channel_id, OLD.id, OLD.category)
       IS DISTINCT FROM
       (NEW.channel_id, NEW.id, NEW.category) THEN
        IF telesrv_visible_channel_media_message(OLD.channel_id, OLD.id) THEN
            PERFORM telesrv_adjust_channel_media_category_count(OLD.channel_id, OLD.category, -1);
        END IF;
        IF telesrv_visible_channel_media_message(NEW.channel_id, NEW.id) THEN
            PERFORM telesrv_adjust_channel_media_category_count(NEW.channel_id, NEW.category, 1);
        END IF;
    END IF;

    RETURN NULL;
END;
$$;


--
-- Name: telesrv_maintain_channel_media_visibility_counts(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_maintain_channel_media_visibility_counts() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    rec RECORD;
    delta INT;
BEGIN
    IF OLD.deleted IS DISTINCT FROM NEW.deleted THEN
        IF COALESCE(OLD.deleted, false) = false AND COALESCE(NEW.deleted, false) = true THEN
            delta := -1;
        ELSE
            delta := 1;
        END IF;

        FOR rec IN
            SELECT mi.channel_id, mi.category
            FROM channel_message_media mi
            WHERE mi.channel_id = NEW.channel_id
              AND mi.id = NEW.id
        LOOP
            PERFORM telesrv_adjust_channel_media_category_count(rec.channel_id, rec.category, delta);
        END LOOP;
    END IF;

    RETURN NULL;
END;
$$;


--
-- Name: telesrv_maintain_private_media_category_counts(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_maintain_private_media_category_counts() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF telesrv_visible_private_media_box(OLD.owner_user_id, OLD.box_id) THEN
            PERFORM telesrv_adjust_private_media_category_count(OLD.owner_user_id, OLD.peer_id, OLD.category, -1);
        END IF;
        RETURN NULL;
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF telesrv_visible_private_media_box(NEW.owner_user_id, NEW.box_id) THEN
            PERFORM telesrv_adjust_private_media_category_count(NEW.owner_user_id, NEW.peer_id, NEW.category, 1);
        END IF;
        RETURN NULL;
    END IF;

    IF (OLD.owner_user_id, OLD.box_id, OLD.peer_id, OLD.category)
       IS DISTINCT FROM
       (NEW.owner_user_id, NEW.box_id, NEW.peer_id, NEW.category) THEN
        IF telesrv_visible_private_media_box(OLD.owner_user_id, OLD.box_id) THEN
            PERFORM telesrv_adjust_private_media_category_count(OLD.owner_user_id, OLD.peer_id, OLD.category, -1);
        END IF;
        IF telesrv_visible_private_media_box(NEW.owner_user_id, NEW.box_id) THEN
            PERFORM telesrv_adjust_private_media_category_count(NEW.owner_user_id, NEW.peer_id, NEW.category, 1);
        END IF;
    END IF;

    RETURN NULL;
END;
$$;


--
-- Name: telesrv_maintain_private_media_visibility_counts(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_maintain_private_media_visibility_counts() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    rec RECORD;
    delta INT;
BEGIN
    IF OLD.deleted IS DISTINCT FROM NEW.deleted THEN
        IF COALESCE(OLD.deleted, false) = false AND COALESCE(NEW.deleted, false) = true THEN
            delta := -1;
        ELSE
            delta := 1;
        END IF;

        FOR rec IN
            SELECT mi.owner_user_id, mi.peer_id, mi.category
            FROM message_box_media mi
            WHERE mi.owner_user_id = NEW.owner_user_id
              AND mi.box_id = NEW.box_id
        LOOP
            PERFORM telesrv_adjust_private_media_category_count(rec.owner_user_id, rec.peer_id, rec.category, delta);
        END LOOP;
    END IF;

    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_bot_channel_participants_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_bot_channel_participants_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    old_bot_id BIGINT;
    new_bot_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        old_bot_id := OLD.bot_user_id;
    ELSIF TG_OP = 'INSERT' THEN
        new_bot_id := NEW.bot_user_id;
    ELSE
        old_bot_id := OLD.bot_user_id;
        new_bot_id := NEW.bot_user_id;
    END IF;

    IF old_bot_id IS NOT NULL THEN
        PERFORM telesrv_bump_channel_participants_for_user(old_bot_id);
    END IF;
    IF new_bot_id IS NOT NULL AND new_bot_id IS DISTINCT FROM old_bot_id THEN
        PERFORM telesrv_bump_channel_participants_for_user(new_bot_id);
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_channel_active_memberships_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_channel_active_memberships_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    old_owner BIGINT;
    new_owner BIGINT;
    changed BOOLEAN;
BEGIN
    IF TG_OP = 'DELETE' THEN
        old_owner := OLD.user_id;
        IF old_owner <> 0 THEN
            PERFORM telesrv_bump_read_model_version('channel_active_memberships', old_owner, 'user', old_owner);
        END IF;
        RETURN NULL;
    END IF;

    new_owner := NEW.user_id;
    IF TG_OP = 'INSERT' THEN
        IF new_owner <> 0 THEN
            PERFORM telesrv_bump_read_model_version('channel_active_memberships', new_owner, 'user', new_owner);
        END IF;
        RETURN NULL;
    END IF;

    old_owner := OLD.user_id;
    changed :=
        OLD.user_id IS DISTINCT FROM NEW.user_id OR
        OLD.channel_id IS DISTINCT FROM NEW.channel_id OR
        OLD.status IS DISTINCT FROM NEW.status OR
        OLD.deleted IS DISTINCT FROM NEW.deleted;

    IF changed THEN
        IF old_owner <> 0 THEN
            PERFORM telesrv_bump_read_model_version('channel_active_memberships', old_owner, 'user', old_owner);
        END IF;
        IF new_owner <> 0 AND new_owner IS DISTINCT FROM old_owner THEN
            PERFORM telesrv_bump_read_model_version('channel_active_memberships', new_owner, 'user', new_owner);
        END IF;
    END IF;

    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_channel_base_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_channel_base_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    changed_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        changed_id := OLD.id;
    ELSE
        changed_id := NEW.id;
    END IF;
    PERFORM telesrv_bump_read_model_version('channel_base', 0, 'channel', changed_id);
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_channel_changed(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_channel_changed() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    changed_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        changed_id := OLD.id;
    ELSE
        changed_id := NEW.id;
    END IF;
    PERFORM pg_notify('telesrv_channel_changed', changed_id::text);
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_channel_dialog_light_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_channel_dialog_light_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    owner_id BIGINT;
    channel_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        owner_id := OLD.user_id;
        channel_id := OLD.channel_id;
    ELSE
        owner_id := NEW.user_id;
        channel_id := NEW.channel_id;
    END IF;

    PERFORM telesrv_bump_dialog_light(owner_id, 'channel', channel_id);
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_channel_media_count_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_channel_media_count_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    new_channel_id BIGINT;
    old_channel_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        old_channel_id := OLD.channel_id;
    ELSIF TG_OP = 'INSERT' THEN
        new_channel_id := NEW.channel_id;
    ELSE
        old_channel_id := OLD.channel_id;
        new_channel_id := NEW.channel_id;
    END IF;

    IF old_channel_id IS NOT NULL THEN
        PERFORM telesrv_bump_read_model_version('channel_media_counts', 0, 'channel', old_channel_id);
    END IF;
    IF new_channel_id IS NOT NULL AND new_channel_id IS DISTINCT FROM old_channel_id THEN
        PERFORM telesrv_bump_read_model_version('channel_media_counts', 0, 'channel', new_channel_id);
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_channel_media_count_visibility_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_channel_media_count_visibility_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.deleted IS DISTINCT FROM NEW.deleted
       AND EXISTS (
           SELECT 1
           FROM channel_message_media mi
           WHERE mi.channel_id = NEW.channel_id AND mi.id = NEW.id
       ) THEN
        PERFORM telesrv_bump_read_model_version('channel_media_counts', 0, 'channel', NEW.channel_id);
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_channel_member_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_channel_member_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    channel_id BIGINT;
    user_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        channel_id := OLD.channel_id;
        user_id := OLD.user_id;
    ELSE
        channel_id := NEW.channel_id;
        user_id := NEW.user_id;
    END IF;

    PERFORM telesrv_bump_read_model_version('channel_member', user_id, 'channel', channel_id);
    PERFORM telesrv_bump_dialog_light(user_id, 'channel', channel_id);
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_channel_participants_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_channel_participants_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    old_channel_id BIGINT;
    new_channel_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        old_channel_id := OLD.channel_id;
    ELSIF TG_OP = 'INSERT' THEN
        new_channel_id := NEW.channel_id;
    ELSE
        old_channel_id := OLD.channel_id;
        new_channel_id := NEW.channel_id;
    END IF;

    IF old_channel_id IS NOT NULL THEN
        PERFORM telesrv_bump_read_model_version('channel_participants', 0, 'channel', old_channel_id);
    END IF;
    IF new_channel_id IS NOT NULL AND new_channel_id IS DISTINCT FROM old_channel_id THEN
        PERFORM telesrv_bump_read_model_version('channel_participants', 0, 'channel', new_channel_id);
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_channel_self_boosts_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_channel_self_boosts_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    old_user_id BIGINT;
    old_peer_type TEXT;
    old_peer_id BIGINT;
    new_user_id BIGINT;
    new_peer_type TEXT;
    new_peer_id BIGINT;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        old_user_id := OLD.user_id;
        old_peer_type := OLD.peer_type;
        old_peer_id := OLD.peer_id;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        new_user_id := NEW.user_id;
        new_peer_type := NEW.peer_type;
        new_peer_id := NEW.peer_id;
    END IF;

    IF old_user_id IS NOT NULL AND old_user_id <> 0 AND old_peer_type = 'channel' AND old_peer_id IS NOT NULL AND old_peer_id <> 0 THEN
        PERFORM telesrv_bump_read_model_version('channel_self_boosts', old_user_id, old_peer_type, old_peer_id);
    END IF;
    IF new_user_id IS NOT NULL
       AND new_user_id <> 0
       AND new_peer_type = 'channel'
       AND new_peer_id IS NOT NULL
       AND new_peer_id <> 0
       AND (new_user_id, new_peer_type, new_peer_id) IS DISTINCT FROM (old_user_id, old_peer_type, old_peer_id) THEN
        PERFORM telesrv_bump_read_model_version('channel_self_boosts', new_user_id, new_peer_type, new_peer_id);
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_contact_block_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_contact_block_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    owner_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        owner_id := OLD.owner_user_id;
    ELSE
        owner_id := NEW.owner_user_id;
    END IF;
    PERFORM telesrv_bump_read_model_version('contact_blocklist', owner_id, 'user', owner_id);
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_contact_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_contact_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    owner_id BIGINT;
    contact_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        owner_id := OLD.user_id;
        contact_id := OLD.contact_user_id;
    ELSE
        owner_id := NEW.user_id;
        contact_id := NEW.contact_user_id;
    END IF;
    PERFORM telesrv_bump_read_model_version('contact_account', owner_id, 'user', owner_id);
    PERFORM telesrv_bump_dialog_light(owner_id, 'user', contact_id);
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_dialog_light_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_dialog_light_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    owner_id BIGINT;
    peer_type TEXT;
    peer_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        owner_id := OLD.user_id;
        peer_type := OLD.peer_type;
        peer_id := OLD.peer_id;
    ELSE
        owner_id := NEW.user_id;
        peer_type := NEW.peer_type;
        peer_id := NEW.peer_id;
    END IF;

    PERFORM telesrv_bump_dialog_light(owner_id, peer_type, peer_id);
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_privacy_channel_participants_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_privacy_channel_participants_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    old_owner_id BIGINT;
    new_owner_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        old_owner_id := OLD.owner_user_id;
    ELSIF TG_OP = 'INSERT' THEN
        new_owner_id := NEW.owner_user_id;
    ELSE
        old_owner_id := OLD.owner_user_id;
        new_owner_id := NEW.owner_user_id;
    END IF;

    IF old_owner_id IS NOT NULL THEN
        PERFORM telesrv_bump_channel_participants_for_user(old_owner_id);
    END IF;
    IF new_owner_id IS NOT NULL AND new_owner_id IS DISTINCT FROM old_owner_id THEN
        PERFORM telesrv_bump_channel_participants_for_user(new_owner_id);
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_privacy_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_privacy_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    owner_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        owner_id := OLD.owner_user_id;
    ELSE
        owner_id := NEW.owner_user_id;
    END IF;

    PERFORM telesrv_bump_read_model_version('privacy_rules', owner_id, 'user', owner_id);
    PERFORM telesrv_bump_contact_accounts_for_user(owner_id);
    PERFORM telesrv_bump_private_dialog_light_for_user(owner_id);
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_private_media_count_dialog_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_private_media_count_dialog_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    old_owner BIGINT;
    old_peer BIGINT;
    new_owner BIGINT;
    new_peer BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        old_owner := OLD.user_id;
        old_peer := OLD.peer_id;
        IF OLD.peer_type = 'user' AND old_owner <> 0 AND old_peer <> 0 THEN
            PERFORM telesrv_bump_read_model_version('private_media_counts', old_owner, 'user', old_peer);
        END IF;
        RETURN NULL;
    END IF;

    IF TG_OP = 'INSERT' THEN
        new_owner := NEW.user_id;
        new_peer := NEW.peer_id;
        IF NEW.peer_type = 'user' AND new_owner <> 0 AND new_peer <> 0 THEN
            PERFORM telesrv_bump_read_model_version('private_media_counts', new_owner, 'user', new_peer);
        END IF;
        RETURN NULL;
    END IF;

    old_owner := OLD.user_id;
    old_peer := OLD.peer_id;
    new_owner := NEW.user_id;
    new_peer := NEW.peer_id;

    IF OLD.peer_type = 'user' AND old_owner <> 0 AND old_peer <> 0 THEN
        PERFORM telesrv_bump_read_model_version('private_media_counts', old_owner, 'user', old_peer);
    END IF;
    IF NEW.peer_type = 'user'
       AND new_owner <> 0
       AND new_peer <> 0
       AND (NEW.peer_type, new_owner, new_peer) IS DISTINCT FROM (OLD.peer_type, old_owner, old_peer) THEN
        PERFORM telesrv_bump_read_model_version('private_media_counts', new_owner, 'user', new_peer);
    END IF;

    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_private_media_count_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_private_media_count_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    old_owner BIGINT;
    old_peer BIGINT;
    new_owner BIGINT;
    new_peer BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        old_owner := OLD.owner_user_id;
        old_peer := OLD.peer_id;
        IF old_owner <> 0 AND old_peer <> 0 THEN
            PERFORM telesrv_bump_read_model_version('private_media_counts', old_owner, 'user', old_peer);
        END IF;
        RETURN NULL;
    END IF;

    IF TG_OP = 'INSERT' THEN
        new_owner := NEW.owner_user_id;
        new_peer := NEW.peer_id;
        IF new_owner <> 0 AND new_peer <> 0 THEN
            PERFORM telesrv_bump_read_model_version('private_media_counts', new_owner, 'user', new_peer);
        END IF;
        RETURN NULL;
    END IF;

    old_owner := OLD.owner_user_id;
    old_peer := OLD.peer_id;
    new_owner := NEW.owner_user_id;
    new_peer := NEW.peer_id;

    IF old_owner <> 0 AND old_peer <> 0 THEN
        PERFORM telesrv_bump_read_model_version('private_media_counts', old_owner, 'user', old_peer);
    END IF;
    IF new_owner <> 0
       AND new_peer <> 0
       AND (new_owner, new_peer) IS DISTINCT FROM (old_owner, old_peer) THEN
        PERFORM telesrv_bump_read_model_version('private_media_counts', new_owner, 'user', new_peer);
    END IF;

    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_private_media_count_visibility_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_private_media_count_visibility_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    rec RECORD;
BEGIN
    IF OLD.deleted IS DISTINCT FROM NEW.deleted THEN
        FOR rec IN
            SELECT DISTINCT mi.owner_user_id, mi.peer_id
            FROM message_box_media mi
            WHERE mi.owner_user_id = NEW.owner_user_id
              AND mi.box_id = NEW.box_id
        LOOP
            IF rec.owner_user_id <> 0 AND rec.peer_id <> 0 THEN
                PERFORM telesrv_bump_read_model_version('private_media_counts', rec.owner_user_id, 'user', rec.peer_id);
            END IF;
        END LOOP;
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_profile_photo_channel_participants_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_profile_photo_channel_participants_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    old_owner_type TEXT;
    old_owner_id BIGINT;
    new_owner_type TEXT;
    new_owner_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        old_owner_type := OLD.owner_peer_type;
        old_owner_id := OLD.owner_peer_id;
    ELSIF TG_OP = 'INSERT' THEN
        new_owner_type := NEW.owner_peer_type;
        new_owner_id := NEW.owner_peer_id;
    ELSE
        old_owner_type := OLD.owner_peer_type;
        old_owner_id := OLD.owner_peer_id;
        new_owner_type := NEW.owner_peer_type;
        new_owner_id := NEW.owner_peer_id;
    END IF;

    IF old_owner_type = 'user' THEN
        PERFORM telesrv_bump_channel_participants_for_user(old_owner_id);
    ELSIF old_owner_type = 'channel' AND old_owner_id IS NOT NULL THEN
        PERFORM telesrv_bump_read_model_version('channel_participants', 0, 'channel', old_owner_id);
    END IF;
    IF (new_owner_type, new_owner_id) IS DISTINCT FROM (old_owner_type, old_owner_id) THEN
        IF new_owner_type = 'user' THEN
            PERFORM telesrv_bump_channel_participants_for_user(new_owner_id);
        ELSIF new_owner_type = 'channel' AND new_owner_id IS NOT NULL THEN
            PERFORM telesrv_bump_read_model_version('channel_participants', 0, 'channel', new_owner_id);
        END IF;
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_profile_photo_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_profile_photo_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    owner_type TEXT;
    owner_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        owner_type := OLD.owner_peer_type;
        owner_id := OLD.owner_peer_id;
    ELSE
        owner_type := NEW.owner_peer_type;
        owner_id := NEW.owner_peer_id;
    END IF;

    PERFORM telesrv_bump_read_model_version('profile_photo', 0, owner_type, owner_id);
    IF owner_type = 'user' THEN
        PERFORM telesrv_bump_contact_accounts_for_user(owner_id);
        PERFORM telesrv_bump_private_dialog_light_for_user(owner_id);
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_story_peer_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_story_peer_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    o_type TEXT;
    o_id BIGINT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        o_type := OLD.owner_peer_type;
        o_id := OLD.owner_peer_id;
    ELSE
        o_type := NEW.owner_peer_type;
        o_id := NEW.owner_peer_id;
    END IF;
    PERFORM telesrv_bump_read_model_version('story_peer', 0, o_type, o_id);
    RETURN NULL;
END;
$$;


--
-- Name: telesrv_notify_user_base_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_user_base_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    changed_id BIGINT;
    projection_changed BOOLEAN;
BEGIN
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


--
-- Name: telesrv_notify_user_channel_participants_read_model(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_notify_user_channel_participants_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    changed_id BIGINT;
    old_id BIGINT;
    projection_changed BOOLEAN;
BEGIN
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


--
-- Name: telesrv_random_read_model_hash(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_random_read_model_hash() RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    value BIGINT;
BEGIN
    -- PostgreSQL random() provides enough entropy for a cache/version token and
    -- avoids requiring pgcrypto in local/dev deployments. Keep it positive and
    -- non-zero because Telegram clients use 0 as "no known hash".
    value := floor(random() * 9007199254740991)::BIGINT + 1;
    IF value = 0 THEN
        value := 1;
    END IF;
    RETURN value;
END;
$$;


--
-- Name: telesrv_visible_channel_media_message(bigint, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_visible_channel_media_message(p_channel_id bigint, p_id integer) RETURNS boolean
    LANGUAGE plpgsql
    AS $$
DECLARE
    visible boolean;
BEGIN
    SELECT NOT m.deleted
    INTO visible
    FROM channel_messages m
    WHERE m.channel_id = p_channel_id
      AND m.id = p_id;

    RETURN COALESCE(visible, false);
END;
$$;


--
-- Name: telesrv_visible_private_media_box(bigint, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.telesrv_visible_private_media_box(p_owner_user_id bigint, p_box_id integer) RETURNS boolean
    LANGUAGE plpgsql
    AS $$
DECLARE
    visible boolean;
BEGIN
    SELECT NOT mb.deleted
    INTO visible
    FROM message_boxes mb
    WHERE mb.owner_user_id = p_owner_user_id
      AND mb.box_id = p_box_id;

    RETURN COALESCE(visible, false);
END;
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: account_passwords; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.account_passwords (
    user_id bigint NOT NULL,
    has_recovery boolean DEFAULT false NOT NULL,
    has_secure_values boolean DEFAULT false NOT NULL,
    has_password boolean DEFAULT false NOT NULL,
    hint character varying(256) DEFAULT ''::character varying NOT NULL,
    email_unconfirmed_pattern character varying(256) DEFAULT ''::character varying NOT NULL,
    login_email_pattern character varying(256) DEFAULT ''::character varying NOT NULL,
    secure_random bytea DEFAULT decode('74656c657372762d746465736b746f702d6465762d7365637572652d72616e64'::text, 'hex'::text) NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    current_algo_salt1 bytea DEFAULT '\x'::bytea NOT NULL,
    current_algo_salt2 bytea DEFAULT '\x'::bytea NOT NULL,
    current_algo_g integer DEFAULT 0 NOT NULL,
    current_algo_p bytea DEFAULT '\x'::bytea NOT NULL,
    srp_id bigint DEFAULT 0 NOT NULL,
    srp_verifier bytea DEFAULT '\x'::bytea NOT NULL,
    srp_b_secret bytea DEFAULT '\x'::bytea NOT NULL,
    srp_b bytea DEFAULT '\x'::bytea NOT NULL,
    recovery_email character varying(256) DEFAULT ''::character varying NOT NULL,
    recovery_code character varying(32) DEFAULT ''::character varying NOT NULL,
    recovery_code_expires_at timestamp with time zone
);


--
-- Name: account_privacy_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.account_privacy_rules (
    owner_user_id bigint NOT NULL,
    privacy_key text NOT NULL,
    rules jsonb DEFAULT '[]'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: account_reaction_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.account_reaction_settings (
    user_id bigint NOT NULL,
    messages_notify_from text DEFAULT 'contacts'::text NOT NULL,
    stories_notify_from text DEFAULT 'contacts'::text NOT NULL,
    poll_votes_notify_from text DEFAULT 'contacts'::text NOT NULL,
    show_previews boolean DEFAULT true NOT NULL,
    default_reaction_type text DEFAULT 'emoji'::text NOT NULL,
    default_reaction_value text DEFAULT '👍'::text NOT NULL,
    paid_privacy_kind text DEFAULT 'default'::text NOT NULL,
    paid_privacy_peer_type text,
    paid_privacy_peer_id bigint,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT account_reaction_settings_check CHECK ((((paid_privacy_kind = 'peer'::text) AND (paid_privacy_peer_type = ANY (ARRAY['user'::text, 'channel'::text])) AND (paid_privacy_peer_id IS NOT NULL)) OR ((paid_privacy_kind <> 'peer'::text) AND (paid_privacy_peer_type IS NULL) AND (paid_privacy_peer_id IS NULL)))),
    CONSTRAINT account_reaction_settings_default_reaction_type_check CHECK ((default_reaction_type = 'emoji'::text)),
    CONSTRAINT account_reaction_settings_default_reaction_value_check CHECK ((default_reaction_value <> ''::text)),
    CONSTRAINT account_reaction_settings_messages_notify_from_check CHECK ((messages_notify_from = ANY (ARRAY['none'::text, 'contacts'::text, 'all'::text]))),
    CONSTRAINT account_reaction_settings_paid_privacy_kind_check CHECK ((paid_privacy_kind = ANY (ARRAY['default'::text, 'anonymous'::text, 'peer'::text]))),
    CONSTRAINT account_reaction_settings_poll_votes_notify_from_check CHECK ((poll_votes_notify_from = ANY (ARRAY['none'::text, 'contacts'::text, 'all'::text]))),
    CONSTRAINT account_reaction_settings_stories_notify_from_check CHECK ((stories_notify_from = ANY (ARRAY['none'::text, 'contacts'::text, 'all'::text])))
);


--
-- Name: app_configs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.app_configs (
    client character varying(64) NOT NULL,
    hash integer NOT NULL,
    config_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: auth_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_keys (
    auth_key_id bigint NOT NULL,
    body bytea NOT NULL,
    server_salt bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: authorizations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.authorizations (
    auth_key_id bigint NOT NULL,
    user_id bigint NOT NULL,
    hash bigint DEFAULT 0 NOT NULL,
    layer integer DEFAULT 0 NOT NULL,
    device_model character varying(128) DEFAULT ''::character varying NOT NULL,
    platform character varying(64) DEFAULT ''::character varying NOT NULL,
    system_version character varying(64) DEFAULT ''::character varying NOT NULL,
    api_id integer DEFAULT 0 NOT NULL,
    app_version character varying(64) DEFAULT ''::character varying NOT NULL,
    ip character varying(64) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    active_at timestamp with time zone DEFAULT now() NOT NULL,
    password_pending boolean DEFAULT false NOT NULL
);


--
-- Name: available_reactions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.available_reactions (
    reaction text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    inactive boolean DEFAULT false NOT NULL,
    premium boolean DEFAULT false NOT NULL,
    static_icon_id bigint DEFAULT 0 NOT NULL,
    appear_animation_id bigint DEFAULT 0 NOT NULL,
    select_animation_id bigint DEFAULT 0 NOT NULL,
    activate_animation_id bigint DEFAULT 0 NOT NULL,
    effect_animation_id bigint DEFAULT 0 NOT NULL,
    around_animation_id bigint DEFAULT 0 NOT NULL,
    center_icon_id bigint DEFAULT 0 NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL
);


--
-- Name: bot_chat_states; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_chat_states (
    bot_user_id bigint NOT NULL,
    user_id bigint NOT NULL,
    state jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: bot_user_permissions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bot_user_permissions (
    bot_user_id bigint NOT NULL,
    user_id bigint NOT NULL,
    from_request boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT bot_user_permissions_check CHECK ((bot_user_id <> user_id))
);


--
-- Name: bots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bots (
    bot_user_id bigint NOT NULL,
    owner_user_id bigint NOT NULL,
    token_secret character varying(64) NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    commands jsonb DEFAULT '[]'::jsonb NOT NULL,
    bot_chat_history boolean DEFAULT false NOT NULL,
    bot_nochats boolean DEFAULT false NOT NULL,
    inline_placeholder character varying(128) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    menu_button_type smallint DEFAULT 0 NOT NULL,
    menu_button_text character varying(64) DEFAULT ''::character varying NOT NULL,
    menu_button_url character varying(512) DEFAULT ''::character varying NOT NULL,
    bot_inline_geo boolean DEFAULT false NOT NULL
);


--
-- Name: business_automation_deliveries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.business_automation_deliveries (
    owner_user_id bigint NOT NULL,
    peer_user_id bigint NOT NULL,
    kind text NOT NULL,
    trigger_message_id integer NOT NULL,
    shortcut_id integer DEFAULT 0 NOT NULL,
    sent_at integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT business_automation_deliveries_kind_check CHECK ((kind = ANY (ARRAY['greeting'::text, 'away'::text, 'ai'::text])))
);


--
-- Name: business_chat_links; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.business_chat_links (
    slug text NOT NULL,
    owner_user_id bigint NOT NULL,
    link text NOT NULL,
    message text NOT NULL,
    entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    views integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: business_connected_bot_peer_states; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.business_connected_bot_peer_states (
    owner_user_id bigint NOT NULL,
    peer_user_id bigint NOT NULL,
    paused boolean DEFAULT false NOT NULL,
    disabled boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: business_connected_bots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.business_connected_bots (
    owner_user_id bigint NOT NULL,
    bot_user_id bigint NOT NULL,
    recipients jsonb DEFAULT '{}'::jsonb NOT NULL,
    rights jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: channel_admin_log_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_admin_log_events (
    channel_id bigint NOT NULL,
    id bigint NOT NULL,
    actor_user_id bigint NOT NULL,
    event_date integer NOT NULL,
    event_type character varying(48) NOT NULL,
    prev_string text DEFAULT ''::text NOT NULL,
    new_string text DEFAULT ''::text NOT NULL,
    prev_bool boolean DEFAULT false NOT NULL,
    new_bool boolean DEFAULT false NOT NULL,
    prev_int integer DEFAULT 0 NOT NULL,
    new_int integer DEFAULT 0 NOT NULL,
    prev_participant jsonb DEFAULT '{}'::jsonb NOT NULL,
    new_participant jsonb DEFAULT '{}'::jsonb NOT NULL,
    participant jsonb DEFAULT '{}'::jsonb NOT NULL,
    message jsonb DEFAULT '{}'::jsonb NOT NULL,
    prev_message jsonb DEFAULT '{}'::jsonb NOT NULL,
    new_message jsonb DEFAULT '{}'::jsonb NOT NULL,
    query text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_admin_log_events_type_check CHECK (((event_type)::text = ANY (ARRAY[('change_title'::character varying)::text, ('change_username'::character varying)::text, ('change_linked_chat'::character varying)::text, ('toggle_signatures'::character varying)::text, ('toggle_pre_history_hidden'::character varying)::text, ('toggle_forum'::character varying)::text, ('toggle_autotranslation'::character varying)::text, ('toggle_anti_spam'::character varying)::text, ('toggle_slow_mode'::character varying)::text, ('participant_invite'::character varying)::text, ('participant_join'::character varying)::text, ('participant_leave'::character varying)::text, ('participant_promote'::character varying)::text, ('participant_demote'::character varying)::text, ('participant_edit_rank'::character varying)::text, ('participant_ban'::character varying)::text, ('participant_unban'::character varying)::text, ('participant_kick'::character varying)::text, ('participant_unkick'::character varying)::text, ('update_pinned'::character varying)::text, ('send_message'::character varying)::text, ('edit_message'::character varying)::text, ('delete_message'::character varying)::text])))
);


--
-- Name: channel_boost_slots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_boost_slots (
    user_id bigint NOT NULL,
    slot integer NOT NULL,
    peer_type character varying(16) DEFAULT ''::character varying NOT NULL,
    peer_id bigint DEFAULT 0 NOT NULL,
    assigned_at integer DEFAULT 0 NOT NULL,
    expires_at integer DEFAULT 0 NOT NULL,
    cooldown_until integer DEFAULT 0 NOT NULL,
    multiplier integer DEFAULT 1 NOT NULL,
    gift boolean DEFAULT false NOT NULL,
    giveaway boolean DEFAULT false NOT NULL,
    unclaimed boolean DEFAULT false NOT NULL,
    giveaway_msg_id integer DEFAULT 0 NOT NULL,
    used_gift_slug text DEFAULT ''::text NOT NULL,
    stars bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_boost_slots_multiplier_check CHECK ((multiplier > 0)),
    CONSTRAINT channel_boost_slots_peer_check CHECK (((((peer_type)::text = ''::text) AND (peer_id = 0)) OR (((peer_type)::text = 'channel'::text) AND (peer_id > 0)))),
    CONSTRAINT channel_boost_slots_slot_check CHECK ((slot > 0))
);


--
-- Name: channel_dialogs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_dialogs (
    user_id bigint NOT NULL,
    channel_id bigint NOT NULL,
    folder_id integer DEFAULT 0 NOT NULL,
    top_message_id integer DEFAULT 0 NOT NULL,
    top_message_date integer DEFAULT 0 NOT NULL,
    read_inbox_max_id integer DEFAULT 0 NOT NULL,
    read_outbox_max_id integer DEFAULT 0 NOT NULL,
    unread_count integer DEFAULT 0 NOT NULL,
    unread_mentions_count integer DEFAULT 0 NOT NULL,
    unread_reactions_count integer DEFAULT 0 NOT NULL,
    pinned boolean DEFAULT false NOT NULL,
    pinned_order integer DEFAULT 0 NOT NULL,
    unread_mark boolean DEFAULT false NOT NULL,
    view_forum_as_messages boolean DEFAULT false NOT NULL,
    notify_settings jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    default_send_as_peer_type character varying(16),
    default_send_as_peer_id bigint,
    has_scheduled boolean DEFAULT false NOT NULL,
    CONSTRAINT channel_dialogs_default_send_as_peer_type_check CHECK (((default_send_as_peer_type IS NULL) OR ((default_send_as_peer_type)::text = ANY (ARRAY[('user'::character varying)::text, ('channel'::character varying)::text])))),
    CONSTRAINT channel_dialogs_folder_id_check CHECK ((folder_id >= 0))
);


--
-- Name: channel_forum_topics; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_forum_topics (
    channel_id bigint NOT NULL,
    topic_id integer NOT NULL,
    creator_user_id bigint NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    icon_color integer DEFAULT 0 NOT NULL,
    icon_emoji_id bigint DEFAULT 0 NOT NULL,
    title_missing boolean DEFAULT false NOT NULL,
    closed boolean DEFAULT false NOT NULL,
    hidden boolean DEFAULT false NOT NULL,
    pinned boolean DEFAULT false NOT NULL,
    pinned_order integer DEFAULT 0 NOT NULL,
    date integer NOT NULL,
    top_message_id integer DEFAULT 0 NOT NULL,
    read_inbox_max_id integer DEFAULT 0 NOT NULL,
    read_outbox_max_id integer DEFAULT 0 NOT NULL,
    unread_count integer DEFAULT 0 NOT NULL,
    unread_mentions_count integer DEFAULT 0 NOT NULL,
    unread_reactions_count integer DEFAULT 0 NOT NULL,
    unread_poll_votes_count integer DEFAULT 0 NOT NULL,
    deleted boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_forum_topics_ids_check CHECK (((topic_id > 0) AND (top_message_id >= 0))),
    CONSTRAINT channel_forum_topics_title_check CHECK (((title <> ''::text) OR title_missing))
);


--
-- Name: channel_invite_hashes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_invite_hashes (
    hash text NOT NULL,
    channel_id bigint NOT NULL,
    invite_id bigint NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_invite_hashes_nonempty_check CHECK ((hash <> ''::text))
);


--
-- Name: channel_invite_importers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_invite_importers (
    channel_id bigint NOT NULL,
    invite_id bigint NOT NULL,
    user_id bigint NOT NULL,
    date integer NOT NULL,
    requested boolean DEFAULT false NOT NULL,
    approved_by bigint DEFAULT 0 NOT NULL,
    via_chatlist boolean DEFAULT false NOT NULL,
    about text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: channel_invites; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_invites (
    channel_id bigint NOT NULL,
    invite_id bigint NOT NULL,
    hash text NOT NULL,
    admin_user_id bigint NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    permanent boolean DEFAULT false NOT NULL,
    revoked boolean DEFAULT false NOT NULL,
    request_needed boolean DEFAULT false NOT NULL,
    expire_date integer,
    usage_limit integer,
    usage_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    requested_count integer DEFAULT 0 NOT NULL
);


--
-- Name: channel_media_category_counts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_media_category_counts (
    channel_id bigint NOT NULL,
    category smallint NOT NULL,
    media_count integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_media_category_counts_media_count_check CHECK ((media_count >= 0))
);


--
-- Name: channel_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_members (
    channel_id bigint NOT NULL,
    user_id bigint NOT NULL,
    inviter_user_id bigint DEFAULT 0 NOT NULL,
    role character varying(16) DEFAULT 'member'::character varying NOT NULL,
    status character varying(16) DEFAULT 'active'::character varying NOT NULL,
    joined_at integer DEFAULT 0 NOT NULL,
    left_at integer DEFAULT 0 NOT NULL,
    admin_rights jsonb DEFAULT '{}'::jsonb NOT NULL,
    banned_rights jsonb DEFAULT '{}'::jsonb NOT NULL,
    rank text DEFAULT ''::text NOT NULL,
    available_min_id integer DEFAULT 0 NOT NULL,
    available_min_pts integer DEFAULT 0 NOT NULL,
    read_inbox_max_id integer DEFAULT 0 NOT NULL,
    read_inbox_date integer DEFAULT 0 NOT NULL,
    read_outbox_max_id integer DEFAULT 0 NOT NULL,
    unread_mark boolean DEFAULT false NOT NULL,
    slowmode_last_send_date integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_members_role_check CHECK (((role)::text = ANY (ARRAY[('creator'::character varying)::text, ('admin'::character varying)::text, ('member'::character varying)::text]))),
    CONSTRAINT channel_members_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('left'::character varying)::text, ('kicked'::character varying)::text, ('banned'::character varying)::text])))
);


--
-- Name: channel_message_media; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_message_media (
    channel_id bigint NOT NULL,
    id integer NOT NULL,
    category smallint NOT NULL,
    message_date integer NOT NULL
);


--
-- Name: channel_message_reactions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_message_reactions (
    channel_id bigint NOT NULL,
    message_id integer NOT NULL,
    reacted_user_id bigint NOT NULL,
    sender_user_id bigint NOT NULL,
    reaction_type character varying(16) NOT NULL,
    reaction_value text NOT NULL,
    big boolean DEFAULT false NOT NULL,
    unread boolean DEFAULT false NOT NULL,
    chosen_order integer DEFAULT 1 NOT NULL,
    reaction_date integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_message_reactions_order_check CHECK ((chosen_order > 0)),
    CONSTRAINT channel_message_reactions_type_check CHECK (((reaction_type)::text = 'emoji'::text)),
    CONSTRAINT channel_message_reactions_value_check CHECK ((reaction_value <> ''::text))
);


--
-- Name: channel_message_viewers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_message_viewers (
    channel_id bigint NOT NULL,
    message_id integer NOT NULL,
    viewer_user_id bigint NOT NULL,
    viewed_at integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_message_viewers_positive_check CHECK (((channel_id > 0) AND (message_id > 0) AND (viewer_user_id > 0)))
);


--
-- Name: channel_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_messages (
    channel_id bigint NOT NULL,
    id integer NOT NULL,
    random_id bigint DEFAULT 0 NOT NULL,
    sender_user_id bigint NOT NULL,
    from_peer_type character varying(16) DEFAULT 'user'::character varying NOT NULL,
    from_peer_id bigint NOT NULL,
    send_as_peer_type character varying(16),
    send_as_peer_id bigint,
    message_date integer NOT NULL,
    edit_date integer DEFAULT 0 NOT NULL,
    post boolean DEFAULT false NOT NULL,
    silent boolean DEFAULT false NOT NULL,
    noforwards boolean DEFAULT false NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    reply_to jsonb DEFAULT '{}'::jsonb NOT NULL,
    reply_to_msg_id integer DEFAULT 0 NOT NULL,
    reply_to_peer_type character varying(16) DEFAULT ''::character varying NOT NULL,
    reply_to_peer_id bigint DEFAULT 0 NOT NULL,
    reply_to_top_id integer DEFAULT 0 NOT NULL,
    fwd_from jsonb DEFAULT '{}'::jsonb NOT NULL,
    discussion_channel_id bigint DEFAULT 0 NOT NULL,
    discussion_message_id integer DEFAULT 0 NOT NULL,
    action jsonb DEFAULT '{}'::jsonb NOT NULL,
    pts integer NOT NULL,
    deleted boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    views_count integer DEFAULT 0 NOT NULL,
    media jsonb DEFAULT '{}'::jsonb NOT NULL,
    ttl_period integer DEFAULT 0 NOT NULL,
    expires_at integer DEFAULT 0 NOT NULL,
    post_author text DEFAULT ''::text NOT NULL,
    pinned boolean DEFAULT false NOT NULL,
    via_bot_id bigint DEFAULT 0 NOT NULL,
    reply_markup jsonb DEFAULT '{}'::jsonb NOT NULL,
    from_boosts_applied integer DEFAULT 0 NOT NULL,
    rich_message jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT channel_messages_content_check CHECK (((body <> ''::text) OR (action <> '{}'::jsonb) OR (media <> '{}'::jsonb) OR (rich_message <> '{}'::jsonb))),
    CONSTRAINT channel_messages_peer_type_check CHECK ((((from_peer_type)::text = ANY (ARRAY[('user'::character varying)::text, ('channel'::character varying)::text])) AND ((send_as_peer_type IS NULL) OR ((send_as_peer_type)::text = ANY (ARRAY[('user'::character varying)::text, ('channel'::character varying)::text]))) AND (((reply_to_peer_type)::text = ''::text) OR ((reply_to_peer_type)::text = ANY (ARRAY[('user'::character varying)::text, ('channel'::character varying)::text])))))
);


--
-- Name: channel_unread_mention_index; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_unread_mention_index (
    channel_id bigint NOT NULL,
    message_id integer NOT NULL,
    user_id bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: channel_unread_mentions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_unread_mentions (
    user_id bigint NOT NULL,
    channel_id bigint NOT NULL,
    message_id integer NOT NULL,
    top_message_id integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    media_unread boolean DEFAULT false NOT NULL,
    unread boolean DEFAULT true NOT NULL
);


--
-- Name: channel_update_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_update_events (
    channel_id bigint NOT NULL,
    pts integer NOT NULL,
    pts_count integer DEFAULT 1 NOT NULL,
    date integer NOT NULL,
    event_type character varying(32) NOT NULL,
    message_id integer DEFAULT 0 NOT NULL,
    message_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    sender_user_id bigint DEFAULT 0 NOT NULL,
    user_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_update_events_pts_count_check CHECK ((pts_count > 0)),
    CONSTRAINT channel_update_events_type_check CHECK (((event_type)::text = ANY (ARRAY[('new_channel_message'::character varying)::text, ('edit_channel_message'::character varying)::text, ('delete_channel_messages'::character varying)::text, ('channel_participant'::character varying)::text, ('pinned_channel_messages'::character varying)::text, ('noop'::character varying)::text])))
);


--
-- Name: channel_usernames; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_usernames (
    username_lower text NOT NULL,
    channel_id bigint NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_usernames_nonempty_check CHECK ((username_lower <> ''::text))
);


--
-- Name: channels; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channels (
    id bigint NOT NULL,
    access_hash bigint NOT NULL,
    creator_user_id bigint NOT NULL,
    title text NOT NULL,
    about text DEFAULT ''::text NOT NULL,
    username text,
    broadcast boolean DEFAULT false NOT NULL,
    megagroup boolean DEFAULT false NOT NULL,
    forum boolean DEFAULT false NOT NULL,
    forum_tabs boolean DEFAULT false NOT NULL,
    noforwards boolean DEFAULT false NOT NULL,
    join_to_send boolean DEFAULT false NOT NULL,
    join_request boolean DEFAULT false NOT NULL,
    signatures boolean DEFAULT false NOT NULL,
    pre_history_hidden boolean DEFAULT false NOT NULL,
    participants_hidden boolean DEFAULT false NOT NULL,
    antispam boolean DEFAULT false NOT NULL,
    linked_chat_id bigint DEFAULT 0 NOT NULL,
    slowmode_seconds integer DEFAULT 0 NOT NULL,
    default_banned_rights jsonb DEFAULT '{}'::jsonb NOT NULL,
    available_reactions jsonb DEFAULT '{}'::jsonb NOT NULL,
    color_set boolean DEFAULT false NOT NULL,
    color integer DEFAULT 0 NOT NULL,
    color_background_emoji_id bigint DEFAULT 0 NOT NULL,
    profile_color_set boolean DEFAULT false NOT NULL,
    profile_color integer DEFAULT 0 NOT NULL,
    profile_color_background_emoji_id bigint DEFAULT 0 NOT NULL,
    emoji_status_document_id bigint DEFAULT 0 NOT NULL,
    emoji_status_until integer DEFAULT 0 NOT NULL,
    participants_count integer DEFAULT 0 NOT NULL,
    admins_count integer DEFAULT 0 NOT NULL,
    kicked_count integer DEFAULT 0 NOT NULL,
    banned_count integer DEFAULT 0 NOT NULL,
    top_message_id integer DEFAULT 0 NOT NULL,
    pts integer DEFAULT 0 NOT NULL,
    admin_log_seq bigint DEFAULT 0 NOT NULL,
    ttl_period integer DEFAULT 0 NOT NULL,
    date integer NOT NULL,
    deleted boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    pinned_message_id integer DEFAULT 0 NOT NULL,
    autotranslation boolean DEFAULT false NOT NULL,
    restricted_sponsored boolean DEFAULT false NOT NULL,
    broadcast_messages_allowed boolean DEFAULT false NOT NULL,
    send_paid_messages_stars bigint DEFAULT 0 NOT NULL,
    photo_id bigint DEFAULT 0 NOT NULL,
    photo_dc_id integer DEFAULT 0 NOT NULL,
    photo_stripped bytea DEFAULT '\x'::bytea NOT NULL,
    read_inbox_top1_user_id bigint DEFAULT 0 NOT NULL,
    read_inbox_top1 integer DEFAULT 0 NOT NULL,
    read_inbox_top2 integer DEFAULT 0 NOT NULL,
    active_call_id bigint DEFAULT 0 NOT NULL,
    active_call_access_hash bigint DEFAULT 0 NOT NULL,
    active_call_not_empty boolean DEFAULT false NOT NULL,
    boosts_unrestrict integer DEFAULT 0 NOT NULL,
    CONSTRAINT channels_kind_check CHECK ((((broadcast AND (NOT megagroup) AND (NOT forum)) OR (megagroup AND (NOT broadcast))) AND ((NOT forum_tabs) OR forum))),
    CONSTRAINT channels_send_paid_messages_stars_nonnegative_check CHECK ((send_paid_messages_stars >= 0)),
    CONSTRAINT channels_title_nonempty_check CHECK ((title <> ''::text))
);


--
-- Name: contact_blocks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.contact_blocks (
    owner_user_id bigint NOT NULL,
    blocked_user_id bigint NOT NULL,
    date integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: contacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.contacts (
    user_id bigint NOT NULL,
    contact_user_id bigint NOT NULL,
    mutual boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    contact_phone character varying(32) DEFAULT ''::character varying NOT NULL,
    contact_first_name character varying(255) DEFAULT ''::character varying NOT NULL,
    contact_last_name character varying(255) DEFAULT ''::character varying NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    note_entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    close_friend boolean DEFAULT false NOT NULL,
    stories_hidden boolean DEFAULT false NOT NULL,
    personal_photo_id bigint DEFAULT 0 NOT NULL,
    personal_photo_date integer DEFAULT 0 NOT NULL
);


--
-- Name: countries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.countries (
    iso2 character varying(2) NOT NULL,
    default_name character varying(128) NOT NULL,
    name character varying(128) DEFAULT ''::character varying NOT NULL,
    hidden boolean DEFAULT false NOT NULL,
    order_index integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: country_codes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.country_codes (
    id bigint NOT NULL,
    iso2 character varying(2) NOT NULL,
    country_code character varying(16) NOT NULL,
    prefixes text[] DEFAULT '{}'::text[] NOT NULL,
    patterns text[] DEFAULT '{}'::text[] NOT NULL,
    order_index integer DEFAULT 0 NOT NULL
);


--
-- Name: country_codes_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.country_codes ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.country_codes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: dialog_drafts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dialog_drafts (
    user_id bigint NOT NULL,
    peer_type character varying(16) NOT NULL,
    peer_id bigint NOT NULL,
    top_message_id integer DEFAULT 0 NOT NULL,
    date integer DEFAULT 0 NOT NULL,
    draft jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT dialog_drafts_peer_type_check CHECK (((peer_type)::text = ANY (ARRAY[('user'::character varying)::text, ('channel'::character varying)::text]))),
    CONSTRAINT dialog_drafts_top_message_id_check CHECK ((top_message_id >= 0))
);


--
-- Name: dialog_filter_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dialog_filter_settings (
    user_id bigint NOT NULL,
    tags_enabled boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    archive_pinned boolean DEFAULT false NOT NULL,
    archive_pinned_order integer DEFAULT 0 NOT NULL
);


--
-- Name: dialog_filters; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dialog_filters (
    user_id bigint NOT NULL,
    filter_id integer NOT NULL,
    is_chatlist boolean DEFAULT false NOT NULL,
    filter jsonb NOT NULL,
    order_value integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT dialog_filters_filter_object_check CHECK ((jsonb_typeof(filter) = 'object'::text)),
    CONSTRAINT dialog_filters_id_check CHECK ((filter_id >= 2))
);


--
-- Name: dialogs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dialogs (
    user_id bigint NOT NULL,
    peer_type character varying(16) NOT NULL,
    peer_id bigint NOT NULL,
    top_message_id integer DEFAULT 0 NOT NULL,
    top_message_date integer DEFAULT 0 NOT NULL,
    read_inbox_max_id integer DEFAULT 0 NOT NULL,
    read_outbox_max_id integer DEFAULT 0 NOT NULL,
    unread_count integer DEFAULT 0 NOT NULL,
    unread_mentions_count integer DEFAULT 0 NOT NULL,
    unread_reactions_count integer DEFAULT 0 NOT NULL,
    pinned boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    pinned_order integer DEFAULT 0 NOT NULL,
    unread_mark boolean DEFAULT false NOT NULL,
    hidden_peer_settings_bar boolean DEFAULT false NOT NULL,
    folder_id integer DEFAULT 0 NOT NULL,
    ttl_period integer DEFAULT 0 NOT NULL,
    has_scheduled boolean DEFAULT false NOT NULL,
    theme_emoticon text DEFAULT ''::text NOT NULL,
    CONSTRAINT dialogs_folder_id_check CHECK ((folder_id >= 0)),
    CONSTRAINT dialogs_peer_type_check CHECK (((peer_type)::text = 'user'::text))
);


--
-- Name: dispatch_outbox; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dispatch_outbox (
    id bigint NOT NULL,
    target_user_id bigint NOT NULL,
    pts integer NOT NULL,
    event_type character varying(32) NOT NULL,
    exclude_session_id bigint DEFAULT 0 NOT NULL,
    exclude_auth_key_id bigint DEFAULT 0 NOT NULL,
    status character varying(16) DEFAULT 'pending'::character varying NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT dispatch_outbox_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('dispatching'::character varying)::text, ('failed'::character varying)::text]))),
    CONSTRAINT dispatch_outbox_exclusion_pair_check CHECK ((((exclude_auth_key_id = 0) AND (exclude_session_id = 0)) OR ((exclude_auth_key_id <> 0) AND (exclude_session_id <> 0))))
);


--
-- Name: dispatch_outbox_v2_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.dispatch_outbox_v2_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: dispatch_outbox_v2_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.dispatch_outbox_v2_id_seq OWNED BY public.dispatch_outbox.id;


--
-- Name: documents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.documents (
    id bigint NOT NULL,
    access_hash bigint DEFAULT 0 NOT NULL,
    file_reference bytea DEFAULT '\x'::bytea NOT NULL,
    date integer DEFAULT 0 NOT NULL,
    mime_type text DEFAULT ''::text NOT NULL,
    size bigint DEFAULT 0 NOT NULL,
    dc_id integer DEFAULT 0 NOT NULL,
    attributes jsonb DEFAULT '[]'::jsonb NOT NULL,
    thumbs jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: encrypted_files; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.encrypted_files (
    id bigint NOT NULL,
    access_hash bigint NOT NULL,
    owner_user_id bigint NOT NULL,
    size bigint NOT NULL,
    dc_id integer NOT NULL,
    key_fingerprint integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: encrypted_message_queue; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.encrypted_message_queue (
    receiver_auth_key_id bigint NOT NULL,
    qts integer NOT NULL,
    receiver_user_id bigint NOT NULL,
    chat_id integer NOT NULL,
    random_id bigint NOT NULL,
    date integer NOT NULL,
    is_service boolean DEFAULT false NOT NULL,
    bytes bytea NOT NULL,
    file_id bigint,
    file_access_hash bigint,
    file_size bigint,
    file_dc_id integer,
    file_key_fingerprint integer,
    acked boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: encrypted_state_event_delivery; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.encrypted_state_event_delivery (
    event_id bigint NOT NULL,
    auth_key_id bigint NOT NULL
);


--
-- Name: encrypted_state_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.encrypted_state_events (
    id bigint NOT NULL,
    target_user_id bigint NOT NULL,
    target_auth_key_id bigint DEFAULT 0 NOT NULL,
    chat_id integer NOT NULL,
    event_type smallint NOT NULL,
    max_date integer DEFAULT 0 NOT NULL,
    date integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: encrypted_state_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.encrypted_state_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: encrypted_state_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.encrypted_state_events_id_seq OWNED BY public.encrypted_state_events.id;


--
-- Name: file_blobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.file_blobs (
    location_key text NOT NULL,
    backend text DEFAULT 'localfs'::text NOT NULL,
    object_key text NOT NULL,
    size bigint DEFAULT 0 NOT NULL,
    sha256 bytea DEFAULT '\x'::bytea NOT NULL,
    mime_type text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: group_call_participant_overrides; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.group_call_participant_overrides (
    call_id bigint NOT NULL,
    setter_user_id bigint NOT NULL,
    target_user_id bigint NOT NULL,
    muted_by_you boolean DEFAULT false NOT NULL,
    volume integer DEFAULT 0 NOT NULL
);


--
-- Name: group_call_participants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.group_call_participants (
    call_id bigint NOT NULL,
    user_id bigint NOT NULL,
    ssrc bigint NOT NULL,
    join_date integer NOT NULL,
    active_date integer DEFAULT 0 NOT NULL,
    muted boolean DEFAULT false NOT NULL,
    muted_by_admin boolean DEFAULT false NOT NULL,
    volume_by_admin integer DEFAULT 0 NOT NULL,
    raise_hand_rating bigint DEFAULT 0 NOT NULL,
    video_json jsonb,
    presentation_json jsonb,
    left_call boolean DEFAULT false NOT NULL,
    last_check_date integer DEFAULT 0 NOT NULL
);


--
-- Name: group_calls; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.group_calls (
    call_id bigint NOT NULL,
    access_hash bigint NOT NULL,
    channel_id bigint NOT NULL,
    creator_user_id bigint NOT NULL,
    state text DEFAULT 'active'::text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    join_muted boolean DEFAULT false NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    participants_count integer DEFAULT 0 NOT NULL,
    created_at integer NOT NULL,
    discarded_at integer DEFAULT 0 NOT NULL,
    duration integer DEFAULT 0 NOT NULL,
    started_msg_id integer DEFAULT 0 NOT NULL,
    CONSTRAINT group_calls_state_check CHECK ((state = ANY (ARRAY['active'::text, 'discarded'::text])))
);


--
-- Name: lang_pack_strings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.lang_pack_strings (
    lang_pack character varying(32) NOT NULL,
    lang_code character varying(64) NOT NULL,
    key character varying(128) NOT NULL,
    version integer NOT NULL,
    pluralized boolean DEFAULT false NOT NULL,
    value text DEFAULT ''::text NOT NULL,
    zero_value text DEFAULT ''::text NOT NULL,
    one_value text DEFAULT ''::text NOT NULL,
    two_value text DEFAULT ''::text NOT NULL,
    few_value text DEFAULT ''::text NOT NULL,
    many_value text DEFAULT ''::text NOT NULL,
    other_value text DEFAULT ''::text NOT NULL,
    deleted boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: lang_packs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.lang_packs (
    lang_pack character varying(32) NOT NULL,
    lang_code character varying(64) NOT NULL,
    version integer NOT NULL,
    strings_count integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: message_box_media; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.message_box_media (
    owner_user_id bigint NOT NULL,
    box_id integer NOT NULL,
    peer_id bigint NOT NULL,
    category smallint NOT NULL,
    message_date integer NOT NULL
);


--
-- Name: message_boxes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.message_boxes (
    owner_user_id bigint NOT NULL,
    box_id integer NOT NULL,
    private_message_id bigint NOT NULL,
    message_sender_id bigint NOT NULL,
    peer_type character varying(16) NOT NULL,
    peer_id bigint NOT NULL,
    from_user_id bigint NOT NULL,
    message_date integer NOT NULL,
    outgoing boolean DEFAULT false NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    pts integer DEFAULT 0 NOT NULL,
    deleted boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    edit_date integer DEFAULT 0 NOT NULL,
    silent boolean DEFAULT false NOT NULL,
    noforwards boolean DEFAULT false NOT NULL,
    reply_to_msg_id integer DEFAULT 0 NOT NULL,
    reply_to_peer_type character varying(16) DEFAULT ''::character varying NOT NULL,
    reply_to_peer_id bigint DEFAULT 0 NOT NULL,
    reply_to_top_id integer DEFAULT 0 NOT NULL,
    quote_text text DEFAULT ''::text NOT NULL,
    quote_entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    quote_offset integer DEFAULT 0 NOT NULL,
    fwd_from_peer_type character varying(16) DEFAULT ''::character varying NOT NULL,
    fwd_from_peer_id bigint DEFAULT 0 NOT NULL,
    fwd_from_name text DEFAULT ''::text NOT NULL,
    fwd_date integer DEFAULT 0 NOT NULL,
    media jsonb DEFAULT '{}'::jsonb NOT NULL,
    media_unread boolean DEFAULT false NOT NULL,
    reaction_unread boolean DEFAULT false NOT NULL,
    ttl_period integer DEFAULT 0 NOT NULL,
    expires_at integer DEFAULT 0 NOT NULL,
    pinned boolean DEFAULT false NOT NULL,
    saved_peer_type character varying(16) DEFAULT ''::character varying NOT NULL,
    saved_peer_id bigint DEFAULT 0 NOT NULL,
    fwd_saved_from_peer_type character varying(16) DEFAULT ''::character varying NOT NULL,
    fwd_saved_from_peer_id bigint DEFAULT 0 NOT NULL,
    fwd_saved_from_msg_id integer DEFAULT 0 NOT NULL,
    reply_markup jsonb DEFAULT '{}'::jsonb NOT NULL,
    via_bot_id bigint DEFAULT 0 NOT NULL,
    rich_message jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT message_boxes_peer_type_check CHECK (((peer_type)::text = 'user'::text))
);


--
-- Name: photos; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.photos (
    id bigint NOT NULL,
    access_hash bigint DEFAULT 0 NOT NULL,
    file_reference bytea DEFAULT '\x'::bytea NOT NULL,
    date integer DEFAULT 0 NOT NULL,
    dc_id integer DEFAULT 0 NOT NULL,
    has_stickers boolean DEFAULT false NOT NULL,
    sizes jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: poll_votes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.poll_votes (
    poll_id bigint NOT NULL,
    user_id bigint NOT NULL,
    options jsonb NOT NULL,
    vote_date integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: polls; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.polls (
    poll_id bigint NOT NULL,
    creator_user_id bigint NOT NULL,
    multiple_choice boolean DEFAULT false NOT NULL,
    quiz boolean DEFAULT false NOT NULL,
    public_voters boolean DEFAULT false NOT NULL,
    revoting_disabled boolean DEFAULT false NOT NULL,
    hide_results boolean DEFAULT false NOT NULL,
    closed boolean DEFAULT false NOT NULL,
    close_period integer DEFAULT 0 NOT NULL,
    close_date integer DEFAULT 0 NOT NULL,
    options jsonb NOT NULL,
    correct_options jsonb DEFAULT '[]'::jsonb NOT NULL,
    solution text DEFAULT ''::text NOT NULL,
    solution_entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: private_media_category_counts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.private_media_category_counts (
    owner_user_id bigint NOT NULL,
    peer_id bigint NOT NULL,
    category smallint NOT NULL,
    media_count integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT private_media_category_counts_media_count_check CHECK ((media_count >= 0))
);


--
-- Name: private_message_reactions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.private_message_reactions (
    message_sender_id bigint NOT NULL,
    private_message_id bigint NOT NULL,
    user_id bigint NOT NULL,
    reaction_type text NOT NULL,
    reaction_value text NOT NULL,
    big boolean DEFAULT false NOT NULL,
    reaction_date integer NOT NULL,
    chosen_order integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: private_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.private_messages (
    id bigint NOT NULL,
    sender_user_id bigint NOT NULL,
    recipient_user_id bigint NOT NULL,
    random_id bigint DEFAULT 0 NOT NULL,
    message_date integer NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    edit_date integer DEFAULT 0 NOT NULL,
    silent boolean DEFAULT false NOT NULL,
    noforwards boolean DEFAULT false NOT NULL,
    reply_to_msg_id integer DEFAULT 0 NOT NULL,
    reply_to_peer_type character varying(16) DEFAULT ''::character varying NOT NULL,
    reply_to_peer_id bigint DEFAULT 0 NOT NULL,
    reply_to_top_id integer DEFAULT 0 NOT NULL,
    quote_text text DEFAULT ''::text NOT NULL,
    quote_entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    quote_offset integer DEFAULT 0 NOT NULL,
    fwd_from_peer_type character varying(16) DEFAULT ''::character varying NOT NULL,
    fwd_from_peer_id bigint DEFAULT 0 NOT NULL,
    fwd_from_name text DEFAULT ''::text NOT NULL,
    fwd_date integer DEFAULT 0 NOT NULL,
    media jsonb DEFAULT '{}'::jsonb NOT NULL,
    ttl_period integer DEFAULT 0 NOT NULL,
    expires_at integer DEFAULT 0 NOT NULL,
    reply_markup jsonb DEFAULT '{}'::jsonb NOT NULL,
    via_bot_id bigint DEFAULT 0 NOT NULL,
    rich_message jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT private_messages_nonempty_body CHECK (((body <> ''::text) OR (media <> '{}'::jsonb) OR (rich_message <> '{}'::jsonb)))
);


--
-- Name: private_messages_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.private_messages ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.private_messages_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: profile_photos; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.profile_photos (
    owner_peer_type text NOT NULL,
    owner_peer_id bigint NOT NULL,
    photo_id bigint NOT NULL,
    date integer DEFAULT 0 NOT NULL,
    active boolean DEFAULT true NOT NULL,
    sort_order bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    kind text DEFAULT 'profile'::text NOT NULL,
    CONSTRAINT profile_photos_kind_check CHECK ((kind = ANY (ARRAY['profile'::text, 'fallback'::text]))),
    CONSTRAINT profile_photos_peer_type_check CHECK ((owner_peer_type = ANY (ARRAY['user'::text, 'channel'::text])))
);


--
-- Name: quick_replies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.quick_replies (
    owner_user_id bigint NOT NULL,
    shortcut_id integer NOT NULL,
    shortcut text NOT NULL,
    sort_order integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT quick_replies_shortcut_not_empty CHECK ((shortcut <> ''::text))
);


--
-- Name: quick_reply_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.quick_reply_messages (
    owner_user_id bigint NOT NULL,
    shortcut_id integer NOT NULL,
    message_id integer NOT NULL,
    random_id bigint DEFAULT 0 NOT NULL,
    message_date integer NOT NULL,
    body text NOT NULL,
    entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: read_model_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.read_model_versions (
    model text NOT NULL,
    owner_user_id bigint DEFAULT 0 NOT NULL,
    peer_type text DEFAULT ''::text NOT NULL,
    peer_id bigint DEFAULT 0 NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    hash bigint DEFAULT public.telesrv_random_read_model_hash() NOT NULL
);


--
-- Name: saved_dialog_pins; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.saved_dialog_pins (
    user_id bigint NOT NULL,
    peer_type character varying(16) NOT NULL,
    peer_id bigint NOT NULL,
    pinned_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT saved_dialog_pins_peer_type_check CHECK (((peer_type)::text = ANY (ARRAY[('user'::character varying)::text, ('channel'::character varying)::text])))
);


--
-- Name: saved_music; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.saved_music (
    user_id bigint NOT NULL,
    document_id bigint NOT NULL,
    sort_order integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: scheduled_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scheduled_messages (
    owner_user_id bigint NOT NULL,
    scheduled_id integer NOT NULL,
    peer_type character varying(16) NOT NULL,
    peer_id bigint NOT NULL,
    random_id bigint DEFAULT 0 NOT NULL,
    message_date integer DEFAULT 0 NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    media jsonb DEFAULT '{}'::jsonb NOT NULL,
    rich_message jsonb DEFAULT '{}'::jsonb NOT NULL,
    silent boolean DEFAULT false NOT NULL,
    noforwards boolean DEFAULT false NOT NULL,
    reply_to_msg_id integer DEFAULT 0 NOT NULL,
    reply_to_peer_type character varying(16) DEFAULT ''::character varying NOT NULL,
    reply_to_peer_id bigint DEFAULT 0 NOT NULL,
    reply_to_top_id integer DEFAULT 0 NOT NULL,
    quote_text text DEFAULT ''::text NOT NULL,
    quote_entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    quote_offset integer DEFAULT 0 NOT NULL,
    fwd_from_peer_type character varying(16) DEFAULT ''::character varying NOT NULL,
    fwd_from_peer_id bigint DEFAULT 0 NOT NULL,
    fwd_from_name text DEFAULT ''::text NOT NULL,
    fwd_date integer DEFAULT 0 NOT NULL,
    send_as_peer_type character varying(16) DEFAULT ''::character varying NOT NULL,
    send_as_peer_id bigint DEFAULT 0 NOT NULL,
    schedule_date integer NOT NULL,
    schedule_repeat_period integer DEFAULT 0 NOT NULL,
    state character varying(16) DEFAULT 'pending'::character varying NOT NULL,
    lease_until integer DEFAULT 0 NOT NULL,
    sent_message_id integer DEFAULT 0 NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    created_at integer DEFAULT 0 NOT NULL,
    updated_at integer DEFAULT 0 NOT NULL,
    CONSTRAINT scheduled_messages_fwd_peer_type_check CHECK ((((fwd_from_peer_type)::text = ''::text) OR ((fwd_from_peer_type)::text = ANY (ARRAY[('user'::character varying)::text, ('channel'::character varying)::text])))),
    CONSTRAINT scheduled_messages_peer_type_check CHECK (((peer_type)::text = ANY (ARRAY[('user'::character varying)::text, ('channel'::character varying)::text]))),
    CONSTRAINT scheduled_messages_reply_peer_type_check CHECK ((((reply_to_peer_type)::text = ''::text) OR ((reply_to_peer_type)::text = ANY (ARRAY[('user'::character varying)::text, ('channel'::character varying)::text])))),
    CONSTRAINT scheduled_messages_send_as_peer_type_check CHECK ((((send_as_peer_type)::text = ''::text) OR ((send_as_peer_type)::text = ANY (ARRAY[('user'::character varying)::text, ('channel'::character varying)::text])))),
    CONSTRAINT scheduled_messages_state_check CHECK (((state)::text = ANY (ARRAY[('pending'::character varying)::text, ('dispatching'::character varying)::text, ('sent'::character varying)::text, ('deleted'::character varying)::text])))
);


--
-- Name: secret_chats; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.secret_chats (
    chat_id integer NOT NULL,
    admin_access_hash bigint NOT NULL,
    participant_access_hash bigint NOT NULL,
    admin_user_id bigint NOT NULL,
    admin_auth_key_id bigint NOT NULL,
    participant_user_id bigint NOT NULL,
    participant_auth_key_id bigint DEFAULT 0 NOT NULL,
    state text NOT NULL,
    g_a bytea,
    g_b bytea,
    key_fingerprint bigint DEFAULT 0 NOT NULL,
    layer integer DEFAULT 0 NOT NULL,
    folder_id integer DEFAULT 0 NOT NULL,
    history_deleted boolean DEFAULT false NOT NULL,
    random_id integer NOT NULL,
    date integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: secret_qts_watermarks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.secret_qts_watermarks (
    auth_key_id bigint NOT NULL,
    reserved_qts integer DEFAULT 0 NOT NULL,
    confirmed_qts integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: sticker_sets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sticker_sets (
    id bigint NOT NULL,
    access_hash bigint DEFAULT 0 NOT NULL,
    short_name text DEFAULT ''::text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    count integer DEFAULT 0 NOT NULL,
    hash integer DEFAULT 0 NOT NULL,
    set_kind text DEFAULT 'stickers'::text NOT NULL,
    official boolean DEFAULT false NOT NULL,
    animated boolean DEFAULT false NOT NULL,
    videos boolean DEFAULT false NOT NULL,
    emojis boolean DEFAULT false NOT NULL,
    masks boolean DEFAULT false NOT NULL,
    installed boolean DEFAULT false NOT NULL,
    archived boolean DEFAULT false NOT NULL,
    installed_date integer DEFAULT 0 NOT NULL,
    thumb_document_id bigint DEFAULT 0 NOT NULL,
    thumbs jsonb DEFAULT '[]'::jsonb NOT NULL,
    thumb_dc_id integer DEFAULT 0 NOT NULL,
    thumb_version integer DEFAULT 0 NOT NULL,
    document_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    packs jsonb DEFAULT '[]'::jsonb NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    system_key text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: stories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.stories (
    owner_peer_type text NOT NULL,
    owner_peer_id bigint NOT NULL,
    story_id integer NOT NULL,
    random_id bigint DEFAULT 0 NOT NULL,
    date integer NOT NULL,
    expire_date integer NOT NULL,
    deleted boolean DEFAULT false NOT NULL,
    pinned boolean DEFAULT false NOT NULL,
    public boolean DEFAULT false NOT NULL,
    close_friends boolean DEFAULT false NOT NULL,
    contacts boolean DEFAULT false NOT NULL,
    selected_contacts boolean DEFAULT false NOT NULL,
    noforwards boolean DEFAULT false NOT NULL,
    edited boolean DEFAULT false NOT NULL,
    privacy_rules jsonb DEFAULT '[]'::jsonb NOT NULL,
    allow_user_ids bigint[] DEFAULT '{}'::bigint[] NOT NULL,
    disallow_user_ids bigint[] DEFAULT '{}'::bigint[] NOT NULL,
    caption text DEFAULT ''::text NOT NULL,
    entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    media jsonb DEFAULT '{}'::jsonb NOT NULL,
    media_areas jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    pinned_to_top_order integer DEFAULT 0 NOT NULL,
    fwd_from jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT stories_id_check CHECK ((story_id > 0)),
    CONSTRAINT stories_owner_peer_check CHECK (((owner_peer_type = ANY (ARRAY['user'::text, 'channel'::text])) AND (owner_peer_id > 0))),
    CONSTRAINT stories_pinned_to_top_order_check CHECK ((pinned_to_top_order >= 0))
);


--
-- Name: story_hidden_peers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.story_hidden_peers (
    viewer_user_id bigint NOT NULL,
    owner_peer_type text NOT NULL,
    owner_peer_id bigint NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT story_hidden_peers_owner_peer_check CHECK (((viewer_user_id > 0) AND (owner_peer_type = ANY (ARRAY['user'::text, 'channel'::text])) AND (owner_peer_id > 0)))
);


--
-- Name: story_read_states; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.story_read_states (
    viewer_user_id bigint NOT NULL,
    owner_peer_type text NOT NULL,
    owner_peer_id bigint NOT NULL,
    max_read_id integer NOT NULL,
    date integer NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT story_read_states_owner_peer_check CHECK (((viewer_user_id > 0) AND (owner_peer_type = ANY (ARRAY['user'::text, 'channel'::text])) AND (owner_peer_id > 0) AND (max_read_id > 0)))
);


--
-- Name: story_views; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.story_views (
    owner_peer_type text NOT NULL,
    owner_peer_id bigint NOT NULL,
    story_id integer NOT NULL,
    viewer_user_id bigint NOT NULL,
    date integer NOT NULL,
    reaction jsonb DEFAULT '{}'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT story_views_owner_peer_check CHECK (((owner_peer_type = ANY (ARRAY['user'::text, 'channel'::text])) AND (owner_peer_id > 0) AND (story_id > 0) AND (viewer_user_id > 0)))
);


--
-- Name: temp_auth_key_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.temp_auth_key_bindings (
    temp_auth_key_id bigint NOT NULL,
    perm_auth_key_id bigint NOT NULL,
    nonce bigint NOT NULL,
    expires_at integer NOT NULL,
    encrypted_message bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    temp_session_id bigint DEFAULT 0 NOT NULL
);


--
-- Name: update_states; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.update_states (
    auth_key_id bigint NOT NULL,
    pts integer DEFAULT 0 NOT NULL,
    qts integer DEFAULT 0 NOT NULL,
    date integer DEFAULT 0 NOT NULL,
    seq integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    user_id bigint DEFAULT 0 NOT NULL
);


--
-- Name: upload_parts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.upload_parts (
    owner_user_id bigint NOT NULL,
    file_id bigint NOT NULL,
    part integer NOT NULL,
    total_parts integer DEFAULT 0 NOT NULL,
    is_big boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    backend text NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    sha256 bytea NOT NULL
);


--
-- Name: user_business_profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_business_profiles (
    user_id bigint NOT NULL,
    work_hours jsonb DEFAULT '{}'::jsonb NOT NULL,
    location jsonb DEFAULT '{}'::jsonb NOT NULL,
    intro jsonb DEFAULT '{}'::jsonb NOT NULL,
    greeting_message jsonb DEFAULT '{}'::jsonb NOT NULL,
    away_message jsonb DEFAULT '{}'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: user_channel_member_index; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_channel_member_index (
    user_id bigint NOT NULL,
    channel_id bigint NOT NULL,
    status character varying(16) NOT NULL,
    megagroup boolean DEFAULT false NOT NULL,
    broadcast boolean DEFAULT false NOT NULL,
    deleted boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    role character varying(16) DEFAULT 'member'::character varying NOT NULL,
    left_at integer DEFAULT 0 NOT NULL,
    forum boolean DEFAULT false NOT NULL,
    public_username boolean DEFAULT false NOT NULL,
    can_pin_messages boolean DEFAULT false NOT NULL,
    CONSTRAINT user_channel_member_index_role_check CHECK (((role)::text = ANY (ARRAY[('creator'::character varying)::text, ('admin'::character varying)::text, ('member'::character varying)::text]))),
    CONSTRAINT user_channel_member_index_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('left'::character varying)::text, ('kicked'::character varying)::text, ('banned'::character varying)::text])))
);


--
-- Name: user_recent_reactions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_recent_reactions (
    user_id bigint NOT NULL,
    reaction_type character varying(16) NOT NULL,
    reaction_value text NOT NULL,
    reaction_date integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_recent_reactions_reaction_type_check CHECK (((reaction_type)::text = 'emoji'::text)),
    CONSTRAINT user_recent_reactions_reaction_value_check CHECK ((reaction_value <> ''::text))
);


--
-- Name: user_saved_reaction_tags; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_saved_reaction_tags (
    user_id bigint NOT NULL,
    reaction_type character varying(16) NOT NULL,
    reaction_value text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    reaction_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_saved_reaction_tags_reaction_count_check CHECK ((reaction_count >= 0)),
    CONSTRAINT user_saved_reaction_tags_reaction_type_check CHECK (((reaction_type)::text = 'emoji'::text)),
    CONSTRAINT user_saved_reaction_tags_reaction_value_check CHECK ((reaction_value <> ''::text)),
    CONSTRAINT user_saved_reaction_tags_title_check CHECK ((char_length(title) <= 12))
);


--
-- Name: user_top_reactions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_top_reactions (
    user_id bigint NOT NULL,
    reaction_type character varying(16) NOT NULL,
    reaction_value text NOT NULL,
    reaction_count integer DEFAULT 0 NOT NULL,
    reaction_date integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_top_reactions_reaction_count_check CHECK ((reaction_count >= 0)),
    CONSTRAINT user_top_reactions_reaction_type_check CHECK (((reaction_type)::text = 'emoji'::text)),
    CONSTRAINT user_top_reactions_reaction_value_check CHECK ((reaction_value <> ''::text))
);


--
-- Name: user_update_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_update_events (
    user_id bigint NOT NULL,
    pts integer NOT NULL,
    pts_count integer DEFAULT 1 NOT NULL,
    date integer NOT NULL,
    event_type character varying(32) NOT NULL,
    message_box_id integer,
    peer_type character varying(16),
    peer_id bigint,
    max_id integer DEFAULT 0 NOT NULL,
    still_unread_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    event_bool boolean DEFAULT false NOT NULL,
    event_peers jsonb DEFAULT '[]'::jsonb NOT NULL,
    peer_settings jsonb DEFAULT '{}'::jsonb NOT NULL,
    message_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    dialog_filter jsonb DEFAULT '{}'::jsonb NOT NULL,
    filter_order jsonb DEFAULT '[]'::jsonb NOT NULL,
    folder_peers jsonb DEFAULT '[]'::jsonb NOT NULL,
    filter_id integer DEFAULT 0 NOT NULL,
    tags_enabled boolean DEFAULT false NOT NULL,
    channel_pts integer DEFAULT 0 NOT NULL,
    folder_id integer DEFAULT 0 NOT NULL,
    quick_replies jsonb DEFAULT '[]'::jsonb NOT NULL,
    quick_reply_message jsonb DEFAULT '{}'::jsonb NOT NULL,
    story_payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    reaction_payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT user_update_events_peer_type_check CHECK (((peer_type IS NULL) OR ((peer_type)::text = ANY (ARRAY[('user'::character varying)::text, ('channel'::character varying)::text])))),
    CONSTRAINT user_update_events_type_check CHECK (((event_type)::text = ANY (ARRAY[('new_message'::character varying)::text, ('read_history_inbox'::character varying)::text, ('read_history_outbox'::character varying)::text, ('read_message_contents'::character varying)::text, ('edit_message'::character varying)::text, ('message_reactions'::character varying)::text, ('message_poll'::character varying)::text, ('draft_message'::character varying)::text, ('quick_replies'::character varying)::text, ('new_quick_reply'::character varying)::text, ('delete_quick_reply'::character varying)::text, ('quick_reply_message'::character varying)::text, ('delete_quick_reply_messages'::character varying)::text, ('contacts_reset'::character varying)::text, ('dialog_pinned'::character varying)::text, ('pinned_dialogs'::character varying)::text, ('pinned_messages'::character varying)::text, ('dialog_unread_mark'::character varying)::text, ('peer_settings'::character varying)::text, ('peer_story_blocked'::character varying)::text, ('delete_messages'::character varying)::text, ('dialog_filter'::character varying)::text, ('dialog_filter_order'::character varying)::text, ('dialog_filters'::character varying)::text, ('folder_peers'::character varying)::text, ('channel_available_messages'::character varying)::text, ('channel_view_forum_as_messages'::character varying)::text, ('channel_state'::character varying)::text, ('saved_dialog_pinned'::character varying)::text, ('pinned_saved_dialogs'::character varying)::text, ('story'::character varying)::text, ('read_stories'::character varying)::text, ('sent_story_reaction'::character varying)::text, ('new_story_reaction'::character varying)::text, ('noop'::character varying)::text])))
);


--
-- Name: user_update_watermarks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_update_watermarks (
    user_id bigint NOT NULL,
    contiguous_pts integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id bigint NOT NULL,
    access_hash bigint NOT NULL,
    phone character varying(32) NOT NULL,
    first_name character varying(64) DEFAULT ''::character varying NOT NULL,
    last_name character varying(64) DEFAULT ''::character varying NOT NULL,
    username character varying(64) DEFAULT ''::character varying NOT NULL,
    country_code character varying(8) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    verified boolean DEFAULT false NOT NULL,
    support boolean DEFAULT false NOT NULL,
    about character varying(255) DEFAULT ''::character varying NOT NULL,
    last_seen_at bigint DEFAULT 0 NOT NULL,
    default_history_ttl_period integer DEFAULT 0 NOT NULL,
    is_bot boolean DEFAULT false NOT NULL,
    bot_info_version integer DEFAULT 0 NOT NULL,
    premium_expires_at timestamp with time zone,
    emoji_status_document_id bigint DEFAULT 0 NOT NULL,
    emoji_status_until bigint DEFAULT 0 NOT NULL,
    color_set boolean DEFAULT false NOT NULL,
    color integer DEFAULT 0 NOT NULL,
    color_background_emoji_id bigint DEFAULT 0 NOT NULL,
    profile_color_set boolean DEFAULT false NOT NULL,
    profile_color integer DEFAULT 0 NOT NULL,
    profile_color_background_emoji_id bigint DEFAULT 0 NOT NULL
);


--
-- Name: users_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.users ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.users_id_seq
    START WITH 1780243200
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: dispatch_outbox id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dispatch_outbox ALTER COLUMN id SET DEFAULT nextval('public.dispatch_outbox_v2_id_seq'::regclass);


--
-- Name: encrypted_state_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.encrypted_state_events ALTER COLUMN id SET DEFAULT nextval('public.encrypted_state_events_id_seq'::regclass);


--
-- Name: account_passwords account_passwords_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_passwords
    ADD CONSTRAINT account_passwords_pkey PRIMARY KEY (user_id);


--
-- Name: account_privacy_rules account_privacy_rules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_privacy_rules
    ADD CONSTRAINT account_privacy_rules_pkey PRIMARY KEY (owner_user_id, privacy_key);


--
-- Name: account_reaction_settings account_reaction_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_reaction_settings
    ADD CONSTRAINT account_reaction_settings_pkey PRIMARY KEY (user_id);


--
-- Name: app_configs app_configs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.app_configs
    ADD CONSTRAINT app_configs_pkey PRIMARY KEY (client);


--
-- Name: auth_keys auth_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_keys
    ADD CONSTRAINT auth_keys_pkey PRIMARY KEY (auth_key_id);


--
-- Name: authorizations authorizations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.authorizations
    ADD CONSTRAINT authorizations_pkey PRIMARY KEY (auth_key_id);


--
-- Name: available_reactions available_reactions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.available_reactions
    ADD CONSTRAINT available_reactions_pkey PRIMARY KEY (reaction);


--
-- Name: bot_chat_states bot_chat_states_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_chat_states
    ADD CONSTRAINT bot_chat_states_pkey PRIMARY KEY (bot_user_id, user_id);


--
-- Name: bot_user_permissions bot_user_permissions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_user_permissions
    ADD CONSTRAINT bot_user_permissions_pkey PRIMARY KEY (bot_user_id, user_id);


--
-- Name: bots bots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_pkey PRIMARY KEY (bot_user_id);


--
-- Name: business_automation_deliveries business_automation_deliveries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_automation_deliveries
    ADD CONSTRAINT business_automation_deliveries_pkey PRIMARY KEY (owner_user_id, peer_user_id, kind, trigger_message_id);


--
-- Name: business_chat_links business_chat_links_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_chat_links
    ADD CONSTRAINT business_chat_links_pkey PRIMARY KEY (slug);


--
-- Name: business_connected_bot_peer_states business_connected_bot_peer_states_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_connected_bot_peer_states
    ADD CONSTRAINT business_connected_bot_peer_states_pkey PRIMARY KEY (owner_user_id, peer_user_id);


--
-- Name: business_connected_bots business_connected_bots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_connected_bots
    ADD CONSTRAINT business_connected_bots_pkey PRIMARY KEY (owner_user_id);


--
-- Name: channel_admin_log_events channel_admin_log_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_admin_log_events
    ADD CONSTRAINT channel_admin_log_events_pkey PRIMARY KEY (channel_id, id);


--
-- Name: channel_boost_slots channel_boost_slots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_boost_slots
    ADD CONSTRAINT channel_boost_slots_pkey PRIMARY KEY (user_id, slot);


--
-- Name: channel_dialogs channel_dialogs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_dialogs
    ADD CONSTRAINT channel_dialogs_pkey PRIMARY KEY (user_id, channel_id);


--
-- Name: channel_forum_topics channel_forum_topics_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_forum_topics
    ADD CONSTRAINT channel_forum_topics_pkey PRIMARY KEY (channel_id, topic_id);


--
-- Name: channel_invite_hashes channel_invite_hashes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_invite_hashes
    ADD CONSTRAINT channel_invite_hashes_pkey PRIMARY KEY (hash);


--
-- Name: channel_invite_importers channel_invite_importers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_invite_importers
    ADD CONSTRAINT channel_invite_importers_pkey PRIMARY KEY (channel_id, user_id);


--
-- Name: channel_invites channel_invites_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_invites
    ADD CONSTRAINT channel_invites_pkey PRIMARY KEY (channel_id, invite_id);


--
-- Name: channel_media_category_counts channel_media_category_counts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_media_category_counts
    ADD CONSTRAINT channel_media_category_counts_pkey PRIMARY KEY (channel_id, category);


--
-- Name: channel_members channel_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_members
    ADD CONSTRAINT channel_members_pkey PRIMARY KEY (channel_id, user_id);


--
-- Name: channel_message_media channel_message_media_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_message_media
    ADD CONSTRAINT channel_message_media_pkey PRIMARY KEY (channel_id, id, category);


--
-- Name: channel_message_reactions channel_message_reactions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_message_reactions
    ADD CONSTRAINT channel_message_reactions_pkey PRIMARY KEY (channel_id, message_id, reacted_user_id, reaction_type, reaction_value);


--
-- Name: channel_message_viewers channel_message_viewers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_message_viewers
    ADD CONSTRAINT channel_message_viewers_pkey PRIMARY KEY (channel_id, message_id, viewer_user_id);


--
-- Name: channel_messages channel_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_messages
    ADD CONSTRAINT channel_messages_pkey PRIMARY KEY (channel_id, id);


--
-- Name: channel_unread_mention_index channel_unread_mention_index_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_unread_mention_index
    ADD CONSTRAINT channel_unread_mention_index_pkey PRIMARY KEY (channel_id, message_id, user_id);


--
-- Name: channel_unread_mentions channel_unread_mentions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_unread_mentions
    ADD CONSTRAINT channel_unread_mentions_pkey PRIMARY KEY (user_id, channel_id, message_id);


--
-- Name: channel_update_events channel_update_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_update_events
    ADD CONSTRAINT channel_update_events_pkey PRIMARY KEY (channel_id, pts);


--
-- Name: channel_usernames channel_usernames_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_usernames
    ADD CONSTRAINT channel_usernames_pkey PRIMARY KEY (username_lower);


--
-- Name: channels channels_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_pkey PRIMARY KEY (id);


--
-- Name: contact_blocks contact_blocks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contact_blocks
    ADD CONSTRAINT contact_blocks_pkey PRIMARY KEY (owner_user_id, blocked_user_id);


--
-- Name: contacts contacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts
    ADD CONSTRAINT contacts_pkey PRIMARY KEY (user_id, contact_user_id);


--
-- Name: countries countries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.countries
    ADD CONSTRAINT countries_pkey PRIMARY KEY (iso2);


--
-- Name: country_codes country_codes_iso2_country_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.country_codes
    ADD CONSTRAINT country_codes_iso2_country_code_key UNIQUE (iso2, country_code);


--
-- Name: country_codes country_codes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.country_codes
    ADD CONSTRAINT country_codes_pkey PRIMARY KEY (id);


--
-- Name: dialog_drafts dialog_drafts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dialog_drafts
    ADD CONSTRAINT dialog_drafts_pkey PRIMARY KEY (user_id, peer_type, peer_id, top_message_id);


--
-- Name: dialog_filter_settings dialog_filter_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dialog_filter_settings
    ADD CONSTRAINT dialog_filter_settings_pkey PRIMARY KEY (user_id);


--
-- Name: dialog_filters dialog_filters_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dialog_filters
    ADD CONSTRAINT dialog_filters_pkey PRIMARY KEY (user_id, filter_id);


--
-- Name: dialogs dialogs_pkey1; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dialogs
    ADD CONSTRAINT dialogs_pkey1 PRIMARY KEY (user_id, peer_type, peer_id);


--
-- Name: dispatch_outbox dispatch_outbox_pkey1; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dispatch_outbox
    ADD CONSTRAINT dispatch_outbox_pkey1 PRIMARY KEY (id);


--
-- Name: dispatch_outbox dispatch_outbox_target_user_id_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dispatch_outbox
    ADD CONSTRAINT dispatch_outbox_target_user_id_id_key UNIQUE (target_user_id, id);


--
-- Name: documents documents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documents
    ADD CONSTRAINT documents_pkey PRIMARY KEY (id);


--
-- Name: encrypted_files encrypted_files_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.encrypted_files
    ADD CONSTRAINT encrypted_files_pkey PRIMARY KEY (id);


--
-- Name: encrypted_message_queue encrypted_message_queue_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.encrypted_message_queue
    ADD CONSTRAINT encrypted_message_queue_pkey PRIMARY KEY (receiver_auth_key_id, qts);


--
-- Name: encrypted_state_event_delivery encrypted_state_event_delivery_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.encrypted_state_event_delivery
    ADD CONSTRAINT encrypted_state_event_delivery_pkey PRIMARY KEY (event_id, auth_key_id);


--
-- Name: encrypted_state_events encrypted_state_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.encrypted_state_events
    ADD CONSTRAINT encrypted_state_events_pkey PRIMARY KEY (id);


--
-- Name: file_blobs file_blobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.file_blobs
    ADD CONSTRAINT file_blobs_pkey PRIMARY KEY (location_key);


--
-- Name: group_call_participant_overrides group_call_participant_overrides_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_call_participant_overrides
    ADD CONSTRAINT group_call_participant_overrides_pkey PRIMARY KEY (call_id, setter_user_id, target_user_id);


--
-- Name: group_call_participants group_call_participants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_call_participants
    ADD CONSTRAINT group_call_participants_pkey PRIMARY KEY (call_id, user_id);


--
-- Name: group_calls group_calls_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_calls
    ADD CONSTRAINT group_calls_pkey PRIMARY KEY (call_id);


--
-- Name: lang_pack_strings lang_pack_strings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lang_pack_strings
    ADD CONSTRAINT lang_pack_strings_pkey PRIMARY KEY (lang_pack, lang_code, key);


--
-- Name: lang_packs lang_packs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lang_packs
    ADD CONSTRAINT lang_packs_pkey PRIMARY KEY (lang_pack, lang_code);


--
-- Name: message_box_media message_box_media_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_box_media
    ADD CONSTRAINT message_box_media_pkey PRIMARY KEY (owner_user_id, box_id, category);


--
-- Name: message_boxes message_boxes_owner_user_id_private_message_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_boxes
    ADD CONSTRAINT message_boxes_owner_user_id_private_message_id_key UNIQUE (owner_user_id, private_message_id);


--
-- Name: message_boxes message_boxes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_boxes
    ADD CONSTRAINT message_boxes_pkey PRIMARY KEY (owner_user_id, box_id);


--
-- Name: photos photos_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.photos
    ADD CONSTRAINT photos_pkey PRIMARY KEY (id);


--
-- Name: poll_votes poll_votes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.poll_votes
    ADD CONSTRAINT poll_votes_pkey PRIMARY KEY (poll_id, user_id);


--
-- Name: polls polls_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.polls
    ADD CONSTRAINT polls_pkey PRIMARY KEY (poll_id);


--
-- Name: private_media_category_counts private_media_category_counts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.private_media_category_counts
    ADD CONSTRAINT private_media_category_counts_pkey PRIMARY KEY (owner_user_id, peer_id, category);


--
-- Name: private_message_reactions private_message_reactions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.private_message_reactions
    ADD CONSTRAINT private_message_reactions_pkey PRIMARY KEY (message_sender_id, private_message_id, user_id, reaction_type, reaction_value);


--
-- Name: private_messages private_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.private_messages
    ADD CONSTRAINT private_messages_pkey PRIMARY KEY (sender_user_id, id);


--
-- Name: profile_photos profile_photos_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.profile_photos
    ADD CONSTRAINT profile_photos_pkey PRIMARY KEY (owner_peer_type, owner_peer_id, kind, photo_id);


--
-- Name: quick_replies quick_replies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.quick_replies
    ADD CONSTRAINT quick_replies_pkey PRIMARY KEY (owner_user_id, shortcut_id);


--
-- Name: quick_reply_messages quick_reply_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.quick_reply_messages
    ADD CONSTRAINT quick_reply_messages_pkey PRIMARY KEY (owner_user_id, shortcut_id, message_id);


--
-- Name: read_model_versions read_model_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.read_model_versions
    ADD CONSTRAINT read_model_versions_pkey PRIMARY KEY (model, owner_user_id, peer_type, peer_id);


--
-- Name: saved_dialog_pins saved_dialog_pins_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saved_dialog_pins
    ADD CONSTRAINT saved_dialog_pins_pkey PRIMARY KEY (user_id, peer_type, peer_id);


--
-- Name: saved_music saved_music_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saved_music
    ADD CONSTRAINT saved_music_pkey PRIMARY KEY (user_id, document_id);


--
-- Name: scheduled_messages scheduled_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scheduled_messages
    ADD CONSTRAINT scheduled_messages_pkey PRIMARY KEY (owner_user_id, scheduled_id);


--
-- Name: secret_chats secret_chats_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.secret_chats
    ADD CONSTRAINT secret_chats_pkey PRIMARY KEY (chat_id);


--
-- Name: secret_qts_watermarks secret_qts_watermarks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.secret_qts_watermarks
    ADD CONSTRAINT secret_qts_watermarks_pkey PRIMARY KEY (auth_key_id);


--
-- Name: sticker_sets sticker_sets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sticker_sets
    ADD CONSTRAINT sticker_sets_pkey PRIMARY KEY (id);


--
-- Name: stories stories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stories
    ADD CONSTRAINT stories_pkey PRIMARY KEY (owner_peer_type, owner_peer_id, story_id);


--
-- Name: story_hidden_peers story_hidden_peers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.story_hidden_peers
    ADD CONSTRAINT story_hidden_peers_pkey PRIMARY KEY (viewer_user_id, owner_peer_type, owner_peer_id);


--
-- Name: story_read_states story_read_states_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.story_read_states
    ADD CONSTRAINT story_read_states_pkey PRIMARY KEY (viewer_user_id, owner_peer_type, owner_peer_id);


--
-- Name: story_views story_views_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.story_views
    ADD CONSTRAINT story_views_pkey PRIMARY KEY (owner_peer_type, owner_peer_id, story_id, viewer_user_id);


--
-- Name: temp_auth_key_bindings temp_auth_key_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.temp_auth_key_bindings
    ADD CONSTRAINT temp_auth_key_bindings_pkey PRIMARY KEY (temp_auth_key_id);


--
-- Name: update_states update_states_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.update_states
    ADD CONSTRAINT update_states_pkey PRIMARY KEY (auth_key_id, user_id);


--
-- Name: upload_parts upload_parts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.upload_parts
    ADD CONSTRAINT upload_parts_pkey PRIMARY KEY (owner_user_id, file_id, part);


--
-- Name: user_business_profiles user_business_profiles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_business_profiles
    ADD CONSTRAINT user_business_profiles_pkey PRIMARY KEY (user_id);


--
-- Name: user_channel_member_index user_channel_member_index_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_member_index
    ADD CONSTRAINT user_channel_member_index_pkey PRIMARY KEY (user_id, channel_id);


--
-- Name: user_recent_reactions user_recent_reactions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_recent_reactions
    ADD CONSTRAINT user_recent_reactions_pkey PRIMARY KEY (user_id, reaction_type, reaction_value);


--
-- Name: user_saved_reaction_tags user_saved_reaction_tags_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_saved_reaction_tags
    ADD CONSTRAINT user_saved_reaction_tags_pkey PRIMARY KEY (user_id, reaction_type, reaction_value);


--
-- Name: user_top_reactions user_top_reactions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_top_reactions
    ADD CONSTRAINT user_top_reactions_pkey PRIMARY KEY (user_id, reaction_type, reaction_value);


--
-- Name: user_update_events user_update_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_update_events
    ADD CONSTRAINT user_update_events_pkey PRIMARY KEY (user_id, pts);


--
-- Name: user_update_watermarks user_update_watermarks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_update_watermarks
    ADD CONSTRAINT user_update_watermarks_pkey PRIMARY KEY (user_id);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: authorizations_user_hash_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX authorizations_user_hash_idx ON public.authorizations USING btree (user_id, hash);


--
-- Name: authorizations_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX authorizations_user_id_idx ON public.authorizations USING btree (user_id);


--
-- Name: bot_user_permissions_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bot_user_permissions_user_id_idx ON public.bot_user_permissions USING btree (user_id);


--
-- Name: bots_owner_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bots_owner_user_id_idx ON public.bots USING btree (owner_user_id);


--
-- Name: business_automation_deliveries_last_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX business_automation_deliveries_last_idx ON public.business_automation_deliveries USING btree (owner_user_id, peer_user_id, kind, sent_at DESC, trigger_message_id DESC);


--
-- Name: business_chat_links_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX business_chat_links_owner_idx ON public.business_chat_links USING btree (owner_user_id, created_at, slug);


--
-- Name: business_connected_bot_peer_states_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX business_connected_bot_peer_states_owner_idx ON public.business_connected_bot_peer_states USING btree (owner_user_id, disabled, peer_user_id);


--
-- Name: business_connected_bots_bot_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX business_connected_bots_bot_idx ON public.business_connected_bots USING btree (bot_user_id, owner_user_id);


--
-- Name: channel_admin_log_events_actor_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_admin_log_events_actor_idx ON public.channel_admin_log_events USING btree (channel_id, actor_user_id, id DESC);


--
-- Name: channel_admin_log_events_scan_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_admin_log_events_scan_idx ON public.channel_admin_log_events USING btree (channel_id, id DESC);


--
-- Name: channel_admin_log_events_type_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_admin_log_events_type_idx ON public.channel_admin_log_events USING btree (channel_id, event_type, id DESC);


--
-- Name: channel_boost_slots_peer_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_boost_slots_peer_idx ON public.channel_boost_slots USING btree (peer_type, peer_id, expires_at DESC, assigned_at DESC, user_id, slot) WHERE (peer_id <> 0);


--
-- Name: channel_boost_slots_user_peer_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_boost_slots_user_peer_idx ON public.channel_boost_slots USING btree (user_id, peer_type, peer_id, slot) WHERE (peer_id <> 0);


--
-- Name: channel_dialogs_pinned_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_dialogs_pinned_idx ON public.channel_dialogs USING btree (user_id, pinned) WHERE pinned;


--
-- Name: channel_dialogs_user_top_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_dialogs_user_top_idx ON public.channel_dialogs USING btree (user_id, folder_id, pinned DESC, pinned_order DESC, top_message_date DESC, top_message_id DESC, channel_id DESC);


--
-- Name: channel_forum_topics_page_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_forum_topics_page_idx ON public.channel_forum_topics USING btree (channel_id, pinned DESC, pinned_order DESC, date DESC, topic_id DESC) WHERE (NOT deleted);


--
-- Name: channel_forum_topics_title_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_forum_topics_title_idx ON public.channel_forum_topics USING btree (channel_id, lower(title), date DESC, topic_id DESC) WHERE (NOT deleted);


--
-- Name: channel_invite_importers_link_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_invite_importers_link_idx ON public.channel_invite_importers USING btree (channel_id, invite_id, requested, date DESC, user_id DESC);


--
-- Name: channel_invite_importers_requested_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_invite_importers_requested_idx ON public.channel_invite_importers USING btree (channel_id, requested, date DESC, user_id DESC);


--
-- Name: channel_invites_hash_lookup_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_invites_hash_lookup_idx ON public.channel_invite_hashes USING btree (hash, channel_id, invite_id);


--
-- Name: channel_members_channel_active_user_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_members_channel_active_user_idx ON public.channel_members USING btree (channel_id, user_id) WHERE ((status)::text = 'active'::text);


--
-- Name: channel_members_channel_role_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_members_channel_role_idx ON public.channel_members USING btree (channel_id, role, user_id) WHERE ((status)::text = 'active'::text);


--
-- Name: channel_members_read_participants_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_members_read_participants_idx ON public.channel_members USING btree (channel_id, read_inbox_max_id, user_id) WHERE ((status)::text = 'active'::text);


--
-- Name: channel_members_user_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_members_user_active_idx ON public.channel_members USING btree (user_id, channel_id) WHERE ((status)::text = 'active'::text);


--
-- Name: channel_members_user_left_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_members_user_left_idx ON public.channel_members USING btree (user_id, left_at DESC, channel_id DESC) WHERE ((status)::text = 'left'::text);


--
-- Name: channel_message_media_seek_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_message_media_seek_idx ON public.channel_message_media USING btree (channel_id, category, id DESC);


--
-- Name: channel_message_reactions_msg_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_message_reactions_msg_date_idx ON public.channel_message_reactions USING btree (channel_id, message_id, reaction_date DESC, reacted_user_id DESC, reaction_value);


--
-- Name: channel_message_reactions_unread_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_message_reactions_unread_owner_idx ON public.channel_message_reactions USING btree (channel_id, sender_user_id, message_id DESC) WHERE unread;


--
-- Name: channel_message_reactions_value_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_message_reactions_value_date_idx ON public.channel_message_reactions USING btree (channel_id, message_id, reaction_type, reaction_value, reaction_date DESC, reacted_user_id DESC);


--
-- Name: channel_messages_body_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_messages_body_trgm_idx ON public.channel_messages USING gin (body public.gin_trgm_ops) WHERE ((NOT deleted) AND (body <> ''::text));


--
-- Name: channel_messages_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_messages_date_idx ON public.channel_messages USING btree (channel_id, message_date DESC, id DESC) WHERE (NOT deleted);


--
-- Name: channel_messages_discussion_ref_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_messages_discussion_ref_idx ON public.channel_messages USING btree (discussion_channel_id, discussion_message_id) WHERE ((discussion_channel_id <> 0) AND (discussion_message_id <> 0) AND (NOT deleted));


--
-- Name: channel_messages_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_messages_expiry_idx ON public.channel_messages USING btree (expires_at, channel_id, id) WHERE ((expires_at > 0) AND (NOT deleted));


--
-- Name: channel_messages_history_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_messages_history_idx ON public.channel_messages USING btree (channel_id, id DESC) WHERE (NOT deleted);


--
-- Name: channel_messages_pinned_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_messages_pinned_idx ON public.channel_messages USING btree (channel_id, id DESC) WHERE pinned;


--
-- Name: channel_messages_random_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX channel_messages_random_idx ON public.channel_messages USING btree (channel_id, sender_user_id, random_id) WHERE (random_id <> 0);


--
-- Name: channel_messages_reply_thread_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_messages_reply_thread_idx ON public.channel_messages USING btree (channel_id, reply_to_top_id, id DESC) WHERE ((reply_to_top_id > 0) AND (NOT deleted));


--
-- Name: channel_messages_sender_history_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_messages_sender_history_idx ON public.channel_messages USING btree (channel_id, sender_user_id, id DESC) WHERE (NOT deleted);


--
-- Name: channel_messages_story_forward_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_messages_story_forward_idx ON public.channel_messages USING btree (((media #>> '{story,peer,type}'::text[])), ((media #>> '{story,peer,id}'::text[])), ((media #>> '{story,id}'::text[])), message_date DESC, channel_id, id DESC) WHERE ((NOT deleted) AND ((media ->> 'kind'::text) = 'story'::text));


--
-- Name: channel_unread_mention_index_user_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_unread_mention_index_user_idx ON public.channel_unread_mention_index USING btree (channel_id, user_id, message_id);


--
-- Name: channel_unread_mentions_peer_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_unread_mentions_peer_idx ON public.channel_unread_mentions USING btree (user_id, channel_id, top_message_id, message_id DESC);


--
-- Name: channel_update_events_scan_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channel_update_events_scan_idx ON public.channel_update_events USING btree (channel_id, pts);


--
-- Name: channels_access_hash_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX channels_access_hash_idx ON public.channels USING btree (id, access_hash);


--
-- Name: channels_creator_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channels_creator_idx ON public.channels USING btree (creator_user_id, id DESC) WHERE (NOT deleted);


--
-- Name: channels_linked_chat_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channels_linked_chat_idx ON public.channels USING btree (linked_chat_id) WHERE ((linked_chat_id <> 0) AND (NOT deleted));


--
-- Name: channels_public_broadcast_recommendations_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channels_public_broadcast_recommendations_idx ON public.channels USING btree (participants_count DESC, date DESC, id DESC) WHERE (broadcast AND (NOT megagroup) AND (NOT deleted) AND (COALESCE(username, ''::text) <> ''::text));


--
-- Name: channels_public_title_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channels_public_title_trgm_idx ON public.channels USING gin (lower(title) public.gin_trgm_ops) WHERE ((NOT deleted) AND (COALESCE(username, ''::text) <> ''::text));


--
-- Name: channels_public_username_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX channels_public_username_trgm_idx ON public.channels USING gin (lower(username) public.gin_trgm_ops) WHERE ((NOT deleted) AND (COALESCE(username, ''::text) <> ''::text));


--
-- Name: contact_blocks_owner_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX contact_blocks_owner_date_idx ON public.contact_blocks USING btree (owner_user_id, date DESC, blocked_user_id DESC);


--
-- Name: contacts_contact_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX contacts_contact_user_id_idx ON public.contacts USING btree (contact_user_id);


--
-- Name: contacts_personal_photo_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX contacts_personal_photo_idx ON public.contacts USING btree (user_id, contact_user_id) WHERE (personal_photo_id <> 0);


--
-- Name: contacts_user_name_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX contacts_user_name_idx ON public.contacts USING btree (user_id, contact_first_name, contact_last_name, contact_user_id);


--
-- Name: contacts_user_saved_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX contacts_user_saved_name_trgm_idx ON public.contacts USING gin (lower(TRIM(BOTH FROM (((contact_first_name)::text || ' '::text) || (contact_last_name)::text))) public.gin_trgm_ops);


--
-- Name: dialog_drafts_user_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dialog_drafts_user_date_idx ON public.dialog_drafts USING btree (user_id, date DESC, peer_type, peer_id, top_message_id);


--
-- Name: dialog_filters_user_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dialog_filters_user_order_idx ON public.dialog_filters USING btree (user_id, order_value, filter_id);


--
-- Name: dialogs_user_folder_top_message_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dialogs_user_folder_top_message_idx ON public.dialogs USING btree (user_id, folder_id, pinned DESC, top_message_date DESC, top_message_id DESC, peer_id DESC);


--
-- Name: dialogs_user_pinned_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dialogs_user_pinned_order_idx ON public.dialogs USING btree (user_id, pinned, pinned_order, top_message_date DESC, top_message_id DESC, peer_id DESC) WHERE pinned;


--
-- Name: dispatch_outbox_dispatching_stale_ready_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dispatch_outbox_dispatching_stale_ready_idx ON public.dispatch_outbox USING btree (updated_at, target_user_id, pts, id) WHERE ((status)::text = 'dispatching'::text);


--
-- Name: dispatch_outbox_failed_cleanup_ready_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dispatch_outbox_failed_cleanup_ready_idx ON public.dispatch_outbox USING btree (updated_at, target_user_id, id) WHERE ((status)::text = 'failed'::text);


--
-- Name: dispatch_outbox_pending_ready_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dispatch_outbox_pending_ready_idx ON public.dispatch_outbox USING btree (next_attempt_at, target_user_id, pts, id) WHERE ((status)::text = 'pending'::text);


--
-- Name: group_call_participants_ssrc_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX group_call_participants_ssrc_uniq ON public.group_call_participants USING btree (call_id, ssrc) WHERE (NOT left_call);


--
-- Name: group_call_participants_sweep_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX group_call_participants_sweep_idx ON public.group_call_participants USING btree (call_id, last_check_date) WHERE (NOT left_call);


--
-- Name: group_calls_active_channel_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX group_calls_active_channel_uniq ON public.group_calls USING btree (channel_id) WHERE (state = 'active'::text);


--
-- Name: idx_emq_unacked; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_emq_unacked ON public.encrypted_message_queue USING btree (receiver_auth_key_id, qts) WHERE (acked = false);


--
-- Name: idx_ese_target; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ese_target ON public.encrypted_state_events USING btree (target_user_id, id);


--
-- Name: idx_secret_chats_admin; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_secret_chats_admin ON public.secret_chats USING btree (admin_user_id);


--
-- Name: idx_secret_chats_participant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_secret_chats_participant ON public.secret_chats USING btree (participant_user_id);


--
-- Name: lang_pack_strings_pack_version_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX lang_pack_strings_pack_version_idx ON public.lang_pack_strings USING btree (lang_pack, lang_code, version);


--
-- Name: message_box_media_seek_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_box_media_seek_idx ON public.message_box_media USING btree (owner_user_id, peer_id, category, box_id DESC);


--
-- Name: message_boxes_body_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_boxes_body_trgm_idx ON public.message_boxes USING gin (body public.gin_trgm_ops) WHERE ((NOT deleted) AND (body <> ''::text));


--
-- Name: message_boxes_dialog_date_seek_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_boxes_dialog_date_seek_idx ON public.message_boxes USING btree (owner_user_id, peer_type, peer_id, message_date DESC, box_id DESC) WHERE (NOT deleted);


--
-- Name: message_boxes_dialog_seek_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_boxes_dialog_seek_idx ON public.message_boxes USING btree (owner_user_id, peer_type, peer_id, box_id DESC) WHERE (NOT deleted);


--
-- Name: message_boxes_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_boxes_expiry_idx ON public.message_boxes USING btree (expires_at, owner_user_id, box_id) WHERE ((expires_at > 0) AND (NOT deleted));


--
-- Name: message_boxes_owner_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_boxes_owner_date_idx ON public.message_boxes USING btree (owner_user_id, message_date DESC, box_id DESC) WHERE (NOT deleted);


--
-- Name: message_boxes_pinned_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_boxes_pinned_idx ON public.message_boxes USING btree (owner_user_id, peer_type, peer_id, box_id DESC) WHERE (pinned AND (NOT deleted));


--
-- Name: message_boxes_private_lookup_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_boxes_private_lookup_idx ON public.message_boxes USING btree (private_message_id, owner_user_id);


--
-- Name: message_boxes_private_sender_live_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_boxes_private_sender_live_idx ON public.message_boxes USING btree (message_sender_id, private_message_id) WHERE (NOT deleted);


--
-- Name: message_boxes_private_sender_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_boxes_private_sender_owner_idx ON public.message_boxes USING btree (message_sender_id, private_message_id, owner_user_id) WHERE (NOT deleted);


--
-- Name: message_boxes_read_receipt_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_boxes_read_receipt_idx ON public.message_boxes USING btree (owner_user_id, peer_type, peer_id, box_id DESC) WHERE ((NOT deleted) AND (NOT outgoing));


--
-- Name: message_boxes_reply_lookup_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_boxes_reply_lookup_idx ON public.message_boxes USING btree (owner_user_id, peer_type, peer_id, box_id) WHERE (NOT deleted);


--
-- Name: message_boxes_saved_dialog_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX message_boxes_saved_dialog_idx ON public.message_boxes USING btree (owner_user_id, saved_peer_type, saved_peer_id, box_id DESC) WHERE ((NOT deleted) AND ((saved_peer_type)::text <> ''::text));


--
-- Name: poll_votes_poll_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX poll_votes_poll_date_idx ON public.poll_votes USING btree (poll_id, vote_date DESC, user_id DESC);


--
-- Name: private_message_reactions_message_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX private_message_reactions_message_idx ON public.private_message_reactions USING btree (message_sender_id, private_message_id, reaction_date DESC, user_id);


--
-- Name: private_messages_recipient_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX private_messages_recipient_date_idx ON public.private_messages USING btree (recipient_user_id, message_date DESC, id DESC);


--
-- Name: private_messages_sender_random_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX private_messages_sender_random_idx ON public.private_messages USING btree (sender_user_id, random_id) WHERE (random_id <> 0);


--
-- Name: profile_photos_current_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX profile_photos_current_idx ON public.profile_photos USING btree (owner_peer_type, owner_peer_id, kind, sort_order DESC) WHERE active;


--
-- Name: quick_replies_owner_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX quick_replies_owner_order_idx ON public.quick_replies USING btree (owner_user_id, sort_order, shortcut_id);


--
-- Name: quick_replies_owner_shortcut_lower_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX quick_replies_owner_shortcut_lower_idx ON public.quick_replies USING btree (owner_user_id, lower(shortcut));


--
-- Name: quick_reply_messages_owner_shortcut_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX quick_reply_messages_owner_shortcut_idx ON public.quick_reply_messages USING btree (owner_user_id, shortcut_id, message_id);


--
-- Name: read_model_versions_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX read_model_versions_owner_idx ON public.read_model_versions USING btree (owner_user_id, model);


--
-- Name: saved_dialog_pins_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX saved_dialog_pins_order_idx ON public.saved_dialog_pins USING btree (user_id, pinned_order, peer_id);


--
-- Name: saved_music_user_order_document_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX saved_music_user_order_document_idx ON public.saved_music USING btree (user_id, sort_order, document_id);


--
-- Name: saved_music_user_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX saved_music_user_order_idx ON public.saved_music USING btree (user_id, sort_order);


--
-- Name: scheduled_messages_due_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX scheduled_messages_due_idx ON public.scheduled_messages USING btree (schedule_date, owner_user_id, scheduled_id) WHERE ((state)::text = 'pending'::text);


--
-- Name: scheduled_messages_lease_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX scheduled_messages_lease_idx ON public.scheduled_messages USING btree (lease_until, owner_user_id, scheduled_id) WHERE ((state)::text = 'dispatching'::text);


--
-- Name: scheduled_messages_owner_random_pending_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX scheduled_messages_owner_random_pending_idx ON public.scheduled_messages USING btree (owner_user_id, random_id) WHERE ((random_id <> 0) AND ((state)::text = ANY (ARRAY[('pending'::character varying)::text, ('dispatching'::character varying)::text])));


--
-- Name: scheduled_messages_peer_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX scheduled_messages_peer_idx ON public.scheduled_messages USING btree (owner_user_id, peer_type, peer_id, schedule_date, scheduled_id) WHERE ((state)::text = ANY (ARRAY[('pending'::character varying)::text, ('dispatching'::character varying)::text]));


--
-- Name: sticker_sets_kind_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX sticker_sets_kind_order_idx ON public.sticker_sets USING btree (set_kind, sort_order, id);


--
-- Name: sticker_sets_short_name_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX sticker_sets_short_name_idx ON public.sticker_sets USING btree (short_name) WHERE (short_name <> ''::text);


--
-- Name: sticker_sets_system_key_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX sticker_sets_system_key_idx ON public.sticker_sets USING btree (system_key) WHERE (system_key <> ''::text);


--
-- Name: stories_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX stories_active_idx ON public.stories USING btree (public, expire_date DESC, date DESC, owner_peer_type, owner_peer_id, story_id DESC) WHERE (deleted = false);


--
-- Name: stories_allow_user_ids_gin_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX stories_allow_user_ids_gin_idx ON public.stories USING gin (allow_user_ids) WHERE (deleted = false);


--
-- Name: stories_disallow_user_ids_gin_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX stories_disallow_user_ids_gin_idx ON public.stories USING gin (disallow_user_ids) WHERE (deleted = false);


--
-- Name: stories_owner_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX stories_owner_active_idx ON public.stories USING btree (owner_peer_type, owner_peer_id, expire_date DESC, story_id) WHERE (deleted = false);


--
-- Name: stories_owner_archive_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX stories_owner_archive_idx ON public.stories USING btree (owner_peer_type, owner_peer_id, story_id DESC) WHERE (deleted = false);


--
-- Name: stories_owner_pinned_to_top_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX stories_owner_pinned_to_top_idx ON public.stories USING btree (owner_peer_type, owner_peer_id, pinned_to_top_order, story_id DESC) WHERE ((deleted = false) AND (pinned = true) AND (pinned_to_top_order > 0));


--
-- Name: stories_random_id_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX stories_random_id_unique ON public.stories USING btree (owner_peer_type, owner_peer_id, random_id) WHERE (random_id <> 0);


--
-- Name: story_read_states_viewer_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX story_read_states_viewer_idx ON public.story_read_states USING btree (viewer_user_id, owner_peer_type, owner_peer_id);


--
-- Name: story_views_story_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX story_views_story_idx ON public.story_views USING btree (owner_peer_type, owner_peer_id, story_id, date DESC);


--
-- Name: story_views_viewer_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX story_views_viewer_idx ON public.story_views USING btree (viewer_user_id, owner_peer_type, owner_peer_id, story_id);


--
-- Name: update_states_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX update_states_user_id_idx ON public.update_states USING btree (user_id);


--
-- Name: upload_parts_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX upload_parts_created_at_idx ON public.upload_parts USING btree (created_at);


--
-- Name: upload_parts_object_key_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX upload_parts_object_key_idx ON public.upload_parts USING btree (object_key);


--
-- Name: uq_emq_dedup; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_emq_dedup ON public.encrypted_message_queue USING btree (receiver_auth_key_id, chat_id, random_id);


--
-- Name: uq_secret_chats_admin_random; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_secret_chats_admin_random ON public.secret_chats USING btree (admin_auth_key_id, random_id) WHERE (state <> 'discarded'::text);


--
-- Name: user_channel_member_index_admined_public_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_channel_member_index_admined_public_idx ON public.user_channel_member_index USING btree (user_id, channel_id DESC) WHERE (((status)::text = 'active'::text) AND ((role)::text = ANY (ARRAY[('creator'::character varying)::text, ('admin'::character varying)::text])) AND public_username AND (NOT deleted));


--
-- Name: user_channel_member_index_common_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_channel_member_index_common_idx ON public.user_channel_member_index USING btree (user_id, channel_id) WHERE (((status)::text = 'active'::text) AND megagroup AND (NOT broadcast) AND (NOT deleted));


--
-- Name: user_channel_member_index_discussion_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_channel_member_index_discussion_idx ON public.user_channel_member_index USING btree (user_id, channel_id DESC) WHERE (((status)::text = 'active'::text) AND megagroup AND (NOT broadcast) AND (NOT forum) AND (NOT deleted) AND (((role)::text = 'creator'::text) OR can_pin_messages));


--
-- Name: user_channel_member_index_left_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_channel_member_index_left_idx ON public.user_channel_member_index USING btree (user_id, left_at DESC, channel_id DESC) WHERE (((status)::text = 'left'::text) AND (NOT deleted) AND (megagroup OR broadcast));


--
-- Name: user_recent_reactions_user_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_recent_reactions_user_date_idx ON public.user_recent_reactions USING btree (user_id, reaction_date DESC, updated_at DESC, reaction_value);


--
-- Name: user_saved_reaction_tags_user_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_saved_reaction_tags_user_order_idx ON public.user_saved_reaction_tags USING btree (user_id, reaction_count DESC, updated_at DESC, reaction_value);


--
-- Name: user_top_reactions_user_rank_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_top_reactions_user_rank_idx ON public.user_top_reactions USING btree (user_id, reaction_count DESC, reaction_date DESC, updated_at DESC, reaction_value);


--
-- Name: user_update_events_read_outbox_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_update_events_read_outbox_idx ON public.user_update_events USING btree (user_id, peer_type, peer_id, max_id, date) WHERE ((event_type)::text = 'read_history_outbox'::text);


--
-- Name: user_update_events_retention_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_update_events_retention_idx ON public.user_update_events USING btree (user_id, date, pts);


--
-- Name: users_last_seen_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX users_last_seen_idx ON public.users USING btree (last_seen_at DESC) WHERE (last_seen_at > 0);


--
-- Name: users_name_lower_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX users_name_lower_trgm_idx ON public.users USING gin (lower(TRIM(BOTH FROM (((first_name)::text || ' '::text) || (last_name)::text))) public.gin_trgm_ops);


--
-- Name: users_phone_prefix_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX users_phone_prefix_idx ON public.users USING btree (phone text_pattern_ops);


--
-- Name: users_phone_unique_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX users_phone_unique_idx ON public.users USING btree (phone) WHERE ((phone)::text <> ''::text);


--
-- Name: users_premium_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX users_premium_expires_at_idx ON public.users USING btree (premium_expires_at) WHERE (premium_expires_at IS NOT NULL);


--
-- Name: users_username_lower_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX users_username_lower_trgm_idx ON public.users USING gin (lower((username)::text) public.gin_trgm_ops);


--
-- Name: users_username_lower_unique_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX users_username_lower_unique_idx ON public.users USING btree (lower((username)::text)) WHERE ((username)::text <> ''::text);


--
-- Name: account_privacy_rules account_privacy_rules_channel_participants_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER account_privacy_rules_channel_participants_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.account_privacy_rules FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_privacy_channel_participants_read_model();


--
-- Name: account_privacy_rules account_privacy_rules_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER account_privacy_rules_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.account_privacy_rules FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_privacy_read_model();


--
-- Name: bots bots_channel_participants_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER bots_channel_participants_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.bots FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_bot_channel_participants_read_model();


--
-- Name: channel_boost_slots channel_boost_slots_self_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER channel_boost_slots_self_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.channel_boost_slots FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_channel_self_boosts_read_model();


--
-- Name: channel_dialogs channel_dialogs_dialog_light_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER channel_dialogs_dialog_light_changed AFTER INSERT OR DELETE OR UPDATE ON public.channel_dialogs FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_channel_dialog_light_read_model();


--
-- Name: channel_members channel_members_participants_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER channel_members_participants_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.channel_members FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_channel_participants_read_model();


--
-- Name: channel_members channel_members_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER channel_members_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.channel_members FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_channel_member_read_model();


--
-- Name: channel_message_media channel_message_media_category_counts_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER channel_message_media_category_counts_changed AFTER INSERT OR DELETE OR UPDATE OF channel_id, id, category ON public.channel_message_media FOR EACH ROW EXECUTE FUNCTION public.telesrv_maintain_channel_media_category_counts();


--
-- Name: channel_message_media channel_message_media_count_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER channel_message_media_count_changed AFTER INSERT OR DELETE OR UPDATE ON public.channel_message_media FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_channel_media_count_read_model();


--
-- Name: channel_messages channel_messages_media_category_count_visibility_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER channel_messages_media_category_count_visibility_changed AFTER UPDATE OF deleted ON public.channel_messages FOR EACH ROW WHEN ((old.deleted IS DISTINCT FROM new.deleted)) EXECUTE FUNCTION public.telesrv_maintain_channel_media_visibility_counts();


--
-- Name: channel_messages channel_messages_media_count_visibility_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER channel_messages_media_count_visibility_changed AFTER UPDATE OF deleted ON public.channel_messages FOR EACH ROW WHEN ((old.deleted IS DISTINCT FROM new.deleted)) EXECUTE FUNCTION public.telesrv_notify_channel_media_count_visibility_read_model();


--
-- Name: channels channels_notify_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER channels_notify_changed AFTER INSERT OR DELETE OR UPDATE ON public.channels FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_channel_changed();


--
-- Name: channels channels_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER channels_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.channels FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_channel_base_read_model();


--
-- Name: contact_blocks contact_blocks_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER contact_blocks_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.contact_blocks FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_contact_block_read_model();


--
-- Name: contacts contacts_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER contacts_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.contacts FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_contact_read_model();


--
-- Name: dialog_drafts dialog_drafts_dialog_light_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER dialog_drafts_dialog_light_changed AFTER INSERT OR DELETE OR UPDATE ON public.dialog_drafts FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_dialog_light_read_model();


--
-- Name: dialogs dialogs_dialog_light_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER dialogs_dialog_light_changed AFTER INSERT OR DELETE OR UPDATE ON public.dialogs FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_dialog_light_read_model();


--
-- Name: dialogs dialogs_private_media_count_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER dialogs_private_media_count_read_model_changed AFTER INSERT OR DELETE OR UPDATE OF user_id, peer_type, peer_id ON public.dialogs FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_private_media_count_dialog_read_model();


--
-- Name: message_box_media message_box_media_category_counts_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER message_box_media_category_counts_changed AFTER INSERT OR DELETE OR UPDATE OF owner_user_id, box_id, peer_id, category ON public.message_box_media FOR EACH ROW EXECUTE FUNCTION public.telesrv_maintain_private_media_category_counts();


--
-- Name: message_box_media message_box_media_count_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER message_box_media_count_changed AFTER INSERT OR DELETE OR UPDATE OF owner_user_id, peer_id, category ON public.message_box_media FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_private_media_count_read_model();


--
-- Name: message_boxes message_boxes_media_category_count_visibility_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER message_boxes_media_category_count_visibility_changed AFTER UPDATE OF deleted ON public.message_boxes FOR EACH ROW WHEN ((old.deleted IS DISTINCT FROM new.deleted)) EXECUTE FUNCTION public.telesrv_maintain_private_media_visibility_counts();


--
-- Name: message_boxes message_boxes_media_count_visibility_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER message_boxes_media_count_visibility_changed AFTER UPDATE OF deleted ON public.message_boxes FOR EACH ROW WHEN ((old.deleted IS DISTINCT FROM new.deleted)) EXECUTE FUNCTION public.telesrv_notify_private_media_count_visibility_read_model();


--
-- Name: profile_photos profile_photos_channel_participants_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER profile_photos_channel_participants_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.profile_photos FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_profile_photo_channel_participants_read_model();


--
-- Name: profile_photos profile_photos_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER profile_photos_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.profile_photos FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_profile_photo_read_model();


--
-- Name: stories telesrv_stories_story_peer_read_model; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER telesrv_stories_story_peer_read_model AFTER INSERT OR DELETE OR UPDATE ON public.stories FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_story_peer_read_model();


--
-- Name: story_hidden_peers telesrv_story_hidden_peers_story_peer_read_model; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER telesrv_story_hidden_peers_story_peer_read_model AFTER INSERT OR DELETE OR UPDATE ON public.story_hidden_peers FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_story_peer_read_model();


--
-- Name: user_channel_member_index user_channel_member_index_active_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER user_channel_member_index_active_read_model_changed AFTER INSERT OR DELETE OR UPDATE OF user_id, channel_id, status, deleted ON public.user_channel_member_index FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_channel_active_memberships_read_model();


--
-- Name: users users_channel_participants_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER users_channel_participants_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.users FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_user_channel_participants_read_model();


--
-- Name: users users_read_model_changed; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER users_read_model_changed AFTER INSERT OR DELETE OR UPDATE ON public.users FOR EACH ROW EXECUTE FUNCTION public.telesrv_notify_user_base_read_model();


--
-- Name: account_passwords account_passwords_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_passwords
    ADD CONSTRAINT account_passwords_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: account_privacy_rules account_privacy_rules_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_privacy_rules
    ADD CONSTRAINT account_privacy_rules_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: account_reaction_settings account_reaction_settings_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_reaction_settings
    ADD CONSTRAINT account_reaction_settings_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: authorizations authorizations_auth_key_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.authorizations
    ADD CONSTRAINT authorizations_auth_key_id_fkey FOREIGN KEY (auth_key_id) REFERENCES public.auth_keys(auth_key_id) ON DELETE CASCADE;


--
-- Name: authorizations authorizations_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.authorizations
    ADD CONSTRAINT authorizations_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: bot_user_permissions bot_user_permissions_bot_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_user_permissions
    ADD CONSTRAINT bot_user_permissions_bot_user_id_fkey FOREIGN KEY (bot_user_id) REFERENCES public.bots(bot_user_id) ON DELETE CASCADE;


--
-- Name: bot_user_permissions bot_user_permissions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bot_user_permissions
    ADD CONSTRAINT bot_user_permissions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: bots bots_bot_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_bot_user_id_fkey FOREIGN KEY (bot_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: bots bots_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bots
    ADD CONSTRAINT bots_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES public.users(id);


--
-- Name: business_automation_deliveries business_automation_deliveries_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_automation_deliveries
    ADD CONSTRAINT business_automation_deliveries_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: business_automation_deliveries business_automation_deliveries_peer_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_automation_deliveries
    ADD CONSTRAINT business_automation_deliveries_peer_user_id_fkey FOREIGN KEY (peer_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: business_chat_links business_chat_links_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_chat_links
    ADD CONSTRAINT business_chat_links_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: business_connected_bot_peer_states business_connected_bot_peer_states_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_connected_bot_peer_states
    ADD CONSTRAINT business_connected_bot_peer_states_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: business_connected_bot_peer_states business_connected_bot_peer_states_peer_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_connected_bot_peer_states
    ADD CONSTRAINT business_connected_bot_peer_states_peer_user_id_fkey FOREIGN KEY (peer_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: business_connected_bots business_connected_bots_bot_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_connected_bots
    ADD CONSTRAINT business_connected_bots_bot_user_id_fkey FOREIGN KEY (bot_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: business_connected_bots business_connected_bots_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_connected_bots
    ADD CONSTRAINT business_connected_bots_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: channel_admin_log_events channel_admin_log_events_actor_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_admin_log_events
    ADD CONSTRAINT channel_admin_log_events_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES public.users(id) ON DELETE RESTRICT;


--
-- Name: channel_admin_log_events channel_admin_log_events_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_admin_log_events
    ADD CONSTRAINT channel_admin_log_events_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: channel_boost_slots channel_boost_slots_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_boost_slots
    ADD CONSTRAINT channel_boost_slots_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: channel_dialogs channel_dialogs_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_dialogs
    ADD CONSTRAINT channel_dialogs_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: channel_dialogs channel_dialogs_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_dialogs
    ADD CONSTRAINT channel_dialogs_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: channel_forum_topics channel_forum_topics_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_forum_topics
    ADD CONSTRAINT channel_forum_topics_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: channel_forum_topics channel_forum_topics_creator_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_forum_topics
    ADD CONSTRAINT channel_forum_topics_creator_user_id_fkey FOREIGN KEY (creator_user_id) REFERENCES public.users(id) ON DELETE RESTRICT;


--
-- Name: channel_invite_importers channel_invite_importers_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_invite_importers
    ADD CONSTRAINT channel_invite_importers_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: channel_invite_importers channel_invite_importers_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_invite_importers
    ADD CONSTRAINT channel_invite_importers_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: channel_invites channel_invites_admin_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_invites
    ADD CONSTRAINT channel_invites_admin_user_id_fkey FOREIGN KEY (admin_user_id) REFERENCES public.users(id) ON DELETE RESTRICT;


--
-- Name: channel_invites channel_invites_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_invites
    ADD CONSTRAINT channel_invites_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: channel_media_category_counts channel_media_category_counts_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_media_category_counts
    ADD CONSTRAINT channel_media_category_counts_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: channel_members channel_members_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_members
    ADD CONSTRAINT channel_members_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: channel_members channel_members_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_members
    ADD CONSTRAINT channel_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: channel_message_media channel_message_media_channel_id_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_message_media
    ADD CONSTRAINT channel_message_media_channel_id_id_fkey FOREIGN KEY (channel_id, id) REFERENCES public.channel_messages(channel_id, id) ON DELETE CASCADE;


--
-- Name: channel_message_reactions channel_message_reactions_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_message_reactions
    ADD CONSTRAINT channel_message_reactions_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: channel_message_reactions channel_message_reactions_channel_id_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_message_reactions
    ADD CONSTRAINT channel_message_reactions_channel_id_message_id_fkey FOREIGN KEY (channel_id, message_id) REFERENCES public.channel_messages(channel_id, id) ON DELETE CASCADE;


--
-- Name: channel_message_reactions channel_message_reactions_reacted_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_message_reactions
    ADD CONSTRAINT channel_message_reactions_reacted_user_id_fkey FOREIGN KEY (reacted_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: channel_message_reactions channel_message_reactions_sender_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_message_reactions
    ADD CONSTRAINT channel_message_reactions_sender_user_id_fkey FOREIGN KEY (sender_user_id) REFERENCES public.users(id) ON DELETE RESTRICT;


--
-- Name: channel_messages channel_messages_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_messages
    ADD CONSTRAINT channel_messages_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: channel_messages channel_messages_sender_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_messages
    ADD CONSTRAINT channel_messages_sender_user_id_fkey FOREIGN KEY (sender_user_id) REFERENCES public.users(id) ON DELETE RESTRICT;


--
-- Name: channel_unread_mention_index channel_unread_mention_index_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_unread_mention_index
    ADD CONSTRAINT channel_unread_mention_index_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: channel_unread_mention_index channel_unread_mention_index_channel_id_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_unread_mention_index
    ADD CONSTRAINT channel_unread_mention_index_channel_id_message_id_fkey FOREIGN KEY (channel_id, message_id) REFERENCES public.channel_messages(channel_id, id) ON DELETE CASCADE;


--
-- Name: channel_unread_mention_index channel_unread_mention_index_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_unread_mention_index
    ADD CONSTRAINT channel_unread_mention_index_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: channel_unread_mentions channel_unread_mentions_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_unread_mentions
    ADD CONSTRAINT channel_unread_mentions_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: channel_unread_mentions channel_unread_mentions_channel_id_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_unread_mentions
    ADD CONSTRAINT channel_unread_mentions_channel_id_message_id_fkey FOREIGN KEY (channel_id, message_id) REFERENCES public.channel_messages(channel_id, id) ON DELETE CASCADE;


--
-- Name: channel_unread_mentions channel_unread_mentions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_unread_mentions
    ADD CONSTRAINT channel_unread_mentions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: channel_update_events channel_update_events_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_update_events
    ADD CONSTRAINT channel_update_events_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: channels channels_creator_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_creator_user_id_fkey FOREIGN KEY (creator_user_id) REFERENCES public.users(id) ON DELETE RESTRICT;


--
-- Name: contact_blocks contact_blocks_blocked_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contact_blocks
    ADD CONSTRAINT contact_blocks_blocked_user_id_fkey FOREIGN KEY (blocked_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: contact_blocks contact_blocks_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contact_blocks
    ADD CONSTRAINT contact_blocks_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: contacts contacts_contact_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts
    ADD CONSTRAINT contacts_contact_user_id_fkey FOREIGN KEY (contact_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: contacts contacts_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts
    ADD CONSTRAINT contacts_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: country_codes country_codes_iso2_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.country_codes
    ADD CONSTRAINT country_codes_iso2_fkey FOREIGN KEY (iso2) REFERENCES public.countries(iso2) ON DELETE CASCADE;


--
-- Name: dialog_drafts dialog_drafts_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dialog_drafts
    ADD CONSTRAINT dialog_drafts_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: dialog_filter_settings dialog_filter_settings_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dialog_filter_settings
    ADD CONSTRAINT dialog_filter_settings_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: dialog_filters dialog_filters_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dialog_filters
    ADD CONSTRAINT dialog_filters_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: dialogs dialogs_user_id_fkey1; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dialogs
    ADD CONSTRAINT dialogs_user_id_fkey1 FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: dispatch_outbox dispatch_outbox_target_user_id_fkey1; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dispatch_outbox
    ADD CONSTRAINT dispatch_outbox_target_user_id_fkey1 FOREIGN KEY (target_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: dispatch_outbox dispatch_outbox_target_user_id_pts_fkey1; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dispatch_outbox
    ADD CONSTRAINT dispatch_outbox_target_user_id_pts_fkey1 FOREIGN KEY (target_user_id, pts) REFERENCES public.user_update_events(user_id, pts) ON DELETE CASCADE;


--
-- Name: encrypted_state_event_delivery encrypted_state_event_delivery_event_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.encrypted_state_event_delivery
    ADD CONSTRAINT encrypted_state_event_delivery_event_id_fkey FOREIGN KEY (event_id) REFERENCES public.encrypted_state_events(id) ON DELETE CASCADE;


--
-- Name: group_call_participant_overrides group_call_participant_overrides_call_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_call_participant_overrides
    ADD CONSTRAINT group_call_participant_overrides_call_id_fkey FOREIGN KEY (call_id) REFERENCES public.group_calls(call_id) ON DELETE CASCADE;


--
-- Name: group_call_participants group_call_participants_call_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.group_call_participants
    ADD CONSTRAINT group_call_participants_call_id_fkey FOREIGN KEY (call_id) REFERENCES public.group_calls(call_id) ON DELETE CASCADE;


--
-- Name: lang_pack_strings lang_pack_strings_lang_pack_lang_code_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lang_pack_strings
    ADD CONSTRAINT lang_pack_strings_lang_pack_lang_code_fkey FOREIGN KEY (lang_pack, lang_code) REFERENCES public.lang_packs(lang_pack, lang_code) ON DELETE CASCADE;


--
-- Name: message_box_media message_box_media_owner_user_id_box_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_box_media
    ADD CONSTRAINT message_box_media_owner_user_id_box_id_fkey FOREIGN KEY (owner_user_id, box_id) REFERENCES public.message_boxes(owner_user_id, box_id) ON DELETE CASCADE;


--
-- Name: message_boxes message_boxes_from_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_boxes
    ADD CONSTRAINT message_boxes_from_user_id_fkey FOREIGN KEY (from_user_id) REFERENCES public.users(id) ON DELETE RESTRICT;


--
-- Name: message_boxes message_boxes_message_sender_id_private_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_boxes
    ADD CONSTRAINT message_boxes_message_sender_id_private_message_id_fkey FOREIGN KEY (message_sender_id, private_message_id) REFERENCES public.private_messages(sender_user_id, id) ON DELETE CASCADE;


--
-- Name: message_boxes message_boxes_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.message_boxes
    ADD CONSTRAINT message_boxes_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: poll_votes poll_votes_poll_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.poll_votes
    ADD CONSTRAINT poll_votes_poll_id_fkey FOREIGN KEY (poll_id) REFERENCES public.polls(poll_id) ON DELETE CASCADE;


--
-- Name: private_message_reactions private_message_reactions_message_sender_id_private_messag_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.private_message_reactions
    ADD CONSTRAINT private_message_reactions_message_sender_id_private_messag_fkey FOREIGN KEY (message_sender_id, private_message_id) REFERENCES public.private_messages(sender_user_id, id) ON DELETE CASCADE;


--
-- Name: private_message_reactions private_message_reactions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.private_message_reactions
    ADD CONSTRAINT private_message_reactions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: private_messages private_messages_recipient_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.private_messages
    ADD CONSTRAINT private_messages_recipient_user_id_fkey FOREIGN KEY (recipient_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: private_messages private_messages_sender_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.private_messages
    ADD CONSTRAINT private_messages_sender_user_id_fkey FOREIGN KEY (sender_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: quick_replies quick_replies_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.quick_replies
    ADD CONSTRAINT quick_replies_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: quick_reply_messages quick_reply_messages_owner_user_id_shortcut_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.quick_reply_messages
    ADD CONSTRAINT quick_reply_messages_owner_user_id_shortcut_id_fkey FOREIGN KEY (owner_user_id, shortcut_id) REFERENCES public.quick_replies(owner_user_id, shortcut_id) ON DELETE CASCADE;


--
-- Name: saved_music saved_music_document_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saved_music
    ADD CONSTRAINT saved_music_document_id_fkey FOREIGN KEY (document_id) REFERENCES public.documents(id) ON DELETE CASCADE;


--
-- Name: saved_music saved_music_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saved_music
    ADD CONSTRAINT saved_music_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: scheduled_messages scheduled_messages_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scheduled_messages
    ADD CONSTRAINT scheduled_messages_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: temp_auth_key_bindings temp_auth_key_bindings_temp_auth_key_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.temp_auth_key_bindings
    ADD CONSTRAINT temp_auth_key_bindings_temp_auth_key_id_fkey FOREIGN KEY (temp_auth_key_id) REFERENCES public.auth_keys(auth_key_id) ON DELETE CASCADE;


--
-- Name: user_business_profiles user_business_profiles_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_business_profiles
    ADD CONSTRAINT user_business_profiles_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_channel_member_index user_channel_member_index_channel_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_member_index
    ADD CONSTRAINT user_channel_member_index_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;


--
-- Name: user_channel_member_index user_channel_member_index_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_channel_member_index
    ADD CONSTRAINT user_channel_member_index_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_recent_reactions user_recent_reactions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_recent_reactions
    ADD CONSTRAINT user_recent_reactions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_saved_reaction_tags user_saved_reaction_tags_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_saved_reaction_tags
    ADD CONSTRAINT user_saved_reaction_tags_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_top_reactions user_top_reactions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_top_reactions
    ADD CONSTRAINT user_top_reactions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_update_events user_update_events_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_update_events
    ADD CONSTRAINT user_update_events_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_update_events user_update_events_user_id_message_box_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_update_events
    ADD CONSTRAINT user_update_events_user_id_message_box_id_fkey FOREIGN KEY (user_id, message_box_id) REFERENCES public.message_boxes(owner_user_id, box_id) ON DELETE CASCADE;


--
-- Name: user_update_watermarks user_update_watermarks_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_update_watermarks
    ADD CONSTRAINT user_update_watermarks_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- PostgreSQL database dump complete
--



-- ===================== 规范 seed 数据 =====================
-- 关触发器+FK 精确装载 seed（等价 pg_restore），避免 seed 其它表触发 read-model 触发器二次生成行而撞键。
-- session_replication_role 需超级权限，与本迁移已有的 CREATE EXTENSION pg_trgm 同一前提。
SET session_replication_role = replica;

--
-- PostgreSQL database dump
--


-- Dumped from database version 17.10
-- Dumped by pg_dump version 17.10

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Data for Name: users; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.users (id, access_hash, phone, first_name, last_name, username, country_code, created_at, updated_at, verified, support, about, last_seen_at, default_history_ttl_period, is_bot, bot_info_version, premium_expires_at, emoji_status_document_id, emoji_status_until, color_set, color, color_background_emoji_id, profile_color_set, profile_color, profile_color_background_emoji_id) VALUES (777000, 6599886787491911851, '42777', 'Telesrv', '', 'telesrv', '', '2026-06-19 13:35:51.253491+00', '2026-06-19 13:35:51.253491+00', true, true, '', 0, 0, false, 0, NULL, 0, 0, false, 0, 0, false, 0, 0);
INSERT INTO public.users (id, access_hash, phone, first_name, last_name, username, country_code, created_at, updated_at, verified, support, about, last_seen_at, default_history_ttl_period, is_bot, bot_info_version, premium_expires_at, emoji_status_document_id, emoji_status_until, color_set, color, color_background_emoji_id, profile_color_set, profile_color, profile_color_background_emoji_id) VALUES (93372553, 7421896403922962293, '', 'BotFather', '', 'BotFather', '', '2026-06-19 13:35:52.688367+00', '2026-06-19 13:35:52.688367+00', true, false, '', 0, 0, true, 1, NULL, 0, 0, false, 0, 0, false, 0, 0);


--
-- Data for Name: account_passwords; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: account_privacy_rules; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: account_reaction_settings; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: app_configs; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.app_configs (client, hash, config_json, updated_at) VALUES ('tdesktop', 5, '{"quote_length_max": 1024, "reactions_default": {"_": "reactionEmoji", "emoticon": "👍"}, "reactions_uniq_max": 11, "reactions_in_chat_max": 3, "telegram_antispam_user_id": "5434988373", "pm_read_date_expire_period": 604800, "reactions_user_max_default": 1, "chat_read_mark_expire_period": 604800, "chat_read_mark_size_threshold": 50, "telegram_antispam_group_size_min": 200}', '2026-06-19 13:35:52.201256+00');


--
-- Data for Name: auth_keys; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: authorizations; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: available_reactions; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: bot_chat_states; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: bots; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.bots (bot_user_id, owner_user_id, token_secret, description, commands, bot_chat_history, bot_nochats, inline_placeholder, created_at, updated_at, menu_button_type, menu_button_text, menu_button_url, bot_inline_geo) VALUES (93372553, 93372553, '', 'BotFather is the one bot to rule them all. Use it to create new bot accounts and manage your existing bots.', '[{"command": "newbot", "description": "create a new bot"}, {"command": "mybots", "description": "list your bots"}, {"command": "token", "description": "show a bot''s token"}, {"command": "revoke", "description": "revoke a bot''s token"}, {"command": "cancel", "description": "cancel the current operation"}, {"command": "help", "description": "show help"}]', false, false, '', '2026-06-19 13:35:52.688367+00', '2026-06-19 13:35:52.688367+00', 0, '', '', false);


--
-- Data for Name: bot_user_permissions; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: business_automation_deliveries; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: business_chat_links; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: business_connected_bot_peer_states; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: business_connected_bots; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channels; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_admin_log_events; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_boost_slots; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_dialogs; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_forum_topics; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_invite_hashes; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_invite_importers; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_invites; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_media_category_counts; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_members; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_messages; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_message_media; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_message_reactions; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_message_viewers; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_unread_mention_index; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_unread_mentions; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_update_events; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: channel_usernames; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: contact_blocks; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: contacts; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: countries; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.countries (iso2, default_name, name, hidden, order_index, updated_at) VALUES ('US', 'United States', '', false, 10, '2026-06-19 13:35:51.214267+00');
INSERT INTO public.countries (iso2, default_name, name, hidden, order_index, updated_at) VALUES ('CN', 'China', '', false, 20, '2026-06-19 13:35:51.214267+00');


--
-- Data for Name: country_codes; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.country_codes (id, iso2, country_code, prefixes, patterns, order_index) VALUES (1, 'US', '1', '{""}', '{"XXX XXX XXXX"}', 10);
INSERT INTO public.country_codes (id, iso2, country_code, prefixes, patterns, order_index) VALUES (2, 'CN', '86', '{""}', '{"XXX XXXX XXXX"}', 20);


--
-- Data for Name: dialog_drafts; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: dialog_filter_settings; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: dialog_filters; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: dialogs; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: private_messages; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: message_boxes; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: user_update_events; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: dispatch_outbox; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: documents; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: encrypted_files; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: encrypted_message_queue; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: encrypted_state_events; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: encrypted_state_event_delivery; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: file_blobs; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: group_calls; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: group_call_participant_overrides; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: group_call_participants; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: lang_packs; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: lang_pack_strings; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: message_box_media; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: photos; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: polls; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: poll_votes; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: private_media_category_counts; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: private_message_reactions; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: profile_photos; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: quick_replies; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: quick_reply_messages; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: read_model_versions; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.read_model_versions (model, owner_user_id, peer_type, peer_id, version, updated_at, hash) VALUES ('contact_account', 777000, 'user', 777000, 1, '2026-06-19 13:35:53.204092+00', 8080089745885824);
INSERT INTO public.read_model_versions (model, owner_user_id, peer_type, peer_id, version, updated_at, hash) VALUES ('contact_account', 93372553, 'user', 93372553, 1, '2026-06-19 13:35:53.204092+00', 3925565686878508);
INSERT INTO public.read_model_versions (model, owner_user_id, peer_type, peer_id, version, updated_at, hash) VALUES ('channel_active_memberships', 777000, 'user', 777000, 1, '2026-06-19 13:35:53.311258+00', 2767535233995134);
INSERT INTO public.read_model_versions (model, owner_user_id, peer_type, peer_id, version, updated_at, hash) VALUES ('channel_active_memberships', 93372553, 'user', 93372553, 1, '2026-06-19 13:35:53.311258+00', 1406287033083140);


--
-- Data for Name: saved_dialog_pins; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: saved_music; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: scheduled_messages; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: secret_chats; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: secret_qts_watermarks; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: sticker_sets; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: stories; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: story_hidden_peers; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: story_read_states; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: story_views; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: temp_auth_key_bindings; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: update_states; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: upload_parts; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: user_business_profiles; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: user_channel_member_index; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: user_recent_reactions; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: user_saved_reaction_tags; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: user_top_reactions; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: user_update_watermarks; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Name: country_codes_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.country_codes_id_seq', 2, true);


--
-- Name: dispatch_outbox_v2_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.dispatch_outbox_v2_id_seq', 1, false);


--
-- Name: encrypted_state_events_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.encrypted_state_events_id_seq', 1, false);


--
-- Name: private_messages_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.private_messages_id_seq', 1, false);


--
-- Name: users_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.users_id_seq', 1780243199, true);


--
-- PostgreSQL database dump complete
--



SET session_replication_role = DEFAULT;
