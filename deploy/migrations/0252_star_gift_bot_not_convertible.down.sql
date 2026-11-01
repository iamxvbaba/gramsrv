-- The migration zeroes convert_stars on existing rows, and the original values have
-- no copy; a down-migration would either mark gifts not sent by a bot as
-- convertible out of thin air or fail to read the original data. Such an
-- irreversible rollback is refused.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.peer_star_gifts gift
        JOIN public.users sender ON sender.id = gift.from_user_id
        WHERE NOT gift.converted
          AND sender.is_bot
          AND gift.convert_stars = 0
    ) THEN
        RAISE EXCEPTION 'cannot roll back 0217: bot-sent gifts are already non-convertible and the original values were dropped';
    END IF;
END
$$;