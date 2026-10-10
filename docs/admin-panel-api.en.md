# Admin panel: all routes and API contracts

Documentation of every route in the built-in `telesrv` admin panel — the SPA
page addresses and the API routes behind them. The panel server lives in
`cmd/telesrv-admin`; routes are declared in `(*server).routes()`
(`server.go:50`).

The panel is a plain HTTP server: it serves the built frontend (`/`) and a
JSON API under the `/api/` prefix. Every API path requires a session (cookie),
and every mutating request additionally requires a CSRF token. Many routes also
check an operator permission (`permission`).

## How to use this document

- For each GET endpoint, the **request parameters** (query) and a short **response** shape are listed.
- For each POST endpoint under `/api/actions/*` and the verification decisions, the **request body** (JSON) is given. They all share common fields (see below) plus their own specific fields.
- `int64` in JSON may be sent either as a number or as a string (e.g. `"user_id": "123"` or `"user_id": 123`) — the server accepts both (`flexInt64`). `flexUnix` accepts Unix seconds or a date in `2006-01-02` / RFC3339 form.
- All mutating requests go to the Admin API. A command's response is either `{"status":..., "message":..., "command_id":...}` on success (HTTP 200) or `{"error":..., "code":...}` on error. An optimistic-locking conflict is `409`.

## Authentication and session

| Method | Path | Description |
| --- | --- | --- |
| POST | `/api/login` | Log in. Body: `{"secret": "..."}`. Validates the config secret, issues a signed session cookie and CSRF cookie. The only mutating route without a CSRF token (no session yet); only Origin is checked. |
| POST | `/api/logout` | Log out. Destroys the session and CSRF cookie. |
| GET | `/api/session` | Returns `{"actor": "...", "permissions": [...], "csrf_token": "..."}`. Called by the panel on startup. |

The session lives in a signed cookie (default TTL 12 hours); there is no
server-side session store. Every request except `GET/HEAD/OPTIONS` and
`/api/login` must present:

- the `telesrv_admin_csrf` cookie;
- the `X-CSRF-Token` header with the same value;
- an Origin matching the host (when Origin is present).

Violating any of the three — `403 Forbidden`.

## Permissions and their checks

The panel distinguishes plain authorization (`requireAuthAPI`) from a
permission check (`requirePermission`). Permission names match the strings in
`TELESRV_ADMIN_UI_PERMISSIONS`:

| Permission | What it grants |
| --- | --- |
| `*` | All permissions (wildcard). |
| `premium.manage` | Managing the Premium plan catalog, granting Premium. |
| `bots.token.read` | Exporting a bot token (`/api/actions/export-bot-token`). |
| `verification.review` | Reading and deciding the official verification queue. |
| `verification.revoke` | Stripping the official badge (in addition to `verification.review`). |
| `botverification.review` | Reading and deciding the third-party (bot) verification queue. |
| `botverification.manage` | Appointing verifiers, editing the icon catalog, stripping a third-party mark. |

Official and third-party verification rights are intentionally independent. The
session's permission list is returned from `/api/session` so the UI can hide
unavailable sections.

## Interface pages (SPA)

SPA routes are declarative: any section name is served as `/` (index.html), and
the frontend decides what to render (`web/src/pages/Routes.tsx`).

