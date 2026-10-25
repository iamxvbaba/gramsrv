-- 0218 необратима: активы ушли в vault с записью в журнале происхождения, а
-- погашенные username-строки и профильные фото потеряли исходные значения флагов
-- и sort_order-проекции. Восстановление означало бы воскресить либо актив, либо
-- «занятое» имя у удалённого аккаунта, что прямо противоречит цели миграции.
-- Как и 0217, откат отклоняется, а не чинит данные выдуманными значениями.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.users
        WHERE deleted_at IS NOT NULL
    ) AND EXISTS (
        SELECT 1
        FROM public.collectible_username_transfers
        WHERE actor = 'telesrv:account-deletion'
    ) THEN
        RAISE EXCEPTION 'cannot roll back 0218: deleted-account identities were retired and their prior state was not kept';
    END IF;
END
$$;