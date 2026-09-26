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

-- Тот же guard, что и в 0095, плюс запрет выставлять подарок замороженного
-- аккаунта. Перевод строки в suspended делает сама заморозка, поэтому он
-- разрешён всегда: проверка frozen касается только активных листингов.
CREATE OR REPLACE FUNCTION public.telesrv_guard_star_gift_listing() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    gift_owner_type text;
    gift_owner_id bigint;
    gift_burned boolean;
    seller_frozen boolean;
BEGIN
    SELECT owner_peer_type, owner_peer_id, burned
      INTO gift_owner_type, gift_owner_id, gift_burned
      FROM public.unique_star_gifts WHERE id=NEW.unique_gift_id FOR SHARE;
    IF gift_burned OR gift_owner_type IS DISTINCT FROM NEW.seller_peer_type OR gift_owner_id IS DISTINCT FROM NEW.seller_peer_id THEN
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