| Path | Page |
| --- | --- |
| `/` | Dashboard (counters, storage stats, section links). |
| `/accounts` | Account list (with search). |
| `/accounts/{id}` | Account card: profile, actions. |
| `/channels` | Supergroup and channel list. |
| `/channels/{id}` | Channel card. |
| `/bots` | Bot list. |
| `/bots/{id}` | Bot card. |
| `/broadcasts` | Broadcasts. |
| `/monetization`, `/premium` | Stars and Premium: plans, grants. Requires `premium.manage`. |
| `/moderation` | Complaints and moderation: case list. |
| `/moderation/{id}` | Moderation case details. |
| `/emoji` | Emoji set catalog. |
| `/stickers` | Sticker pack catalog. |
| `/gif-catalog` | GIF catalog. |
| `/messages`, `/messages/private` | Private message audit. |
| `/messages/detail`, `/messages/private/detail` | Private message detail (`?owner_user_id=&msg_id=`). |
| `/messages/groups` | Group/channel message audit. |
| `/messages/groups/detail` | Group message detail (`?channel_id=&msg_id=`). |
| `/gifts` | Star gifts: catalog, collectibles, auctions. |
| `/give-gifts` | Gift granting. |
| `/collectible-usernames` | Collectible usernames. |
| `/collectible-usernames/{id}` | Collectible username card. |
| `/collectible-phones` | Anonymous numbers. |
| `/account-ratings` | Account ratings. |
| `/account-ratings/{user_id}` | Account rating card. |
| `/storage` | Object storage: stats. |
| `/verification` | Official verification: application queue. Requires `verification.review`. |
| `/verification/{id}` | Official verification application details. Requires `verification.review`. |
| `/bot-verification` | Third-party verification: verifiers, icons, marks, queue. Requires `botverification.review`. |
| `/bot-verification/{id}` | Third-party verification request details. Requires `botverification.review`. |

An unknown API path returns `404 {"error":"api route not found"}`; any unknown
frontend path is served as `/`.

## Response conventions

- Successful reads — `200` + JSON; files (avatars, animations, previews) — the file itself.
- Errors — JSON of the form `{"error": "...", "code": "..."}` with the corresponding HTTP status.
- `401` — missing/expired session; `403` — CSRF/Origin violation or missing permission (in `requirePermission` the body gets an added `permission` field); `409` — optimistic-locking conflict (moderation case, verification); `502` — Admin API unreachable.
- Errors from commands sent to the Admin API are returned as `{"status": ..., "message": ..., "error": ...}`.

## Common command fields (POST `/api/actions/*`)

Every mutating request carries in its body:

| Field | Type | Description |
| --- | --- | --- |
| `command_id` | string | Idempotency key for the command. Repeating the same `command_id` does not execute the action twice. If empty — generated by the server. |
| `reason` | string | Mandatory operation reason (audit). |
| `confirm` | bool | Operator confirmation (`true`). |

---

## API: dashboard and storage

| Method | Path | Description |
| --- | --- | --- |
| GET | `/api/dashboard` | Summary: `counts`, `storage`, and optionally `host`. |
| GET | `/api/storage/stats` | Object storage statistics. |

## API: accounts

| Method | Path | Parameters / response |
| --- | --- | --- |
| GET | `/api/accounts` | Params: `q` (search), `before_id` (int64), `before_active_us` (int64, microseconds), `limit` (int). Response: `query`, `limit`, `rows`, `has_more`, `next_before_id`, `next_before_active_us`, `listing`. |
| GET | `/api/accounts/{id}` | Account card: profile, flags, statistics. |
| GET | `/api/accounts/{id}/avatar` | Account avatar (file). |
| GET | `/api/account-ratings` | Params: `q`, `min_level` (int), `user_id` (int64), `before_id` (int64), `limit` (int). Response: `rows`, `has_more`, `next_before_id`. |
| GET | `/api/account-ratings/{user_id}` | Response: `rating`, `events`. |

## API: channels and supergroups

| Method | Path | Parameters / response |
| --- | --- | --- |
| GET | `/api/channels` | Params: `q`, `before_id` (int64), `before_updated_us` (int64, microseconds), `limit` (int). Response: `query`, `limit`, `rows`, `has_more`, `next_before_id`, `next_before_updated_us`, `listing`. |
| GET | `/api/channels/{id}` | Channel card. |
| GET | `/api/channels/{id}/avatar` | Channel avatar (file). |

## API: bots and broadcasts

| Method | Path | Parameters / response |
| --- | --- | --- |
| GET | `/api/bots` | Params: `q`, `before_id` (int64), `limit` (int). Response: `query`, `limit`, `rows`, `has_more`, `next_before_id`, `listing`. |
| GET | `/api/bots/{id}` | Bot card. |
| GET | `/api/broadcasts` | Params: `before_id` (int64), `limit` (int). Response: `limit`, `rows`, `has_more`, `next_before_id`. |

## API: media catalogs

