# Behaviour that differs from official Telegram

`gramsrv` aims at wire compatibility with official clients, but a few server
decisions deliberately differ from the official service. None of them are
bugs; they matter mostly to client and bot authors and to end-to-end test
suites. The list was started from the report in
[#140](https://github.com/iamxvbaba/gramsrv/issues/140).

| Area | Official Telegram | gramsrv |
|---|---|---|
| Basic groups | `messages.createChat` creates a basic group; `messages.migrateChat` upgrades it later | Every group is a supergroup from the start. `messages.createChat` returns a deactivated legacy `chat` with `migrated_to` plus the supergroup under the same id. The legacy chat RPCs (`messages.addChatUser`, `deleteChatUser`, `deleteChat`, `editChatTitle`, `editChatAdmin`, ...) act on that supergroup. |
| Long text | Text over 4096 characters fails with `MESSAGE_TOO_LONG` | MTProto `messages.sendMessage` splits it into up to 16 messages; `MESSAGE_TOO_LONG` only above 16 × 4096 characters. The HTTP Bot API keeps the 4096 limit. |
| Scheduled messages | A `schedule_date` in the near future is scheduled | A `schedule_date` at most 20 seconds ahead is sent immediately, without an error. |
| Privacy defaults | "Who can add me to groups and channels" defaults to everybody | Defaults to contacts only, as do birthday and peer-to-peer calls; the phone number defaults to nobody. Bots are not subject to it ([#130](https://github.com/iamxvbaba/gramsrv/issues/130)). |
| Send rate | Per-chat limits of about one message per second with bursts | One per-account budget: `TELESRV_SEND_RATE_LIMIT` messages (default 30) per `TELESRV_SEND_RATE_WINDOW` (default 1 minute), across all chats; past it the client gets `FLOOD_WAIT_X` until the window resets. `0` disables it. |
| pts bookkeeping | Events that consume a pts arrive as their own update or `updatePtsChanged` | An event whose TL update has no `pts` field (for example being imported as someone's contact) is followed by an empty `updateDeleteMessages{messages: [], pts_count: N}` so the client advances its pts without a visible change. |
| Push notifications | APNs/FCM pushes reach backgrounded clients | Off by default. `account.registerDevice` is accepted; with `TELESRV_PUSH_ENABLED` and an FCM service account or web-push VAPID keys the server stores device tokens and sends encrypted FCM or RFC 8291 web pushes when the recipient has no live session (muted peers get a throttled `MESSAGE_MUTED` badge refresh). The Android client must be rebuilt against the operator's own Firebase project (`google-services.json` swap), the web client against the matching VAPID key. A default build still sends nothing, so a client only receives updates while it keeps a connection open ([#98](https://github.com/iamxvbaba/gramsrv/issues/98)). |
| HTTP Bot API | Full method set, 50 MB uploads | The subset listed in [bot-api.en.md](bot-api.en.md), 25 MiB uploads, and only `message`, `edited_message` and `callback_query` updates. |
| Star gift withdrawal | Fragment | Withdrawal links are local bearer URLs that need an HTTPS public base URL outside loopback; on a plain-HTTP LAN address they are disabled with a warning ([#135](https://github.com/iamxvbaba/gramsrv/issues/135)). |

When you find another difference, open an issue with the client version, the
RPC or Bot API method and what the official service does; it will be fixed or
added here.
