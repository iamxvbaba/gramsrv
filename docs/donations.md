# Crypto donations

Deposit crypto (native currency, or USDT/USDC) to a personal, permanent
address and have it automatically credited to your Stars balance once the
deposit reaches the chain's required confirmation depth. This is separate
from "support the project" (a single operator wallet shown on the site and
in the admin panel, not wired into any per-user crediting) -- this document
covers the per-user deposit-to-Stars pipeline only.

## Address model

Every EVM-compatible chain (Ethereum, Base, Polygon, BSC, Sepolia, Ganache,
...) shares the same secp256k1 curve and address derivation, so **one
address per user works, unmodified, on every enabled chain at once**. There
is never a per-chain address.

The server holds a single BIP39 mnemonic and derives each user's address at
`m/44'/60'/0'/0/{index}`, where `index` is a compact sequence value
(`donation_address_index_seq`), not the raw Telegram user ID -- user IDs
could someday exceed BIP32's unhardened `uint32` index range, and keeping
the two decoupled avoids ever having to think about that again.

## Custody

Full custodial: the server holds the mnemonic, encrypted at rest with
AES-256-GCM. **Works out of the box, no manual setup step:**

- The encryption key is a local file, `data/donation_wallet.key` by default
  (`TELESRV_DONATION_WALLET_KEY_PATH`), generated automatically the first
  time the server ever runs -- the exact same pattern
  `internal/mtprotoedge.LoadOrGenerateRSAKey` already uses for the server's
  MTProto RSA key. An operator who wants to supply their own key instead
  (e.g. from a secrets manager) can set `TELESRV_DONATION_WALLET_KEY` to a
  literal 64-hex-char value, which overrides the file entirely.
- The wallet mnemonic itself is generated automatically the first time
  `cmd/telesrv` starts against a database with no wallet row yet
  (`donationsService.EnsureWallet`, called right after construction iff
  `!Ready()`). The recovery phrase is never printed or logged -- it only
  ever exists encrypted in the database -- and every later restart loads
  the already-encrypted wallet instead.

Losing the key file is exactly like losing a hardware wallet's seed phrase
-- there is no recovery path, because the encrypted mnemonic in the
database cannot be decrypted without it. Back up `data/donation_wallet.key`
the same way you'd back up `data/server_rsa.pem`.

**Deriving addresses and reading balances never touches the private key.**
The only code that can produce a private key at all is
`Wallet.PrivateKeyHex`, and nothing in the automatic watcher/crediting path
calls it. Sweeping funds out of a user's deposit address into an operator
treasury is a deliberately separate, manual, operator-triggered action --
not implemented yet, and never automatic by design (see "Not yet built"
below).

## Chains

`donation_chains` is a normal table, seeded by migration
`20260924140000_donations` with six rows. Only **Ganache** (local dev,
`http://127.0.0.1:7545`) and **Sepolia** (testnet) ship `enabled = true`.
Ethereum mainnet, Base, Polygon and BNB Smart Chain ship as disabled
placeholder rows (empty `rpc_url`) -- an operator fills in the RPC endpoint
and flips `enabled` once ready, no migration or deploy needed.

`donation_tokens` holds the USDT/USDC contract per chain. Sepolia's USDC
row points at Circle's own canonical testnet deployment
(`0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238`, verified against Circle's own
docs). There is no citable official Sepolia USDT deployment, so that row
(and every mainnet/L2 token row, and both Ganache rows) ships with an empty
`contract_address` -- the watcher skips any token row shaped like that
rather than watching the zero address. Ganache has no real stablecoins at
all; testing the ERC-20 path there means deploying a mock ERC-20 and writing
its address into `donation_tokens` by hand.

## Watching

One goroutine per enabled, watchable chain (`Service.WatchChain`), polling
(no WebSocket subscription -- HTTP polling is simpler and works identically
against Ganache, Sepolia, and every future chain without needing `ws_url`
wired up):