| Method | Path | Parameters / response |
| --- | --- | --- |
| GET | `/api/emoji` | Params: `q`, `before_id` (int64), `limit` (int). Response: `query`, `rows`, `has_more`, `next_before_id`, `listing`. |
| GET | `/api/emoji/{id}/animation` | Emoji Lottie animation (file). |
| GET | `/api/stickers` | Param: `kind` (string, type filter). Response: `rows`, `max_items`. |
| GET | `/api/stickers/{id}/documents` | Response: `document_ids`. |
| GET | `/api/stickers/documents/{id}/animation` | Sticker animation (file). |
| GET | `/api/gif-catalog` | Response: proxied from Admin API (`/v1/gif-catalog`). |
| GET | `/api/gif-catalog/documents/{id}/preview` | GIF preview (file). |

## API: message audit

| Method | Path | Parameters / response |
| --- | --- | --- |
| GET | `/api/messages` | Params: `owner_user_id` (int64, required with `peer_id`), `peer_id` (int64), `before_date` (int64), `before_id` (int), `limit` (int). Response: `owner_user_id`, `peer_id`, `before_date`, `before_id`, `limit`, `rows`. |
| GET | `/api/messages/detail` | Params: `owner_user_id` (int64, req.), `msg_id` (int, req.). Response: message card. |
| GET | `/api/messages/groups` | Params: `channel_id` (int64, req.), `before_date` (int64), `before_id` (int), `limit` (int). Response: `channel_id`, `before_date`, `before_id`, `limit`, `rows`. |
| GET | `/api/messages/groups/detail` | Params: `channel_id` (int64, req.), `msg_id` (int, req.). Response: message card. |

## API: star gifts and collectibles

| Method | Path | Parameters / response |
| --- | --- | --- |
| GET | `/api/gifts` | Response: `Gifts` (gift list). |
| GET | `/api/auctions` | Response: `Auctions` — live state of all operator-authored auctions and scheduled drops. |
| GET | `/api/official-gifts` | Response: proxied from Admin API (`/v1/official-gifts`). |
| GET | `/api/official-gifts/{id}/animation` | Official gift animation (file). |
| GET | `/api/gifts/{id}/animation` | Gift animation (file). |
| GET | `/api/gifts/{id}/collectibles` | Response: proxied from Admin API (`/v1/gifts/{id}/collectibles`). |
| GET | `/api/gifts/{id}/collectibles/{kind}/{attribute_id}/animation` | Collectible attribute animation (file). `kind` ∈ {`model`, `pattern`}. |
| GET | `/api/collectible-usernames` | Params: `status` (`` | `vault` | `owned` | `burned`), `owner_user_id` (int64), `before_id` (int64), `limit` (int), `q`. Response: `rows`, `has_more`, `next_before_id`. |
| GET | `/api/collectible-usernames/{id}` | Response: `asset`, `transfers`. |
| GET | `/api/collectible-phones` | Params are passed through to the Admin API as-is (`/v1/collectible-phones?...`). |
| GET | `/api/collectible-phones/{id}` | Params are passed through to the Admin API (`/v1/collectible-phones/{id}?...`). |

## API: Premium

All routes in this section require the `premium.manage` permission (checked both
at the panel and at the Admin API).

| Method | Path | Description |
| --- | --- | --- |
| GET | `/api/premium/plans` | Premium plan catalog (proxied to Admin API `/v1/premium/plans`). |

## API: moderation

All reads pass query parameters through to the Admin API as-is
(`/v1/moderation/...`). Decisions are `POST` (see below).

| Method | Path | Description |
| --- | --- | --- |
| GET | `/api/moderation/cases` | Moderation case list. |
| GET | `/api/moderation/cases/{id}` | Moderation case. |
| GET | `/api/moderation/reports/{id}` | Report. |
| POST | `/api/moderation/cases/{id}/claim` | Claim the case. Body: common fields + `version` (int64), `internal_note` (string). |
| POST | `/api/moderation/cases/{id}/decide` | Decide the case. Body: common fields + `version` (int64), `internal_note` (string). |
| POST | `/api/moderation/cases/{id}/appeals/{appeal_id}/review` | Review an appeal. Body: common fields + `version` (int64), `internal_note` (string). |

## API: official verification

