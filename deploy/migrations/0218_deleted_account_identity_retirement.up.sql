-- A deleted account no longer gives out its identities.
--
-- 0217/ExecuteAccountDeletion previously simply deleted the editable row from
-- peer_usernames, which freed a deleted account's ordinary username for everyone
-- while a collectible (NFT) username stayed pinned to a ghost. Now deletion retires
-- the editable slot (the row remains but active=false: the name is taken forever
-- and resolves for nobody) and returns NFT usernames to the vault (status='vault'
-- -- the asset remains and can be reissued).
--
-- The migration catches up accounts deleted before this change: vaulting
-- collectible assets with a 'revoke' entry in the provenance journal, retiring
-- registry rows and deactivating profile photos. Exactly the same set of operations
-- that ExecuteAccountDeletion now performs in one transaction.

WITH previous AS MATERIALIZED (
 SELECT asset.id, asset.owner_peer_id FROM public.collectible_usernames asset
 JOIN public.users owner ON owner.id=asset.owner_peer_id
 WHERE asset.status='owned' AND asset.owner_peer_type='user' AND owner.deleted_at IS NOT NULL
 FOR UPDATE OF asset
), vaulted AS (
    UPDATE public.collectible_usernames asset
    SET status = 'vault',
        owner_peer_type = '',
        owner_peer_id = 0,
        version = asset.version + 1,
        updated_at = now()
    FROM previous
    WHERE asset.id=previous.id
    RETURNING asset.id,previous.owner_peer_id
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

-- Registry rows of collectible usernames are removed: the name no longer resolves
-- to a deleted account (including collectible resolution, which requires active).
DELETE FROM public.peer_usernames
WHERE peer_type = 'user'
  AND collectible_id IS NOT NULL
  AND EXISTS (
      SELECT 1
      FROM public.users owner
      WHERE owner.id = peer_usernames.peer_id
        AND owner.deleted_at IS NOT NULL
  );

-- Remaining slots (already retired but still active -- e.g. if the slot was
-- overwritten before deletion) are retired for good: the name is taken forever.
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

-- A deleted account's avatar must not be projected in any form; the rows are kept
-- (the blobs remain in file storage) but stop being active.
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