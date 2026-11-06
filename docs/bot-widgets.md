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
- The widget is only accepted through `messages.sendMedia` / `sendMultiMedia` /
  `uploadMedia`; it is not accepted on ephemeral or story media.

## Public / chat data

- `messages.getWidgetPublicData(peer, msg_id, hash)` returns
  `messages.widgetPublicData{hash, data, period}` or
  `messages.widgetPublicDataNotModified{period}` when the client already has the
  current `hash`. The effective data is the shared widget's data (when the
  message references a `widget_id`) over the per-message snapshot.
- `messages.editMessageWidgetData(peer, id, public_data?, chat_data?)` mutates
  the message media snapshot and persists it through the normal message-edit
  path; the edit update carries the new `messageMediaWidget`.
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
- The pending registry is process-local (like the pre-checkout registry); a
  multi-node deployment must be single-node for the bot's answer to land, or the
  registry must gain a shared store like `BotCallbackRegistryStore`.

## Deliberate limits

- `updateMessageWidgetData` is not emitted as a standalone pts event yet: an
  edit re-sends the whole message via `updateEditMessage`, so a client sees the
  new widget data without a separate widget-data update.
- `messages.transcribeAudioFile` (file-based ASR) returns
  `TRANSCRIPTION_FAILED`; `messages.getTranscriptionLangs` returns an empty list.
  The message-based `messages.transcribeAudio` path is unchanged.