All routes require the `verification.review` permission. Reads go straight to
PostgreSQL; decisions always go through the Admin API (command journal, state
machine, optimistic locking).

| Method | Path | Parameters / body |
| --- | --- | --- |
| GET | `/api/verification/applications` | Params: `status`, `target_type`, `reviewer`, `q`, `before_id`, `limit`. Response: `rows`, `has_more`, `next_before_id`. |
| GET | `/api/verification/applications/{id}` | Response: application, events, `applicant_controls_target`, `target_verified`. |
| GET | `/api/verification/counts` | Application counts by status. |
| POST | `/api/verification/applications/{id}/claim` | Decision body: common fields + `version` (int64), `internal_note` (string). |
| POST | `/api/verification/applications/{id}/approve` | Body: common fields + `version` (int64), `internal_note` (string). Grants badge. |
| POST | `/api/verification/applications/{id}/reject` | Body: common fields + `version` (int64), `internal_note` (string). |
| POST | `/api/actions/revoke-verification` | Strip a badge. Requires `verification.review` **and** `verification.revoke`. Body — see actions section below. |

A "decided by another moderator" conflict is returned as `409 Conflict`.

## API: third-party (bot) verification

A separate mechanism with separate tables, permissions, and routes. Queue reads
and decisions require `botverification.review`; managing verifiers, the icon
catalog, and stripping marks requires `botverification.manage`.

| Method | Path | Parameters / body |
| --- | --- | --- |
| GET | `/api/botverification/verifiers` | Params: `enabled_only` (bool), `limit` (int). Response: `rows`. |
| GET | `/api/botverification/icons` | Params: `active_only` (bool), `limit` (int). Response: `rows`. |
| GET | `/api/botverification/marks` | Params: `peer_type`, `verifier_bot_id` (int64), `q`, `before_id` (int64), `limit` (int). Response: `rows`, `has_more`, `next_before_id`. |
| GET | `/api/botverification/requests` | Params: `status`, `peer_type`, `verifier_bot_id` (int64), `q`, `before_id` (int64), `limit` (int). Response: `rows`, `has_more`, `next_before_id`. |
| GET | `/api/botverification/requests/{id}` | Response: `request`, `verifier`, `mark_active`. |
| GET | `/api/botverification/counts` | Request counts by status. Response: `counts`. |
| POST | `/api/botverification/requests/{id}/approve` | Decision body: common fields + `version` (int64), `internal_note` (string). |
| POST | `/api/botverification/requests/{id}/reject` | Body: common fields + `version` (int64), `internal_note` (string). |
| POST | `/api/botverification/requests/{id}/revoke` | Body: common fields + `version` (int64), `internal_note` (string). |
| POST | `/api/actions/grant-bot-verifier` | Appoint a bot as verifier. Requires `botverification.manage`. Body — see below. |
| POST | `/api/actions/set-bot-verifier-enabled` | Enable/disable a verifier. Requires `botverification.manage`. Body — see below. |
| POST | `/api/actions/revoke-bot-verifier` | Strip a bot's verifier status. Requires `botverification.manage`. Body — see below. |
| POST | `/api/actions/upsert-verification-icon` | Add/edit a catalog icon. Requires `botverification.manage`. Body — see below. |
| POST | `/api/actions/set-verification-icon-active` | Enable/disable an icon. Requires `botverification.manage`. Body — see below. |
| POST | `/api/actions/revoke-custom-verification` | Strip a third-party mark. Requires `botverification.manage`. Body — see below. |

---

## API: account actions

All routes are `POST /api/actions/...`, require a session and CSRF. The request
body always contains the **common command fields** (`command_id`, `reason`,
`confirm`) plus the fields from the table below.

