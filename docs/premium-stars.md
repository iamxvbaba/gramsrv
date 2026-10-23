# Telegram Premium paid with Stars

This implementation provides a Layer 228 Premium storefront, direct Premium
gifts, and the HTTP Bot API `giftPremiumSubscription` method on top of the
existing users, messages, updates, and Stars ledger.

## Supported entry points

- `help.getPremiumPromo` and `help.getAppConfig` advertise the configured
  built-in Premium bot. The Stars-only catalog is opened inside that bot.
- `@premiumbot` supports `/start`, `/premium`, `/gift`, `/status`, `/history`,
  `/terms`, and `/help`. `/gift` opens a regular-user peer picker, then presents
  the current server catalog and an invoice. A fallback command has the form
  `/gift <user_id> <months> [greeting]`.
  Menu and plan buttons are callback buttons handled by the local server; they
  do not navigate to the public `sgq.me` service.
  Every incoming message and callback also carries the physical auth-key,
  session id, `lang_code`, `system_lang_code`, and `lang_pack` from the exact
  `initConnection` request. The copy is Russian for `ru`, English for all other
  reported languages, and retains Russian only for legacy internal calls that
  have no client metadata.
- `payments.getPremiumGiftCodeOptions` returns ordered, enabled, one-user XTR
  plans. A supplied `boost_peer` is validated and then rejected with
  `BOOST_PEER_INVALID`; Premium giveaways and multi-user gift codes are not
  implemented.
- `payments.getPaymentForm` accepts
  `inputInvoicePremiumGiftStars`, an `@premiumbot` invoice message, and the
  native Premium subscription store purpose.
  Because that constructor has no product/month field, a normal native purchase
  maps to the shortest enabled plan and its `upgrade` variant maps to the
  longest enabled plan. An unverifiable external-store `restore` request is
  rejected instead of minting Premium locally.
- `payments.sendStarsForm` settles a form and returns the existing
  `payments.paymentResult` update container.
- The local HTTP Bot API exposes `giftPremiumSubscription` with `user_id`,
  `month_count`, `star_count`, optional `text`, `text_parse_mode`, and
  `text_entities`. It validates `month_count` and `star_count` against the same
  catalog used by MTProto.

Bot API callers can supply an `Idempotency-Key` HTTP header or the local
`request_id` extension. Reusing the same key with the same request returns the
stored successful result; reusing it with different recipient, plan, price, or
text is rejected. Calls without either field remain compatible with the
official method but receive a new server-generated request key, so an
application that retries requests should always send `Idempotency-Key`.

## Layer 228 wire contract

The implementation uses constructors from the vendored Layer 228 schema:

| Constructor | ID | Relevant flags |
|---|---:|---|
| `inputInvoicePremiumGiftStars` | `0xdabab2ef` | message: bit 0 |
| `payments.paymentFormStars` | `0x7bf6b15c` | schema-generated |
| `premiumGiftCodeOption` | `0x257e962b` | store product: bit 0; quantity: bit 1 (both absent for XTR) |
| `messageActionGiftPremium` | `0x48e91302` | crypto pair: bit 0; message: bit 1 |
| `starsTransactionPeerPremiumBot` | `0x250dbaf8` | no fields |
| `inputStorePaymentPremiumSubscription` | `0xa6751e66` | restore: bit 0 |

The direct gift parser verifies `InputUser.access_hash`, rejects self-gifts and
bot/deleted/system recipients, bounds the greeting to 128 Unicode characters,
allows only bold, italic, underline, strikethrough, spoiler, and valid custom
emoji entities, and verifies every entity against UTF-16 boundaries, including
surrogate-pair boundaries. The recipient's persisted
`disallow_premium_gifts` setting is checked both while creating the form and
again during settlement.

## Catalog and configuration

The authoritative catalog is `premium_plans`. A client never supplies the
charged price. Every form snapshots the amount, duration, and plan version, and
settlement checks the snapshot against the current enabled catalog again.

`TELESRV_PREMIUM_PLANS` seeds and synchronizes config-owned rows. Saving a row
through the protected admin UI/API changes its ownership to `admin`, so a
restart does not overwrite the operator's price, duration, label, order, or
enabled state. Plans are disabled rather than deleted, and the last enabled
plan cannot be disabled. Existing checkout forms retain their original
snapshot but fail settlement after a plan version changes.

