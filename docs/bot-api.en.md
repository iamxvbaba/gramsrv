# HTTP Bot API: supported methods

This document lists every Bot API method that the telesrv HTTP gateway
(`internal/botapi`, default port `8081`) actually implements, together with
the parameters it accepts, the extensions it adds, and the official methods it
deliberately does not support.

Russian version of this document: [bot-api.ru.md](./bot-api.ru.md).

## How to use this document

- [Transport](#transport)
- [Authentication](#authentication)
- [Request and response conventions](#request-and-response-conventions)
- [Errors](#errors)
- [Method summary](#method-summary)
- [Updates](#updates)
- [Methods: bot identity and commands](#methods-bot-identity-and-commands)
- [Methods: sending](#methods-sending)
- [Methods: rich messages](#methods-rich-messages)
- [Methods: ephemeral messages](#methods-ephemeral-messages)
- [Methods: editing and deleting](#methods-editing-and-deleting)
- [Methods: files](#methods-files)
- [Methods: webhooks](#methods-webhooks)
- [Methods: menus, emoji status, web apps](#methods-menus-emoji-status-web-apps)
- [Methods: payments](#methods-payments)
- [Markup and entities](#markup-and-entities)
- [Limits](#limits)
- [Not implemented](#not-implemented)
- [Demos and self-tests](#demos-and-self-tests)

## Transport

| Item | Value |
|---|---|
| Method endpoint | `http(s)://<host>/bot<TOKEN>/<METHOD>` |
| File download | `http(s)://<host>/file/bot<TOKEN>/<file_path>` |
| Listener setting | `TELESRV_BOT_API_ADDR`; an empty value disables the gateway |
| Request body limit | 25 MiB upload + 1 MiB overhead (`FILE_TOO_BIG`) |
| Accepted content types | `application/json`, `multipart/form-data`, `application/x-www-form-urlencoded` |

`<METHOD>` is matched case-insensitively. Both `GET` and `POST` reach the
dispatcher, so `urlencoded` GET requests work.

Client configuration:

- `python-telegram-bot`: pass `--base-url http://host:8081/bot` and
  `--base-file-url http://host:8081/file/bot`.
- `aiogram`: `TelegramAPIServer.from_base("http://host:8081")`.
- grammY: `new Api("host:8081")` / `api.config.use((ctx) => ({ apiRoot:
  "http://host:8081/bot" }))`.

## Authentication

The path token must be the bot's `<bot_id>:<secret>` pair and its secret must
match the value stored for that bot. Anything else returns HTTP `401` with
`ACCESS_TOKEN_INVALID`. There is no separate auth header; the token in the URL
is the credential, exactly like the official gateway.

File downloads (`/file/bot...`) are authenticated the same way.

## Request and response conventions

Success:

```json
{"ok": true, "result": {}}
```

Failure:

```json
{"ok": false, "error_code": 400, "description": "CHAT_ID_INVALID"}
```

`error_code` is the HTTP status code. `description` is an upper-case
`SCREAMING_SNAKE` error marker, not always the official Telegram wording —
match on the marker, not on the whole string.

Field decoding is generic: JSON bodies are flattened to strings, non-string
JSON values are re-marshalled to their JSON form, and `multipart` uses the
first value of each field. This is why several methods accept both the
`message_text` shorthand and the full `InputTextMessageContent` object, and
why `setChatMenuButton` accepts both a nested `menu_button` object and a flat
body carrying `type`.

## Errors

Markers the gateway can return directly:

| Marker | HTTP | Notes |
|---|---:|---|
| `ACCESS_TOKEN_INVALID` | 401 | bad or unknown token |
| `METHOD_NOT_FOUND` | 404 / 501 | unknown method, or the feature is not wired in this deployment |
| `BAD_REQUEST` | 400 | unclassified failure |
| `CHAT_ID_INVALID` | 400 | `chat_id` missing, zero, or unparsable |
| `MESSAGE_ID_INVALID` | 400 | `message_id` missing or `<= 0` |
| `MESSAGE_IDENTIFIER_INVALID` | 400 | `inline_message_id` mixed with `chat_id`/`message_id` |
| `MESSAGE_EMPTY` | 400 | no `text` given |
| `MESSAGE_TOO_LONG` | 400 | over the text limit |
| `MESSAGE_NOT_MODIFIED` | 400 | edit that changes nothing |
| `REPLY_PARAMETERS_INVALID` | 400 | conflicting reply selectors |
| `REPLY_MESSAGE_ID_INVALID` | 400 | negative or malformed reply target |
| `MESSAGE_THREAD_ID_INVALID` | 400 | ephemeral top-message id out of range |
| `ENTITY_INVALID`, `ENTITY_BOUNDS_INVALID`, `ENTITY_TYPE_UNSUPPORTED`, `ENTITIES_TOO_LONG` | 400 | entity list problems |
| `FILE_ID_INVALID`, `FILE_TOO_BIG`, `MEDIA_INVALID` | 400 | file/media problems |
| `BUTTON_INVALID`, `BUTTON_TYPE_INVALID`, `BUTTON_DATA_INVALID`, `BUTTON_URL_INVALID` | 400 | markup problems |
| `RICH_MESSAGE_INVALID`, `RICH_MESSAGE_TOO_LONG`, `RICH_MESSAGE_DATE_INVALID`, `RICH_MESSAGE_BLOCKS_UNSUPPORTED`, `RICH_MESSAGE_MEDIA_UNSUPPORTED`, `RICH_MESSAGE_OPTION_UNSUPPORTED` | 400 | rich message problems |
| `WEBPAGE_MEDIA_EMPTY` | 400 | rich HTML referenced remote media the local blob backend cannot materialize |
| `EFFECT_ID_INVALID`, `VALUE_INVALID` | 400 | bad numeric parameter |
| `BOT_COMMAND_INVALID` | 400 | malformed or oversized `commands` |
| `BOT_COMMAND_SCOPE_UNSUPPORTED` | 400 | non-default scope, or any `language_code` |
| `EPHEMERAL_MESSAGE_ID_INVALID`, `EPHEMERAL_TARGET_REQUIRED`, `EPHEMERAL_ACTION_EXPIRED` | 400 | ephemeral addressing problems |
| `QUERY_ID_INVALID`, `USER_ID_INVALID`, `RESULT_ID_INVALID`, `RESULT_ID_EMPTY`, `RESULT_TYPE_INVALID` | 400 | query/user/result problems |
| `OFFSET_INVALID` | 400 | `getUpdates` offset below `-10000` |
| `ALLOWED_UPDATES_INVALID` | 400 | not a JSON array of up to 100 strings |
| `ALLOWED_UPDATES_UNSUPPORTED` | 501 | gateway without the update-control service |
| `MAX_CONNECTIONS_INVALID` | 400 | outside `1..100` |
| `SECRET_TOKEN_INVALID` | 400 | characters outside `[A-Za-z0-9_-]`, or longer than 256 |
| `WEBHOOK_URL_INVALID` | 400 | not an absolute `http(s)` URL, longer than 2048, has userinfo/fragment, or a port outside `1..65535` |
| `CERTIFICATE_PINNING_UNSUPPORTED` | 400 | `certificate` / uploaded certificate supplied |
| `IP_ADDRESS_UNSUPPORTED` | 400 | `ip_address` supplied |
| `WEBHOOK_UNSUPPORTED` | 501 | gateway without the webhook service |
| `METHOD_NOT_FOUND` | 501 | `answerShippingQuery` |
| `BLOCKED_USER_EMOJI_STATUS_SERVICE_MISSING`, `BLOCKED_WEBAPP_QUERY_SERVICE_MISSING`, `BLOCKED_PREPARED_INLINE_SERVICE_MISSING` | 501 | optional service not wired |
| `USER_PERMISSION_DENIED` | 403 | emoji status without the required user grant |
| `PREMIUM_ACCOUNT_REQUIRED` | 400 | emoji status without Premium |
| `PREMIUM_GIFT_SELF_INVALID`, `PREMIUM_GIFT_CODE_INVALID`, `BALANCE_TOO_LOW`, `STAR_COUNT_INVALID`, `MONTH_COUNT_INVALID` | 400 | premium gift problems |
| `IDEMPOTENCY_KEY_INVALID` | 400 | conflicting `request_id`/`Idempotency-Key`, or bad charset/length |
| `PAYMENT_FORM_INVALID` | 400 | payment form rejected |
| `CHAT_WRITE_FORBIDDEN`, `CHAT_ADMIN_REQUIRED` | 400 | rights missing |
| `USER_BOT_REQUIRED` | 400 | target is not a bot user |
| `CONFLICT: ...` | 409 | concurrent poll or webhook delivery |
| `INTERNAL_SERVER_ERROR` | 500 / 503 | store or transport failure |

## Method summary

Implemented (37 methods + 1 file route):

| Group | Methods |
|---|---|
| Bot & commands | `getMe`, `setMyCommands`, `deleteMyCommands`, `getMyCommands` |
| Updates | `getUpdates` |
| Text | `sendMessage`, `sendRichMessage` |
| Media | `sendPhoto`, `sendAnimation`, `sendAudio`, `sendDocument`, `sendLivePhoto`, `sendSticker`, `sendVideo`, `sendVideoNote`, `sendVoice` |
| Location | `sendContact`, `sendLocation`, `sendVenue` |
| Edit/delete | `editMessageText`, `deleteMessage` |
| Ephemeral edit/delete | `editEphemeralMessageText`, `editEphemeralMessageMedia`, `editEphemeralMessageCaption`, `editEphemeralMessageReplyMarkup`, `deleteEphemeralMessage` |
| Callback | `answerCallbackQuery` |
| Files | `getFile`, `GET /file/bot<TOKEN>/<file_path>` |
| Webhooks | `setWebhook`, `deleteWebhook`, `getWebhookInfo` |
| Menus & status | `setChatMenuButton`, `getChatMenuButton`, `setUserEmojiStatus` |
| Web apps & inline | `answerWebAppQuery`, `savePreparedInlineMessage` |
| Payments | `giftPremiumSubscription` |
| Blocked by design | `answerShippingQuery` (HTTP 501) |

## Updates

Only three update kinds are produced:

| `allowed_updates` value | Update key |
|---|---|
| `message` | `message` |
| `edited_message` | `edited_message` |
| `callback_query` | `callback_query` |

`allowed_updates` is a JSON array of up to 100 strings; unknown names parse
successfully but never match anything, so do not rely on filtering to hide
updates — drop the fields in the bot. The setting is accepted both by
`getUpdates` and by `setWebhook`, and it persists per bot.

Ephemeral (Bot API 10.2) messages arrive as normal `message` updates, but
their `message_id` belongs to the ephemeral namespace rather than the chat's
message numbering, so ordinary `reply_to_message_id` will not address them.
Replies go back through `reply_parameters.ephemeral_message_id`; see
[Methods: ephemeral messages](#methods-ephemeral-messages).

Queue retention is controlled by `TELESRV_BOT_API_UPDATE_RETENTION`
(default `24h`); acknowledged updates are dropped earlier.

## Methods: bot identity and commands

### `getMe`

No parameters. Returns the bot's own `User` object.

### `setMyCommands`

| Parameter | Type | Notes |
|---|---|---|
| `commands` | JSON array | up to 100 entries of `{command, description, is_ephemeral}` |
| `scope` | object | only `{"type":"default"}` is accepted |
| `language_code` | string | must be empty |

`is_ephemeral=true` marks the command as an ephemeral one: invoking it in a
group produces a message only the caller can see, and the bot must answer
inside the ephemeral action window. Returns `true`.

### `deleteMyCommands`

Same scope rules as `setMyCommands`; clears the list. Returns `true`.

### `getMyCommands`

Same scope rules. Returns the stored array; entries include
`is_ephemeral: true` only when the flag is set.

## Methods: sending

### `sendMessage`

| Parameter | Type | Notes |
|---|---|---|
| `chat_id` | integer | required, non-zero; negative for groups/channels |
| `text` | string | required, up to 4096 characters after formatting |
| `parse_mode` | string | `HTML`, `Markdown`, `MarkdownV2`, or omitted for plain text |
| `entities` | JSON array | used when `parse_mode` is absent; ignored otherwise |
| `reply_markup` | object | any of the four constructors, see [Markup](#markup-and-entities) |
| `disable_web_page_preview` | boolean | |
| `disable_notification` | boolean | |
| `reply_to_message_id` | integer | |
| `receiver_user_id` | integer | **telesrv extension**: switches the call to an ephemeral send |
| `callback_query_id` | integer | ephemeral send anchored to a callback query |
| `reply_parameters` | object | `{ephemeral_message_id}` for ephemeral replies, `{message_id}` for ordinary ones |
| `message_thread_id` | integer | ephemeral: the top message id of the discussion group |

Ordinary and ephemeral addressing are mutually exclusive: with
`receiver_user_id` set, `reply_to_message_id` must stay unset and
`reply_parameters` may only carry `ephemeral_message_id`. Ephemeral sends
accept inline keyboards only (`BUTTON_TYPE_INVALID` otherwise). The result is
the same `Message` shape, with the ephemeral id in `message_id`.

### `sendPhoto`, `sendAnimation`, `sendAudio`, `sendDocument`, `sendSticker`, `sendVideo`, `sendVideoNote`, `sendVoice`

All share one handler.

| Parameter | Type | Notes |
|---|---|---|
| `chat_id` | integer | required |
| `<kind>` | string / file | the method's own field: `photo`, `animation`, `audio`, `document`, `sticker`, `video`, `video_note`, `voice` |
| `caption` | string | up to 1024 characters |
| `parse_mode`, `caption_entities` | | as in `sendMessage` |
| `reply_markup` | object | all four constructors |
| `disable_notification`, `reply_to_message_id` | | |
| `width`, `height`, `duration`, `title`, `performer` | integer / string | accepted and forwarded as metadata hints |
| `emoji` | string | sticker only |
| `receiver_user_id`, `callback_query_id`, `reply_parameters`, `message_thread_id` | | same ephemeral extension as `sendMessage` |

The file field accepts, in this order of precedence:

1. `attach://<field>` referencing a multipart file part;
2. an uploaded multipart part with the same field name;
3. an `http://` or `https://` URL (the server fetches it);
4. a `file_id` previously returned by `getFile`.

Anything else is `FILE_ID_INVALID`.

### `sendLivePhoto`

Two media fields: `photo` (still frame) and `live_photo` (the video part).
Both accept upload, `attach://`, or `file_id`. The video part **cannot** be an
HTTP URL — that is `FILE_ID_INVALID`, matching the official restriction.
Captions, `parse_mode`, and the ephemeral extension work as for the other
media methods.

### `sendContact`, `sendLocation`, `sendVenue`

These three are **ephemeral-only**: they require `receiver_user_id` (or a
`callback_query_id`) and otherwise fail with `EPHEMERAL_TARGET_REQUIRED`.

`sendContact` takes `chat_id`, `phone_number`, `first_name`, optional
`last_name`, and `vcard` (up to 2048 bytes), plus an optional inline
`reply_markup`.

`sendLocation` takes `chat_id`, `latitude` (`-90..90`), `longitude`
(`-180..180`), `horizontal_accuracy` (`0..1500`, default `0`). A non-zero
`live_period` is rejected with `MEDIA_INVALID` — live location is not
implemented.

`sendVenue` adds required `title` and `address`, and accepts either
`foursquare_id` + `foursquare_type` or `google_place_id` +
`google_place_type`.

## Methods: rich messages

### `sendRichMessage`

Telesrv extension, Layer 228 rich text. Sends `InputRichMessage` instead of a
plain formatted string, so tables, headings, dividers, details blocks and
footers reach the client as real rich blocks.

| Parameter | Type | Notes |
|---|---|---|
| `chat_id` | integer | required |
| `rich_message` | object | `{"html": "..."}` **or** `{"markdown": "..."}`, plus optional `is_rtl` and `skip_entity_detection` |
| `reply_markup` | object | inline keyboards only |
| `reply_parameters` | object | `{"message_id": N}`; the legacy `reply_to_message_id` is also accepted, but not both |
| `disable_notification`, `protect_content` | boolean | |
| `message_effect_id` | integer | message effect id |

Rules and rejections:

- exactly one of `html` / `markdown` must be non-empty; zero or both is
  `RICH_MESSAGE_INVALID`;
- `blocks` is parsed but rejected — `RICH_MESSAGE_BLOCKS_UNSUPPORTED`;
- `media` is parsed but rejected — `RICH_MESSAGE_MEDIA_UNSUPPORTED`;
- the source is capped at 256 KiB;
- `business_connection_id`, `message_thread_id`,
  `direct_messages_topic_id`, `allow_paid_broadcast`, and
  `suggested_post_parameters` are rejected with an explicit error instead of
  being silently ignored;
- rich HTML that references remote media (`img`, `video`, `audio`, `tg-map`,
  `tg-collage`, `tg-slideshow`) fails with `WEBPAGE_MEDIA_EMPTY`, because the
  local blob backend cannot materialize an arbitrary URL atomically. The
  documented workaround (see `cmd/bots/bedolagaformat`) is to retry once with
  the logo removed rather than degrade to a classic menu.

Supported HTML elements: `b`/`strong`, `i`/`em`, `u`/`ins`,
`s`/`strike`/`del`, `tg-spoiler`, `span` (only as
`<span class="tg-spoiler">`), `a`, `code`, `pre` (with `class="language-…"`),
`blockquote`, `tg-emoji`, `tg-time`, `footer`, and `table` (with the
`bordered` and `striped` attributes, per-cell alignment).
`tg-time` requires `unix` in `1..2^31-1` and a `format` built from `t`, `T`,
`d`, `D`, `w`/`W`, or exactly `r`/`R` for a relative timestamp.

Markdown input is parsed by the same rich builder used on the MTProto side,
so `#`-style headings, `---` dividers, pipe tables, `>` quotes, fenced code
and footer blocks all work.

`editMessageText` accepts the same `rich_message` object, and there is no
separate `editMessageRichMessage` method.

## Methods: ephemeral messages

Ephemeral messages (Bot API 10.2) are visible only to their receiver, who
sees a "only visible to you" notice. telesrv supports the whole lifecycle.

Sending is done through the ordinary methods with `receiver_user_id`:

- `sendMessage` — text;
- the media methods — media plus a caption;
- `sendContact`, `sendLocation`, `sendVenue` — ephemeral only.

Addressing rules:

- `receiver_user_id` is required and must be a positive integer;
- `callback_query_id` may replace it as the anchor (the receiver is derived
  from the query);
- `reply_parameters.ephemeral_message_id` replies to an existing ephemeral
  message, and cannot be combined with `callback_query_id`;
- `reply_parameters.message_id` is the ordinary reply form and is not
  combined with a receiver;
- `message_thread_id` is the top message of the ephemeral discussion group;
- the action window is 15 seconds — after it the server answers
  `EPHEMERAL_ACTION_EXPIRED`.

Editing and deleting:

| Method | Parameters |
|---|---|
| `editEphemeralMessageText` | `chat_id`, `receiver_user_id`, `ephemeral_message_id`, `text`, `parse_mode`, `entities`, `reply_markup` |
| `editEphemeralMessageCaption` | same, with `caption` + `caption_entities` |
| `editEphemeralMessageMedia` | same, plus `media` (see below) |
| `editEphemeralMessageReplyMarkup` | same, plus `reply_markup` only |
| `deleteEphemeralMessage` | `chat_id`, `receiver_user_id`, `ephemeral_message_id` |

`media` is an `InputMedia`-like object: `type` (one of `photo`, `animation`,
`audio`, `document`, `video`, `live_photo`), the file field, and optional
`caption`, `parse_mode`, `caption_entities`, `width`, `height`, `duration`,
`title`, `performer`. `live_photo` uses `photo` for the still and `media` for
the video. Raw uploads are not allowed here — only `file_id`, `attach://`-free
file references, or HTTPS URLs (`FILE_ID_INVALID` otherwise).

`reply_markup` is inline-only, and its *presence* matters: sending
`reply_markup` with an empty value clears the keyboard.

## Methods: editing and deleting

### `editMessageText`

The single edit method. It covers both ordinary and rich text edits, and both
chat messages and inline messages.

| Parameter | Type | Notes |
|---|---|---|
| `chat_id` + `message_id` | | one addressing mode |
| `inline_message_id` | string | the other mode; must not be mixed with the first |
| `text` | string | one content form |
| `rich_message` | object | the other content form; must not be mixed with `text` |
| `parse_mode`, `entities` | | as in `sendMessage` |
| `reply_markup` | object | inline only; presence clears when empty |
| `disable_web_page_preview` | boolean | |

For an `inline_message_id` the result is a boolean; for a chat message it is
the updated `Message`. An edit that changes nothing returns
`MESSAGE_NOT_MODIFIED`.

There is no `editMessageCaption`, `editMessageMedia`, `editMessageReplyMarkup`,
`editMessageLiveLocation`, or `stopMessageLiveLocation` — use
`editMessageText` (or the ephemeral variants) instead.

### `deleteMessage`

`chat_id` + `message_id`. Returns `true`. Deleting an already-deleted or
foreign message returns `false` or an error, as with the official gateway.

### `answerCallbackQuery`

| Parameter | Type | Notes |
|---|---|---|
| `callback_query_id` | string | required |
| `text` | string | notification text |
| `url` | string | |
| `show_alert` | boolean | modal instead of toast |
| `cache_time` | integer | seconds |

Returns `true`.

## Methods: files

### `getFile`

`file_id` must be a telesrv-issued id. The result always has
`file_unique_id == file_id` and `file_path == file_id`, so the download URL is
`/file/bot<TOKEN>/<file_id>`. Foreign or unknown ids are `FILE_ID_INVALID`.

### `GET /file/bot<TOKEN>/<file_path>`

Streams the file, chunk by chunk, with the stored MIME type and
`Content-Length`. The path is the `file_id`; a path with extra segments is
`404 FILE_NOT_FOUND`.

## Methods: webhooks

### `setWebhook`

| Parameter | Type | Notes |
|---|---|---|
| `url` | string | empty value deletes the webhook instead |
| `secret_token` | string | `[A-Za-z0-9_-]`, up to 256 characters |
| `max_connections` | integer | `1..100`, default `40` |
| `allowed_updates` | JSON array | see [Updates](#updates) |
| `drop_pending_updates` | boolean | |

`certificate` (and any uploaded certificate part) is rejected with
`CERTIFICATE_PINNING_UNSUPPORTED`; use a publicly trusted certificate.
`ip_address` is rejected with `IP_ADDRESS_UNSUPPORTED` — the gateway uses the
configured address list.

Setting a webhook while `getUpdates` is long-polling, or while a delivery is
in flight, returns `409 CONFLICT`.

Delivery behaviour:

- one HTTP `POST` per update, with a **single `Update` object** as the body
  (not an array like `getUpdates` returns);
- header `X-Telegram-Bot-Api-Secret-Token` carries the secret token;
- `Content-Type: application/json`; any `2xx` counts as delivered;
- redirects are never followed, so the secret cannot leak to another host;
- up to `max_connections` updates are delivered in parallel, 16 bots and 64
  concurrent HTTP requests overall;
- failures retry with exponential backoff from 1s up to 5 minutes, plus a
  small per-bot jitter;
- delivery is confirmed up to the first failed update, so nothing is skipped.

### `getWebhookInfo`

Returns `url`, `has_custom_certificate` (always `false` here),
`pending_update_count`, and — when a webhook is configured — `max_connections`,
`allowed_updates`, `last_error_date`, `last_error_message`.

### `deleteWebhook`

`drop_pending_updates` is optional. Returns `409` while a delivery is active.

## Methods: menus, emoji status, web apps

### `setChatMenuButton` / `getChatMenuButton`

`menu_button` is `{"type": "default"}`, `{"type": "commands"}`, or
`{"type": "web_app", "text": "...", "web_app": {"url": "..."}}`. A flat body
that carries `type` at the top level is also accepted, which keeps older
library serializers working. Setting returns `true`; getting returns the
current button (never `null` — an unset button reads as `{"type":
"default"}`).

### `setUserEmojiStatus`

`user_id`, `emoji_status_custom_emoji_id`, `emoji_status_expiration_date`.
Requires the user to have granted the bot permission to set their status
(otherwise `403 USER_PERMISSION_DENIED`) and to be a Premium user
(`PREMIUM_ACCOUNT_REQUIRED`). Returns `true`.

### `answerWebAppQuery`

`web_app_query_id` plus `result`. Only `InlineQueryResult` of type `article`
is supported: `id` (up to 64 characters), `title`, `description`, `url` (HTTPS
only), `input_message_content` (or the `message_text` shorthand),
`reply_markup` (inline only). Other result types are
`RESULT_TYPE_INVALID`. The response contains `inline_message_id` when the
answer produced one.

### `savePreparedInlineMessage`

`user_id`, `result` (same `article` restriction), and the peer filters
`allow_user_chats`, `allow_bot_chats`, `allow_group_chats`,
`allow_channel_chats`. Returns `{id, expiration_date}`.

## Methods: payments

### `giftPremiumSubscription`

Pays for a Premium gift out of the bot's Stars balance, through the same
durable Stars pipeline as MTProto `payments.sendStarsForm`.

| Parameter | Type | Notes |
|---|---|---|
| `user_id` | integer | recipient; self-gifts are rejected |
| `month_count` | integer | `1..120` |
| `star_count` | integer | positive, at most 10^15 |
| `text` | string | up to 128 characters |
| `text_parse_mode`, `text_entities` | | restricted entity set |
| `request_id` | string | telesrv extension for idempotency |

Send an `Idempotency-Key` HTTP header (or the local `request_id` field) — the
allowed charset is `[A-Za-z0-9]` plus `-`, `_`, `.`, `:` up to 128
characters. Replaying the same key with the same request returns the stored
result; replaying it with a different recipient, plan, price, or text is
`IDEMPOTENCY_KEY_INVALID`. Requests without any key still work, but each
retry charges again, so always send a key.

### `dropPendingUpdates`

Implemented: discards the bot's unconfirmed queued updates and answers `true`.
A bot that has fallen behind needs it, otherwise it spends forever working through
a backlog while `pre_checkout_query`, which lives 10 seconds, quietly expires.

### `answerPreCheckoutQuery`

Implemented. Before any Stars move, the payer waits for the bot's answer for **at
most 10 seconds** (Telegram's requirement). The bot receives a `pre_checkout_query`
update and answers with `pre_checkout_query_id`, `ok` and, when `ok=false`, an
`error_message`.

- `ok=true` — the Stars are charged and the bot receives `successful_payment`;
- `ok=false` — the payment is cancelled and `error_message` is shown to the payer;
- no answer within 10 seconds — the payment does not go through (`PAYMENT_FAILED`).

The timeout never degrades into charging anyway: a silent bot means a refusal,
otherwise pre-checkout would verify nothing. Only the bot that owns the order may
answer; a stale `pre_checkout_query_id` gets `QUERY_ID_INVALID`. The currency and
`invoice_payload` in the answer are not compared against what the bot recorded at
`sendInvoice`, so that check stays on the bot side, same as in Telegram.

### `answerShippingQuery`

Recognized, but answers HTTP `501 METHOD_NOT_FOUND`: a Stars payment has no
shipping step, so there is nothing to answer.
## Markup and entities

`reply_markup` accepts exactly one constructor:

| Constructor | Notes |
|---|---|
| `inline_keyboard` | rows of buttons |
| `keyboard` | reply keyboard; `resize_keyboard`, `one_time_keyboard`, `is_persistent`, `input_field_placeholder`, `selective` |
| `remove_keyboard` | `remove_keyboard: true`, `selective` |
| `force_reply` | `force_reply: true`, `input_field_placeholder`, `selective` |

Inline keyboard buttons (exactly one action per button, otherwise
`BUTTON_INVALID`): `url`, `callback_data` (up to 64 bytes),
`web_app`, `login_url` (`url`, `bot_username`, `forward_text`,
`request_write_access`), `switch_inline_query`,
`switch_inline_query_current_chat`, `switch_inline_query_chosen_chat`,
`copy_text`. Every button also accepts `text` (required),
`style` (`primary` / `danger` / `success` / omitted), and
`icon_custom_emoji_id`. Unknown fields are rejected rather than ignored.

Reply keyboard buttons: a plain string, or an object with `text` plus one of
`request_contact`, `request_location`, `request_poll` (`type`),
`request_users` (`request_id`, `user_is_bot`, `user_is_premium`,
`max_quantity`, `request_name`, `request_username`, `request_photo`),
`request_chat` (`request_id`, `chat_is_channel`, `chat_is_forum`,
`chat_has_username`, `chat_is_created`, `user_administrator_rights`,
`bot_administrator_rights`, `bot_is_member`, `request_title`,
`request_username`, `request_photo`), or `web_app`.

Message entities (for `entities` / `caption_entities`, and for the
`date_time` entity, which takes `unix_time` + `date_time_format`):

`bold`, `italic`, `underline`, `strikethrough`, `code`, `pre` (`language`),
`text_link` (`url`), `text_mention` (`user.id`), `spoiler`, `blockquote`,
`expandable_blockquote`, `custom_emoji` (`custom_emoji_id`), `mention`,
`hashtag`, `cashtag`, `bot_command`, `url`, `email`, `phone_number`,
`bank_card_number`, `date_time`.

Limits: 256 entities per message (`ENTITIES_TOO_LONG`), offsets in UTF-16 code
units, unknown types are `ENTITY_TYPE_UNSUPPORTED`.

## Limits

| Limit | Value |
|---|---|
| Text length | 4096 characters after parsing |
| Caption length | 1024 characters |
| Entities per message | 256 |
| Inline result id | 64 bytes |
| Callback data | 64 bytes |
| Commands per `setMyCommands` | 100 |
| Upload size | 25 MiB per file |
| `allowed_updates` entries | 100 |
| `getUpdates` `limit` | default and maximum 100 |
| `getUpdates` `timeout` | clamped to 0..50 seconds |
| `max_connections` | 1..100, default 40 |
| Rich message source | 256 KiB |
| `getFile` offset/limit | offset ignored; size reported in full |

## Not implemented

Every other official method returns HTTP `404 METHOD_NOT_FOUND`. The most
commonly reached ones:

- editing: `editMessageCaption`, `editMessageMedia`,
  `editMessageReplyMarkup`, `editMessageLiveLocation`,
  `stopMessageLiveLocation`, `stopPoll`;
- media groups and the rest of the `send*` surface: `sendMediaGroup`,
  `sendPoll`, `sendDice`, `sendGame`, `sendChatAction`;
- payments: `createInvoiceLink`, `sendInvoice`, `answerPreCheckoutQuery`,
  `answerShippingQuery`, `getStarTransactions`, `refundStarPayment`;
- chat administration: `banChatMember`, `unbanChatMember`,
  `restrictChatMember`, `promoteChatMember`, `setChatPermissions`,
  `setChatPhoto`, `deleteChatPhoto`, `pinChatMessage`, `unpinChatMessage`,
  `unpinAllChatMessages`, `setChatTitle`, `setChatDescription`,
  `leaveChat`;
- queries: `getChat`, `getChatAdministrators`, `getChatMember`,
  `getChatMemberCount`, `getForumTopic*`, `getUserChatBoosts`;
- messages: `forwardMessage`, `forwardMessages`, `copyMessage`,
  `copyMessages`, `sendPoll`, `getFile`-adjacent helpers;
- bot administration: `setMyName`, `setMyDescription`, `setMyShortDescription`,
  `setMyDefaultAdministratorRights`, `deleteWebhook` for other bots,
  `close`, `logOut`;
- reactions, games, and business-account methods.

MTProto clients are unaffected: everything implemented for the official
Android/iOS/Desktop clients is reached over MTProto, not HTTP. See
`docs/premium-stars.md` for the shared Stars and Premium pipelines.

## Demos and self-tests

| Path | Purpose |
|---|---|
| `cmd/bots/ptbecho` | `python-telegram-bot` echo against the gateway, including an ephemeral command demo |
| `cmd/bots/aiogramecho` | aiogram 3 echo, ephemeral replies, reply and inline keyboards |
| `cmd/bots/bedolagaformat` | HTML / Markdown / MarkdownV2, rich menus via `sendRichMessage`, Telegram Login |
| `cmd/bots/botcheck` | Go probe: imports the bot token over MTProto and checks the bot loop |
| `cmd/bots/botdemo` | Go example: a bot sending to a channel or user on a timer |
| `cmd/bots/grammystore` | Production grammY bot on the HTTP gateway (Postgres, own deployment) |
| `scripts/botapi_echo_once.py` | Single-shot send/receive self-test |
| `scripts/botapi_receive_selftest.py` | Update-queue and webhook self-test |