| Path | Specific body fields | Permission |
| --- | --- | --- |
| `set-frozen` | `user_id` (int64), `frozen` (bool), `freeze_until` (time, opt.), `freeze_appeal_url` (string, opt.) | — |
| `grant-premium` | `user_id` (int64), `months` (int) | `premium.manage` |
| `upsert-premium-plan` | `months` (int), `duration_days` (int), `amount_stars` (int64), `fiat_currency` (string), `fiat_amount` (int64), `store_product` (string), `store_quantity` (int), `enabled` (bool), `sort_order` (int), `label` (string), `expected_version` (int64) | `premium.manage` |
| `grant-stars` | `user_id` (int64), `amount` (int64) | — |
| `debit-stars` | `user_id` (int64), `amount` (int64, 1..1000000000); insufficient balance rejects the debit | — |
| `set-verified` | `user_id` (int64), `verified` (bool) | — |
| `set-account-flags` | `user_id` (int64), `scam` (bool), `fake` (bool) | — |
| `set-support` | `user_id` (int64), `support` (bool) | — |
| `set-account-username` | `user_id` (int64), `username` (string) | — |
| `set-account-profile` | `user_id` (int64), `first_name` (string), `last_name` (string) | — |
| `set-account-phone` | `user_id` (int64), `phone` (string) | — |
| `set-account-login-email` | `user_id` (int64), `email` (string) | — |
| `set-account-avatar` | **multipart**: `metadata` field (JSON with common fields + `user_id` (int64)) and a file in `file` | — |
| `set-account-color` | `user_id` (int64), `for_profile` (bool), `has_color` (bool), `color` (int), `background_emoji_id` (int64) | — |
| `set-account-emoji-status` | `user_id` (int64), `document_id` (int64), `until` (int, seconds) | — |
| `revoke-sessions` | `user_id` (int64), `hash` (int64, opt.), `keep_hash` (int64, opt.), `revoke_all` (bool) | — |

Example (granting Premium, the case from above):

```http
POST /api/actions/grant-premium
Content-Type: application/json
X-CSRF-Token: <csrf from /api/session>

{
  "command_id": "grant-premium-001",
  "reason": "Incident compensation",
  "confirm": true,
  "user_id": 123456789,
  "months": 12
}
```

## API: machine account lookup (v1)

`POST /v1/accounts/resolve-by-phone` — bearer-token-protected, read-only
account lookup used by the Telegram store bot's "fetch my ID" flow. It resolves
the numeric account ID from a phone number without viewer privacy projection,
so the bot can auto-save a user's server ID when they have already set up an
account there. Unlike every `/api/actions/...` route it carries no command
metadata, because the endpoint never mutates state and writes no audit entry.

| Field | Type | Description |
| --- | --- | --- |
| `phone` | string | Phone in any formatting (spaces, dashes, leading `+`); normalized server-side by `domain.NormalizePhone`. |

Response `200 OK`:

| Field | Type | Description |
| --- | --- | --- |
| `found` | bool | Whether an account with this phone exists. |
| `user_id` | int64 | The numeric account ID; `0` when `found` is `false`. |

Errors: `401` — invalid/absent bearer token; `500` — lookup failure;
`501` — deployment built without a lookup backend. Missing numbers are a
success with `found: false`, never `404`.

Example:

```http
POST /v1/accounts/resolve-by-phone
Authorization: Bearer <token>
Content-Type: application/json

{ "phone": "+79991234567" }

// 200
{ "found": true, "user_id": 1780243207 }
```

## API: channel actions

| Path | Specific body fields | Permission |
| --- | --- | --- |
| `set-channel-flags` | `channel_id` (int64), `scam` (bool), `fake` (bool) | — |
| `set-channel-settings` | `channel_id` (int64), `gigagroup` (*bool), `antispam` (*bool), `participants_hidden` (*bool), `noforwards` (*bool), `join_to_send` (*bool), `join_request` (*bool), `slowmode_seconds` (*int) — all optional pointers | — |
| `set-channel-username` | `channel_id` (int64), `username` (string) | — |
| `set-channel-color` | `channel_id` (int64), `for_profile` (bool), `has_color` (bool), `color` (int), `background_emoji_id` (int64) | — |
| `set-channel-emoji-status` | `channel_id` (int64), `document_id` (int64), `until` (int) | — |
| `set-channel-avatar` | **multipart**: `metadata` (JSON with common fields + `channel_id` (int64)) and a file `file` | — |
| `set-channel-verified` | `channel_id` (int64), `verified` (bool) | — |

## API: bot actions