| Environment variable | Default | Meaning |
|---|---|---|
| `TELESRV_PREMIUM_BOT_USERNAME` | `premiumbot` | Reserved built-in storefront username, without `@`. |
| `TELESRV_PREMIUM_BOT_USER_ID` | `1250000015` | Stable positive system-user ID that must not collide with another built-in account. |
| `TELESRV_PREMIUM_PLANS` | `3:90:750,6:180:1300,12:365:2400` | Comma-separated `months:duration_days:amount_stars` catalog. |
| `TELESRV_PREMIUM_SWEEP_INTERVAL` | `1m` | Expiration worker interval. |
| `TELESRV_PREMIUM_SWEEP_BATCH` | `500` | Expired entitlement rows processed per transaction. |

Durations use the catalog's fixed `duration_days` everywhere: invoice,
entitlement, service message, and expiration. Extension is
`max(now, premium_until) + duration_days`.

## Atomic settlement

Before issuing a form, the application reads the existing Stars account through
the normal Stars service, which also applies the configured one-time starting
grant. `@premiumbot` shows the resulting balance alongside the exact catalog
price. Settlement then runs in one PostgreSQL transaction:

1. locks buyer and recipient users in stable ID order through the existing
   private-message state machine;
2. locks the PaymentIntent and revalidates owner, status, expiry, invoice
   snapshot, plan version, recipient, and the latest gift-privacy restriction;
3. locks and debits the buyer's existing Stars balance without allowing it to
   become negative;
4. inserts one immutable negative Stars transaction linked to the PaymentIntent;
5. inserts one entitlement and extends `users.premium_expires_at`;
6. marks the intent paid and stores transaction/message IDs;
7. inserts the purchase audit event;
8. writes both private message boxes, dialogs, PTS events, and dispatch outbox
   rows through the existing message transaction hooks.

Gift purchases create `messageActionGiftPremium` between buyer and recipient.
Self purchases create a confirmation from `@premiumbot`. Stars are burned by the
platform: the buyer receives a negative ledger entry whose peer projects as
`starsTransactionPeerPremiumBot`; the Premium bot is not credited.

`form_id`, PaymentIntent idempotency keys, entitlement payment IDs, and linked
transaction IDs are unique. Concurrent retries therefore cannot double debit,
extend twice, or create a second service message.

## Storage

Migration `0167_premium_stars` adds:

- `premium_plans`;
- `premium_payment_intents`;
- `premium_entitlements`;
- `premium_audit_events`;
- Premium linkage columns on `stars_transactions`;
- `users.premium_updated_at` and the Layer 228 disallowed-gift switches in
  `account_settings`;
- the built-in Premium bot user, bot profile, reserved username, and read-model
  versions.

Migration `0168_premium_plan_management` records whether a catalog row is
managed by startup configuration or by an operator. Apply both migrations
before starting a build that includes the Premium catalog editor.

Indexes cover plan order, form/idempotency/transaction uniqueness, pending form
expiry, recipient history, entitlement user/expiry/status, payment and
transaction uniqueness, and audit lookup by target/payment/command.

Expiration is lazy on every User projection and durable through a restart-safe
worker. The worker expires rows in batches, recomputes the maximum remaining
active entitlement, writes audit events, invalidates User/UserFull caches, and
pushes `updateUser`.

Refunds preserve the original debit and add a compensating Stars credit. The
associated entitlement becomes refunded, later entitlement windows are
compacted without destroying time from unrelated grants, the aggregate
expiration is recomputed, and the command is idempotent.

## Protected operator API

All Premium endpoints require a master token or a scoped token with
`premium.manage`:

- `GET /v1/premium/users/{id}/entitlements?limit=50`
- `GET /v1/premium/payments/{id}` — returns the PaymentIntent, linked
  entitlement, and original ledger transaction
- `GET /v1/premium/plans`
- `POST /v1/premium/plans/upsert`
- `POST /v1/accounts/grant-premium` — `months: 0` revokes
- `POST /v1/accounts/refund-premium`

Mutations require the existing command metadata (`command_id`, `actor`,
`reason`, and optional `dry_run`) and are written to both the admin command log
and Premium audit log.

