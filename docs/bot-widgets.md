# Bot web widgets (Layer 233)

`gramsrv` implements the Layer 233 bot web-widget surface. This document records
what the server does and the deliberate limits.

## Media

- `inputMediaWidget` becomes `messageMediaWidget`. The markup, box size and
  `allow_queries` flag are stored on the message media snapshot.
- When `shared_widget` is set, the widget definition (HTML, payload, size,
  `allow_queries`, public data) is also persisted in the `bot_widgets` table and
  gets a server-assigned `widget_id`.
- `inputMediaWidgetID` resolves that definition by id and attaches it to the
  message as an `is_copy` widget. `chat_data` from the input is kept.
- The widget is accepted through `messages.sendMedia` / `sendMultiMedia` /
  `uploadMedia`, and through inline bot results: `inputBotInlineMessageWidget` /
  `inputBotInlineMessageWidgetID` project back to `botInlineMessageWidget`. It is
  not accepted on ephemeral or story media.

## Public / chat data

- `messages.getWidgetPublicData(peer, msg_id, hash)` returns
  `messages.widgetPublicData{hash, data, period}` or
  `messages.widgetPublicDataNotModified{period}` when the client already has the
  current `hash`. The effective data is the shared widget's data (when the
  message references a `widget_id`) over the per-message snapshot.
- `messages.editMessageWidgetData(peer, id, public_data?, chat_data?)` mutates
  the message media snapshot through the normal message-edit path (the edit update
  refreshes the client's message) **and** records the durable pts-bearing
  `updateMessageWidgetData` event (difference, offline replay and proactive
  delivery).
- `messages.editInlineBotMessageWidgetData(id, public_data?, chat_data?)` does
  the same for inline-bot messages owned by the calling bot.
- `messages.editWidgetData(widget_id, public_data, cache_time?)` replaces the
  shared widget's public data; only the widget's owner bot may call it.

## Queries

- `messages.getBotWidgetQueryAnswer(peer, msg_id, query_type, data)` registers a
  pending query, pushes `updateBotWidgetQuery` to the widget's bot, and waits up
  to 25 s for the answer.
- `messages.setBotWidgetQueryAnswer(query_id, result?)` unblocks the waiter.
  Only the owning bot may resolve a query id; anything else is
  `QUERY_ID_INVALID`. A missing answer ends in `BOT_RESPONSE_TIMEOUT`.
- The pending registry is shared across nodes through a Redis store (mirroring
  `BotCallbackRegistryStore`): a `getBotWidgetQueryAnswer` and the bot's
  `setBotWidgetQueryAnswer` may land on different nodes. Without a shared store
  configured it falls back to process-local.
- `updateBotWidgetQuery.wallet_card` carries the querying user's wallet card
  (address/balance) when a wallet is bound.

## Transcription files

- `messages.transcribeAudioFile(file)` assembles the upload into a document and
  runs it through the existing ASR service (`TELESRV_ASR_URL`); it caches by
  document id and the client re-calls until `pending` is false.
- `messages.getTranscriptionLangs` returns an empty list: the ASR backend exposes
  no fixed language set.

## Deliberate limits

- `channels.toggleForum` models only the list and tabs layouts: enabling a forum
  with `sidebar=true` fails closed (the author's Layer 233 decision is unchanged),
  and `channel.forum_sidebar` is never emitted.
- TON widget signing is not implemented: `wallet.tonConnect*` still refuses with
  `WALLET_UNAVAILABLE`. The query type still carries the client-supplied
  `UserWallet` through to the bot, and the server adds the user's `wallet_card`.

