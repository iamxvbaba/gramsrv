# python-telegram-bot echo demo

This demo uses the normal `python-telegram-bot` API and only swaps the Bot API
base URLs to telesrv.

```powershell
python -m pip install python-telegram-bot
$env:TELESRV_BOT_TOKEN = "<bot_id>:<secret>"
python .\cmd\bots\ptbecho\echo.py `
  --base-url http://127.0.0.1:8081/bot `
  --base-file-url http://127.0.0.1:8081/file/bot
```

In a group with BotFather privacy enabled, send a command such as:

```text
/ping hello from group
```

## Ephemeral echo (Bot API 10.2)

First register `private` as `is_ephemeral=true` via `setMyCommands`, then keep
this example polling. Send in a group on TDesktop Layer 228:

```text
/private@your_bot_username
```

This example reads `ephemeral_message_id` from PTB's `Message.api_kwargs`, and
within the 15-second action window replies `ephemeral echo: ...` with
`receiver_user_id` and `reply_parameters.ephemeral_message_id`. On request
failure it does not degrade into an ordinary group message. Alice should see two
messages with a visibility hint: the command she sent shows "Only visible to
@Bot", the bot reply shows "Only visible to you"; Bob should see neither.

Send one message proactively and exit:

```powershell
python .\cmd\bots\ptbecho\echo.py `
  --send-only `
  --send-chat-id -1000000000002 `
  --send-text "hello from python-telegram-bot"
```

Send one message proactively right after the long-polling echo starts:

```powershell
python .\cmd\bots\ptbecho\echo.py `
  --send-chat-id -1000000000002 `
  --send-text "ptbecho is online"
```

Send the two verification messages, a reply keyboard and an inline callback, and
keep polling:

```powershell
python .\cmd\bots\ptbecho\echo.py `
  --buttons-chat-id 1780243200
```

Both the reply keyboard and the inline keyboard show the three semantic colors
blue/green/red; tapping a reply button enters the echo chain as an ordinary text
message, and tapping an inline button makes the `callback_query` handler call
`answerCallbackQuery` and show `telesrv inline callback OK`. You can also send
`/buttons` in a private chat to generate the same two messages.

Optional arguments:

- `--send-count N`: send N messages proactively in a row.
- `--send-interval SEC`: the interval between consecutive sends.
- `TELESRV_BOT_DEMO_CHAT_ID` / `TELESRV_BOT_DEMO_SEND_TEXT`: environment-variable forms of the proactive send arguments.
- `--buttons-chat-id` / `TELESRV_BOT_DEMO_BUTTONS_CHAT_ID`: send the two keyboard verification messages and listen for callbacks.

The local supergroup chat id uses the Bot API form `-100<channel_id>`; for
example, channel id `2` is `-1000000000002`.

Implemented telesrv Bot API surface for this demo: `getMe`, `getUpdates`,
`deleteWebhook`, `sendMessage`, and file URL configuration. The wider gateway
also has basic `sendPhoto`, `sendDocument`, `editMessageText`, `deleteMessage`,
`answerCallbackQuery`, `getFile`, and `/file/bot...` support.