The browser admin exposes one **Stars & Premium** (`/monetization`) workspace.
It shows the native/bot channels, supports manual Stars and Premium grants, and
edits the shared Premium catalog: Stars price, fixed duration, label, sort
order, and enabled state. The legacy `/premium` address remains an alias.
Browser reads and writes are protected by `premium.manage`; users with a
wildcard permission also have access.

## Stars-only promo compatibility

`premiumSubscriptionOption.currency` is an ISO 4217 fiat currency field. `XTR`
is valid for Premium gift-code options and Stars payment forms, but not for
that constructor. Publishing `XTR` in `help.premiumPromo.period_options` makes
official clients render duplicate Star icons or missing-glyph boxes. The
Stars-only server therefore leaves `period_options` empty and relies on
`premium_bot_username`; the bot displays the live Stars catalog without
ambiguous symbols.

## Verification

Run unit tests:

```powershell
go test ./...
```

Run the real PostgreSQL bootstrap/concurrency/privacy/refund/expiry tests
against a dedicated
database whose name contains `test`:

```powershell
$env:TELESRV_TEST_POSTGRES_DSN = "postgres://user:password@127.0.0.1:5432/telesrv_test?sslmode=disable"
go test ./internal/store/postgres -run "TestPremiumPurchaseAtomicConcurrentIdempotentRefundAndExpiry|TestAccountSettingsRoundTripPostgres" -count=1 -v
```

Manual Telegram Desktop check:

1. apply migrations and start the server;
2. open Settings → Telegram Premium and confirm there are no malformed fiat
   rows or duplicate Star glyphs;
3. open `@premiumbot`, press each menu callback, buy one plan, and verify the Stars balance/history and
   Premium badge;
4. use `/gift`, choose a regular user, pay the invoice, and verify
   `messageActionGiftPremium` in both dialog views;
5. disable Premium gifts in the recipient's gift privacy, verify
   `USER_PRIVACY_RESTRICTED`, then re-enable them;
6. retry the same `sendStarsForm` and confirm there is no second debit, extension,
   or message;
7. disconnect the recipient before gifting, reconnect, and confirm
   `updates.getDifference` restores the service message;
8. restart the server and confirm Premium persists;
9. edit a plan in **Stars & Premium**, restart the server, and confirm the
   operator-managed value remains;
10. configure a short test plan or admin grant, wait for expiry, and confirm the
   badge disappears after the worker or immediately on a fresh User read.

## Deliberate non-goals

- `boost_peer`, Premium giveaways, multi-user gift codes, fiat/store purchases,
  recurring renewal, and external payment providers are not implemented.
- Premium promo videos remain optional and use the existing
  `TELESRV_PREMIUM_PROMO_SEED_DIR`; an absent seed keeps the no-video response.
- No server-side `inputStickerSetPremiumGifts` asset is seeded; official clients
  may use their own built-in rendering for `messageActionGiftPremium`.
- Built-in bot copy currently ships in Russian and English; unsupported client
  languages fall back to English. Protocol responses and purchase data remain
  language-neutral, and the request-scoped session metadata path is reusable by
  future service bots.

Protocol references:
[Layer schema](https://core.telegram.org/schema),
[Premium API](https://core.telegram.org/api/premium),
[Stars API](https://core.telegram.org/api/stars),
[payment flow](https://core.telegram.org/api/payments),
[`inputInvoicePremiumGiftStars`](https://core.telegram.org/constructor/inputInvoicePremiumGiftStars),
[`premiumSubscriptionOption`](https://core.telegram.org/constructor/premiumSubscriptionOption),
[`payments.getPremiumGiftCodeOptions`](https://core.telegram.org/method/payments.getPremiumGiftCodeOptions),
[`payments.getPaymentForm`](https://core.telegram.org/method/payments.getPaymentForm),
[`payments.sendStarsForm`](https://core.telegram.org/method/payments.sendStarsForm), and
[`messageActionGiftPremium`](https://core.telegram.org/constructor/messageActionGiftPremium).
The HTTP surface follows the official
[`giftPremiumSubscription`](https://core.telegram.org/bots/api#giftpremiumsubscription)
parameter set and adds only the optional local idempotency key described above.
