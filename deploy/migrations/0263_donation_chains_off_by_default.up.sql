-- v2 编号 0263；移植自 main 0239_donation_chains_off_by_default（main 10d011c1，B10），见 docs/main-v2-sync.md。
-- v2 的 0255 已把 Ganache 种成关闭；本迁移再关闭仍带占位端点的 Sepolia，并保留运维已配置的链。
-- Crypto donations: no chain is watchable out of the box.
--
-- The seed migration shipped ganache (http://127.0.0.1:7545) and sepolia
-- (empty rpc_url) with enabled = true. Neither can work on a real deployment:
-- there is no local Ganache node to dial, and Sepolia had no endpoint filled
-- in. main.go skips a chain whose rpc_url is empty (domain.DonationChain.
-- Watchable), so sepolia was merely cosmetic -- but ganache was dialable, and
-- every single boot of every server logged
--
--     donations.ganache  ERROR  donation watcher stopped  dial ganache:
--     connection refused
--
-- forever, for a feature nobody had turned on. Worse, cmd/telesrv provisions
-- the custodial wallet on first boot regardless of any chain setting, so every
-- deployment also silently created a real BIP39 wallet and a
-- data/donation_wallet.key and logged a warning about backing it up.
--
-- Enabling a chain is now two deliberate steps -- set TELESRV_DONATIONS_ENABLED
-- and flip the row -- rather than something a fresh database does behind your
-- back. An operator who HAS enabled ganache locally keeps it: only the rows
-- that still carry the seeded placeholder endpoint are cleared, so a
-- deployment that was already working is not switched off by this migration.
UPDATE public.donation_chains
   SET enabled = false
 WHERE enabled = true
   AND rpc_url IN ('', 'http://127.0.0.1:7545');