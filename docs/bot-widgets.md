# Bot web widgets (Layer 233)

`gramsrv` implements the Layer 233 bot web-widget surface. This document records
what the server does and the deliberate limits.

## Media

- `inputMediaWidget` becomes `messageMediaWidget`. The markup is stored once (as
  `Payload`; the shared definition keeps the `HTML`), together with the box size
  and `allow_queries` flag.
- Widget media may only be authored by **bots**: a non-bot sender is rejected
  with `MEDIA_INVALID`, so a client-rendered widget can never become a query
  target.
- When `shared_widget` is set, the message gets a server-assigned random
  `widget_id` and the definition is persisted in the `bot_widgets` table **in the
  committed send transaction** (not while the input is parsed), so `uploadMedia`,
  `setInlineBotResults` and sends that fail later leave no row.
- `inputMediaWidgetID` resolves that definition by id and attaches it to the
  message as an `is_copy` widget. `chat_data` from the input is kept.
- `invert_media` from an inline widget input is carried through accept, the
  `botInlineMessageWidget` projection and the sent `message.invert_media`.
- Accepted through `messages.sendMedia` / `sendMultiMedia` / `uploadMedia`, and
  through inline bot results (`inputBotInlineMessageWidget` /
  `inputBotInlineMessageWidgetID` → `botInlineMessageWidget`). Not accepted on
  ephemeral or story media.

## Public / chat data

- `messages.getWidgetPublicData(peer, msg_id, hash)` returns
  `messages.widgetPublicData{hash, data, period}` or
  `messages.widgetPublicDataNotModified{period}` when the client already has the
  current `hash`. Per-message data takes precedence over the shared definition.
- `messages.editMessageWidgetData(peer, id, public_data?, chat_data?)` patches the
  widget data **in place** for both private and channel messages: only the media
  JSONB is rewritten (body/entities/edit_date untouched, the message is not marked
  edited), a compact pts-bearing `updateMessageWidgetData` is recorded/dispatched
  (account pts for private, channel pts + a `channel_widget_data` durable event
  for channels), and unchanged data returns `WIDGET_DATA_NOT_MODIFIED`. Only the
  **bot that sent the message** (or its via bot) may patch: a recipient, channel
  admin or ordinary member is rejected with `MESSAGE_ID_INVALID`.
- `messages.editInlineBotMessageWidgetData(id, public_data?, chat_data?)` does
  the same for inline-bot messages owned by the calling bot, reusing the in-place
  patch (channel messages use `EditInlineBotMessage` + fanout).
- `messages.editWidgetData(widget_id, public_data, cache_time?)` replaces the
  shared widget's public data for messages that carry no per-message override;
  only the widget's owner bot may call it, `public_data=""` clears it explicitly,
  and `cache_time` is only rewritten when the optional field is present.

## Queries

- `messages.getBotWidgetQueryAnswer(peer, msg_id, query_type, data)` registers a
  pending query and waits up to 25 s for the answer. The update follows the
  target: `updateBotWidgetCopyQuery` for an `is_copy` widget, a forwarded/shared
  widget the owner bot did not itself send, or a copy whose owner cannot be
  resolved (fail closed); `updateInlineBotWidgetQuery` (routed to `ViaBotID`) for
  a `via_bot` message; and `updateBotWidgetQuery` only for a message the bot
  itself sent (translated to the bot's own view in private chats). A private
  query's `chat_instance` hashes the querier, never the chat partner.
- `messages.setBotWidgetQueryAnswer(query_id, result?)` unblocks the waiter; only
  the owning bot may resolve a query id (`QUERY_ID_INVALID` otherwise,
  `BOT_RESPONSE_TIMEOUT` when unanswered).
- The pending registry is shared across nodes through a Redis store (mirroring
  `BotCallbackRegistryStore`); without one it is process-local.
- `updateBotWidgetQuery.wallet_card` carries the querying user's wallet card only
  when the query type opts in (`widgetQueryTypeData{with_wallet}`, or the
  sign/sent-transaction types).

## Transcription files

- `messages.transcribeAudioFile(file)` bounds the summed uploaded part size to
  25 MiB before it materialises a document (the check uses the stored part sizes,
  not an assumed 512 KiB part), then runs the existing ASR service
  (`TELESRV_ASR_URL`). A failed file job is terminal (`TRANSCRIPTION_FAILED`); a
  success pushes `updateTranscribedAudioFile` to the requester.
- `messages.getTranscriptionLangs` returns an empty list (the backend exposes no
  fixed language set).

## Deliberate limits

- A shared widget's `bot_widgets` row is created only by the committed send
  transaction, so `uploadMedia` returns a `widget_id` whose row does not exist
  until the media is actually sent. The in-memory store (tests only) materialises
  nothing on send; production is the postgres send path.
- `messages.transcribeAudioFile` bounds the uploaded size and document creation
  before the ASR job, but a job that fails after materialisation leaves the
  document row in place (documents are shared, unrefcounted).
- `channels.toggleForum` models only the list and tabs layouts: enabling a forum
  with `sidebar=true` fails closed (the author's Layer 233 decision is unchanged),
  and `channel.forum_sidebar` is never emitted.
- TON widget signing is not implemented: `wallet.tonConnect*` still refuses with
  `WALLET_UNAVAILABLE`.