| Path | Specific body fields | Permission |
| --- | --- | --- |
| `create-bot` | `owner_user_id` (int64), `name` (string), `username` (string) | — |
| `delete-bot` | `bot_user_id` (int64) | — |
| `export-bot-token` | `bot_user_id` (int64) | `bots.token.read` |
| `create-broadcast` | `message` (string), `target_mode` (string), `user_ids` ([]int64, opt.) | — |

## API: sticker and emoji actions

| Path | Specific body fields | Permission |
| --- | --- | --- |
| `create-sticker-set` | **multipart**: `metadata` (JSON: common fields + `title`, `short_name`, `kind`, `emoji`, `keywords`) and a file `file` | — |
| `rename-sticker-set` | `set_id` (int64), `title` (string) | — |
| `add-sticker-to-set` | **multipart**: `metadata` (JSON: common fields + `set_id` (string), `emoji`, `keywords`) and a file `file` | — |
| `remove-sticker-from-set` | `set_id` (int64), `document_id` (int64) | — |
| `set-sticker-set-archived` | `set_id` (int64), `archived` (bool) | — |
| `set-sticker-set-sort-order` | `set_id` (int64), `sort_order` (int) | — |
| `delete-sticker-set` | `set_id` (int64) | — |

## API: GIF catalog actions

| Path | Specific body fields | Permission |
| --- | --- | --- |
| `create-gif-catalog-entry` | **multipart**: `metadata` (JSON: common fields + `title`) and a file `file` | — |
| `set-gif-catalog-enabled` | `id` (int64, sent as string), `enabled` (bool) | — |
| `set-gif-catalog-sort-order` | `id` (int64, string), `sort_order` (int) | — |
| `delete-gif-catalog-entry` | `id` (int64, string) | — |

## API: gift and collectible actions

| Path | Specific body fields | Permission |
| --- | --- | --- |
| `import-gift` | **multipart**: `metadata` (JSON: common fields + `gift_id` (int64), `title`, `stars` (int64), `convert_stars` (int64), `enabled` (bool), `sort_order` (int), `auction` (bool), `auction_slug`, `gifts_per_round` (int), `auction_start_date` (int), `auction_round_duration` (int), `availability_total` (int), `locked_until_date` (int)) and a file `file` | — |
| `import-official-gift` | common fields + `source_gift_id` (string), `gift_id` (int64), `title`, `stars` (int64), `convert_stars` (int64), `enabled` (bool), `sort_order` (int), `include_collectible` (bool), `upgrade_stars` (int64), `supply_total` (int), `slug_prefix` (string), `locked_until_date` (int) | — |
| `publish-gift-collectibles` | **multipart**, `gift_id` sent in query (`?gift_id=`): `metadata` (JSON: common fields + `upgrade_stars` (int64), `supply_total` (int), `slug_prefix` (string), `models` ([]object), `patterns` ([]object), `backdrops` ([]object)); animations are files keyed by the `models`/`patterns` entries | — |
| `set-gift-enabled` | `gift_id` (int64), `enabled` (bool) | — |
| `set-gift-sort-order` | `gift_id` (int64), `sort_order` (int) | — |
| `give-gift` | common fields + `sender_user_id` (int64), `user_id` (int64), `channel_id` (int64), `gift_id` (int64), `hide_name` (bool), `message` (string), `upgrade` (bool), `model_attribute_id` (int64), `pattern_attribute_id` (int64), `backdrop_attribute_id` (int64) | — |
| `set-nft-gift-wallet` | common fields + `ref` (string: slug, NFT address or numeric id), `wallet_name` (string, defaults to the typed `wallet_address` value), `wallet_address` (string: a TON mainnet address; a `.ton` name resolved on chain through TON DNS, falling back to the flashfragment username of the same name when DNS has no record; or a Telegram identity — `rayo`, `@rayo`, `rayo.t.me`, `t.me/rayo` — resolved only through flashfragment records: username claim, username mint, verified wallet of the collectible username owner, TON Connect. The typed name is never stored as an address, and the record that matched is reported as `wallet_source` in details), `host_user_id` (int64, optional), `clear` (bool, releases the gift back to its Telegram owner instead of binding a wallet) | — |

## API: collectible username actions

