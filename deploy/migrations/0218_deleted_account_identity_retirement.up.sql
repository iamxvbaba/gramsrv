-- Удалённый аккаунт больше не отдаёт свои идентичности.
--
-- 0217/ExecuteAccountDeletion раньше просто удалял editable-строку из
-- peer_usernames, из-за чего обычный username удалённого аккаунта освобождался
-- для всех, а collectible (NFT) username оставался закреплён за призраком.
-- Теперь удаление гасит editable-слот (строка остаётся, но active=false: имя
-- занято навсегда и никому не резолвится) и возвращает NFT-юзернеймы в хранилище
-- (status='vault' — актив остаётся, его можно перевыпустить).
--
-- Миграция догоняет аккаунты, удалённые до этого изменения: vault для
-- collectible-активов с записью 'revoke' в журнале происхождения, погашение
-- registry-строк и деактивация профильных фото. Ровно тот же набор операций, что
-- теперь делает ExecuteAccountDeletion в одной транзакции.

WITH vaulted AS (
    UPDATE public.collectible_usernames asset
    SET status = 'vault',
        owner_peer_type = '',
        owner_peer_id = 0,
        version = asset.version + 1,
        updated_at = now()
    WHERE asset.status = 'owned'
      AND asset.owner_peer_type = 'user'
      AND EXISTS (
          SELECT 1
          FROM public.users owner
          WHERE owner.id = asset.owner_peer_id
            AND owner.deleted_at IS NOT NULL
      )
    RETURNING asset.id, asset.owner_peer_id
)
INSERT INTO public.collectible_username_transfers (
    collectible_id, kind, from_peer_type, from_peer_id,
    to_peer_type, to_peer_id, currency, amount, actor, reason,
    command_key, created_at
)
SELECT vaulted.id,
       'revoke',
       'user',
       vaulted.owner_peer_id,
       '',
       0,
       '',
       0,
       'telesrv:account-deletion',
       'account deletion',
       NULL,
       now()
FROM vaulted;

-- Registry-строки collectible-юзернеймов снимаются: имя перестаёт резолвиться в
-- удалённый аккаунт (в том числе collectible-резолв, который требует active).
DELETE FROM public.peer_usernames
WHERE peer_type = 'user'
  AND collectible_id IS NOT NULL
  AND EXISTS (
      SELECT 1
      FROM public.users owner
      WHERE owner.id = peer_usernames.peer_id
        AND owner.deleted_at IS NOT NULL
  );

-- Оставшиеся слоты (уже погашенные, но всё ещё активные — например если слот был
-- перезаписан до удаления) окончательно гасятся: имя занято навсегда.
UPDATE public.peer_usernames
SET active = false,
    updated_at = now()
WHERE peer_type = 'user'
  AND active
  AND EXISTS (
      SELECT 1
      FROM public.users owner
      WHERE owner.id = peer_usernames.peer_id
        AND owner.deleted_at IS NOT NULL
  );

-- Аватар удалённого аккаунта не должен проецироваться ни в одном виде; строки
-- сохраняются (blob'ы остаются в файловом хранилище), но перестают быть активными.
UPDATE public.profile_photos
SET active = false
WHERE owner_peer_type = 'user'
  AND active
  AND EXISTS (
      SELECT 1
      FROM public.users owner
      WHERE owner.id = profile_photos.owner_peer_id
        AND owner.deleted_at IS NOT NULL
  );