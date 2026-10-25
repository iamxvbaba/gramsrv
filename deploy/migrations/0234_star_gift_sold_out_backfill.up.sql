-- v2 编号 0234；移植自 main 0205_star_gift_sold_out_backfill（main ebf2b23d），见 docs/main-v2-sync.md。
-- Backfill the sold-out flag for limited gifts whose inventory was already
-- exhausted before the purchase/auction auto-flip landed. sold_out,
-- first_sale_date and last_sale_date share the TL flag, and the RPC layer only
-- exposes the sale timestamps behind sold_out, so an exhausted limited edition
-- with a stale revision rendered as sold out without its first/last sale dates.
-- The auto-flip has shipped in the store layers; this closes out any lingering
-- rows created before it went live.

UPDATE public.star_gift_catalog_revisions r
SET sold_out = true
FROM public.star_gift_catalog c
WHERE c.active_revision_id = r.id
  AND r.limited
  AND NOT r.sold_out
  AND c.availability_remains = 0;