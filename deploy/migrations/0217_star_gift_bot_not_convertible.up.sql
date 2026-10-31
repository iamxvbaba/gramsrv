-- Gifts sent via Bot API sendGift are not convertible back to Stars: convertibility
-- is governed by peer_star_gifts.convert_stars, and the purchase transaction zeroes
-- it for bot buyers as of 0217. This migration applies that fix retroactively to bot
-- gifts already sent before the fix that have not yet been converted. Whether the
-- sender is a bot is determined by users.is_bot (from_user_id is always the real
-- sender; anonymity affects only visibility at delivery time, not this determination).
-- Already-converted gifts (converted=true) are excluded: their historical conversion
-- is settled and clawing it back would alter the ledger -- an irreversible data
-- change that is not done.
UPDATE public.peer_star_gifts gift
SET convert_stars = 0
WHERE NOT gift.converted
  AND gift.from_user_id > 0
  AND EXISTS (
      SELECT 1
      FROM public.users sender
      WHERE sender.id = gift.from_user_id
        AND sender.is_bot
  );