| Path | Specific body fields | Permission |
| --- | --- | --- |
| `mint-collectible-username` | `username` (string), `owner_user_id` (int64), `owner_channel_id` (int64), `currency` (string), `amount` (int64), `crypto_currency` (string), `crypto_amount` (int64), `url` (string), `purchase_date` (int/unix or date) | — |
| `transfer-collectible-username` | `username` (string), `to_user_id` (int64), `to_channel_id` (int64) | — |
| `revoke-collectible-username` | `username` (string), `burn` (bool) | — |
| `delete-collectible-username` | `username` (string) | — |

## API: anonymous number actions

| Path | Specific body fields | Permission |
| --- | --- | --- |
| `mint-collectible-phone` | `phone` (string), `tier` (string), `owner_user_id` (int64), `currency` (string), `amount` (int64), `crypto_currency` (string), `crypto_amount` (int64), `url` (string), `purchase_date` (int/unix or date) | — |
| `update-collectible-phone-price` | `phone` (string), `currency` (string), `amount` (int64), `crypto_currency` (string), `crypto_amount` (int64) | — |
| `transfer-collectible-phone` | `phone` (string), `to_user_id` (int64) | — |
| `revoke-collectible-phone` | `phone` (string), `burn` (bool) | — |
| `delete-collectible-phone` | `phone` (string) | — |

## Gift inventory when importing or replacing a revision

For a new official gift, enabling `include_collectible` uses `supply_total` as
both the unique-gift supply and the base gift's initial sales cap. If omitted,
the collectible supply is derived from the snapshot's availability or upgrade
variants. With collectible import disabled, this input does not limit ordinary
base gifts. Auctions still require their own finite supply.

An existing `gift_id` keeps its original base supply, limited flag, remaining
stock, resale counters and sale dates. Replacement never replenishes exhausted
stock or converts it to unlimited. A different base cap or auction/non-auction
mode requires a new gift identity; requesting it on an existing identity fails
without publishing either revision. The separate collectible-pool editor does
not change base stock. Inventory is rechecked under the purchase/catalog lock
at execution, and the result reports the committed remaining stock. Dry-run
does not reserve stock. The catalog badge shows remaining / total copies.

## API: account rating actions

| Path | Specific body fields | Permission |
| --- | --- | --- |
| `recompute-account-rating` | `user_id` (int64) | — |
| `adjust-account-rating` | `user_id` (int64), `amount` (int64) | — |

## API: message deletion

| Path | Specific body fields | Permission |
| --- | --- | --- |
| `delete-messages` | `owner_user_id` (int64), `peer_id` (int64), `ids` ([]int), `revoke` (bool) | — |
| `delete-history` | `owner_user_id` (int64), `peer_id` (int64), `max_id` (int), `min_date` (int), `max_date` (int), `max_batches` (int), `just_clear` (bool), `revoke` (bool) | — |

## API: verification decisions (request bodies)

Official verification — `revoke-verification`:

| Field | Type | Description |
| --- | --- | --- |
| `target_type` | string | Target type (`user` / `channel`, etc., validated by `domain.VerificationTargetType.Valid()`). |
| `target_id` | int64 | Target identifier (not the application!). |
| `internal_note` | string | Operator-only internal note (opt.). |
| + common fields | | `command_id`, `reason`, `confirm`. |

Third-party verification — verifier/mark actions (`botverification.manage`):

| Path | Specific body fields |
| --- | --- |
| `grant-bot-verifier` | `bot_id` (int64), `icon_document_id` (int64), `company_name` (string, req.), `default_description` (string), `can_modify_custom_description` (bool), `version` (int64, 0 for new) |
| `set-bot-verifier-enabled` | `bot_id` (int64), `enabled` (bool) |
| `revoke-bot-verifier` | `bot_id` (int64) |
| `upsert-verification-icon` | `document_id` (int64), `name` (string, req.), `owner_bot_id` (int64, opt., 0 = shared) |
| `set-verification-icon-active` | `icon_id` (int64), `active` (bool) |
| `revoke-custom-verification` | `verifier_bot_id` (int64), `peer_type` (string, validated), `peer_id` (int64) |

All of these also carry the common fields (`command_id`, `reason`, `confirm`).
