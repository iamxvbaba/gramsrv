-- v2 编号 0246；移植自 main 0217_star_gift_listing_freeze_suspension（main 3a84827a，B7b–7e），见 docs/main-v2-sync.md。
-- Заморозка аккаунта снимает с продажи все его маркет-листинги.
--
-- Строка листинга не удаляется: цена, валюта и время выставления должны пережить
-- цикл заморозки, поэтому листинг помечается suspended, а все чтения маркета
-- (payments.getResaleStarGifts, UniqueStarGiftValueInfo, resell_amount в
-- uniqueStarGift, проекции каталога) и покупка его игнорируют. Разморозка
-- возвращает ровно те строки, которые были выставлены до заморозки, обратно в
-- продажу с той же ценой. Переключение выполняется в той же транзакции, что и
-- сама заморозка (SetAccountFreeze), поэтому окна «аккаунт frozen, листинг ещё
-- в продаже» не существует.
ALTER TABLE public.star_gift_listings
    ADD COLUMN suspended boolean NOT NULL DEFAULT false;

-- Частичный индекс обслуживает только переключение заморозки: замороженных
-- продавцов мало, а активных листингов много.
CREATE INDEX star_gift_listings_seller_suspension_idx
    ON public.star_gift_listings(seller_peer_id)
    WHERE suspended AND seller_peer_type='user';

-- v2 编号 0246；移植自 main 0217（main 3a84827a，B7b–7e）。函数体以 v2 0186 定义为底
-- （含 externalization_pending 守卫），再加上 main 的冻结卖家检查；
-- 转入 suspended 由冻结事务本身完成，因此始终允许，frozen 检查只针对活跃挂单。
CREATE OR REPLACE FUNCTION public.telesrv_guard_star_gift_listing() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    gift_owner_type text;
    gift_owner_id bigint;
    gift_burned boolean;
    gift_externalizing boolean;
    seller_frozen boolean;
BEGIN
    SELECT owner_peer_type, owner_peer_id, burned, externalization_pending
      INTO gift_owner_type, gift_owner_id, gift_burned, gift_externalizing
      FROM public.unique_star_gifts WHERE id=NEW.unique_gift_id FOR SHARE;
    IF gift_burned OR gift_externalizing OR
       gift_owner_type IS DISTINCT FROM NEW.seller_peer_type OR gift_owner_id IS DISTINCT FROM NEW.seller_peer_id THEN
        RAISE EXCEPTION 'star gift listing owner/state mismatch';
    END IF;
    IF NOT NEW.suspended AND NEW.seller_peer_type = 'user' THEN
        SELECT EXISTS(SELECT 1 FROM public.account_restrictions r WHERE r.user_id=NEW.seller_peer_id AND r.frozen)
          INTO seller_frozen;
        IF seller_frozen THEN
            RAISE EXCEPTION 'star gift listing seller frozen';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