1. **Native currency**: fetch each new block, scan every transaction's `to`
   address against known donation addresses.
2. **USDT/USDC**: `eth_getLogs` filtered to the token contract and the
   standard `Transfer(address,address,uint256)` topic, `to` = the address --
   a targeted, cheap query, not a full block scan.
3. Every still-pending deposit's confirmation count is refreshed against the
   chain's latest block each pass; once it reaches
   `donation_chains.confirmations_required` (currently 1 for Ganache, 10 for
   Sepolia and everything else) it flips to `confirmed`.
4. Confirmed deposits are priced and credited to Stars in the same pass.

A deposit is uniquely identified by `(chain_key, tx_hash, log_index)`
(`log_index = -1` for a native transfer, which has no log). Re-scanning a
block range that was already covered is a safe no-op: `RecordDonationDeposit`
is `ON CONFLICT DO NOTHING`, and `CreditDonationDeposit` only fires from a
`WHERE status = 'confirmed'` guard, so nothing can ever be credited twice --
proven by `internal/store/postgres/donations_ganache_integration_test.go`,
which sends a real 1 ETH transaction on a local Ganache node and asserts the
resulting Stars credit end to end.

## Pricing

Stablecoins are priced 1:1 to USD. Native currency uses
`donation_chains.manual_usd_rate_micros` (USD per one whole unit, ×1e6) --
an admin-set placeholder rate until a Chainlink price feed is wired in via
`donation_chains.price_feed_address` (not implemented yet; the column
exists, reading it doesn't). USD converts to Stars at the same $0.013/Star
rate this server already uses elsewhere
(`internal/compat/tdesktop/startup_stubs.go`'s `UsdRate: 0.013`), rounded
down -- a donor is never credited more Stars than their deposit was
actually worth.

## Notifications

Once the watcher credits a deposit, `donations.Service` calls its
`CreditNotifier` port (`app/bots.Service.NotifyDonationCredited`, wired via
`donationsService.SetNotifier(botsService)` in `cmd/telesrv/main.go`), which
sends the donor a `@premiumbot` chat message naming the amount, asset, chain
and Stars credited. Proven end to end (not mocked) by
`internal/store/postgres/donations_ganache_integration_test.go`, which
asserts the actual chat message lands after a real Ganache deposit.

All chat text from `@premiumbot` (the `/deposit` reply included) uses
`domain.MessageEntity` spans for formatting -- the server has no markdown
parser on the send path, so a literal backtick/asterisk in `botReply.Text`
is sent and shown verbatim rather than rendered.

## Admin panel

The Donations page (`permission: donations.manage`) shows wallet
provisioning status and address count, every configured chain (enabled or
not) with an editable RPC/WS endpoint, confirmation depth, price feed
address and manual USD rate, and the deposit ledger across every user. It
never exposes the wallet mnemonic or a private key -- no admin route
returns either; `Wallet.PrivateKeyHex` (see "Not yet built" below) is not
reachable from the admin API at all. Writes go through
`internal/admin.Service.UpdateDonationChain` (audit trail, actor/reason
required), the same pattern as Premium plan edits.

## Not yet built

- **`@premiumbot` `/deposit` QR code.** The command answers with the raw
  address today; ideally it'd also send a QR image via `botReply.Media`.
- **"Support the project"**, the separate single-operator-wallet flow for
  the site and admin panel -- unrelated to per-user crediting, not started.
- **Chainlink price feeds** for native currency, replacing the manual rate.
- **Manual, operator-triggered sweep** of accumulated funds out of
  per-user deposit addresses into a treasury address. `Wallet.PrivateKeyHex`
  exists for this; nothing calls it yet.
- **Reorg handling.** A deposit currently only moves forward
  (`pending → confirmed → credited`); there is no `orphaned` transition yet
  if a block gets reorganized out before reaching depth. Ganache/Sepolia
  testing at low confirmation counts makes this more likely to matter in
  practice than it will once mainnet chains (12+ confirmations) are enabled.
