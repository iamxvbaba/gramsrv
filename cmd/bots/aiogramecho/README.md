# aiogram 3 echo demo

This example uses the standard aiogram 3 API and only points the API server at
telesrv. aiogram's `TelegramAPIServer.from_base()` automatically builds the
telesrv-supported `/bot<TOKEN>/<method>` and `/file/bot<TOKEN>/<path>`.

```powershell
python -m pip install -r .\cmd\bots\aiogramecho\requirements.txt
$env:TELESRV_BOT_TOKEN = "<bot_id>:<secret>"
python .\cmd\bots\aiogramecho\echo.py --drop-pending
```

## Ephemeral echo (Bot API 10.2)

First register `private` as `is_ephemeral=true` via `setMyCommands`, then send in
a group on TDesktop Layer 228:

```text
/private@your_bot_username
```

aiogram 3.30.0 parses `ephemeral_message_id` natively. Within the 15-second
action window the example replies `ephemeral echo: ...` carrying
`receiver_user_id` and `ReplyParameters(ephemeral_message_id=...)`; a failure
does not degrade into a normal message. Alice should see two messages with a
visibility hint: the command she sent shows "Only visible to @Bot", the bot
reply shows "Only visible to you"; Bob should see neither.

Send a reply keyboard and inline callback buttons in the three semantic colors:

```powershell
python .\cmd\bots\aiogramecho\echo.py `
  --buttons-chat-id 1780243200 `
  --drop-pending
```

The optional `--button-icon-id <custom_emoji_document_id>` also verifies the
button's custom emoji icon. Official Telegram restricts icon use by the bot
owner's Premium / Fragment entitlement; colors only accept `primary` (blue),
`success` (green), `danger` (red), and arbitrary RGB is rejected.

Send messages proactively only, without starting polling:

```powershell
python .\cmd\bots\aiogramecho\echo.py `
  --send-only `
  --send-chat-id 1780243200 `
  --send-text "hello from aiogram"
```

The default API server is `http://127.0.0.1:8081`, overridable with `--base-url`
or `TELESRV_BOT_API_SERVER`. Do not append `/bot` here; this differs from
ptbecho's `--base-url http://127.0.0.1:8081/bot` argument form.

Polling mode answers this example's `aiogram-*` callbacks, and also gives a
fallback acknowledgement for inline callbacks previously created by other demos
on the same test bot, so a TDesktop button does not spin forever.

## Webhook mode

telesrv now persists webhook configuration, delivers it through a cross-instance
lease, and only advances `update_id` after the target returns 2xx. aiogram can
register an HTTP/HTTPS domain or IP directly, or Caddy/Nginx/Tunnel can provide
public HTTPS:

```powershell
$env:TELESRV_BOT_WEBHOOK_URL = "http://192.0.2.25:8080/webhook"
$env:TELESRV_BOT_WEBHOOK_SECRET = "replace_with_a_random_secret"
python .\cmd\bots\aiogramecho\echo.py `
  --mode webhook `
  --listen-host 127.0.0.1 `
  --listen-port 8082 `
  --webhook-path /webhook `
  --drop-pending
```

The webhook URL may use any valid HTTP/HTTPS domain or IP and port `1..65535`;
the local listen address may also use plain HTTP. telesrv places `secret_token`
into `X-Telegram-Bot-Api-Secret-Token`, which aiogram verifies automatically. To
delete the configuration when the process exits, also add
`--delete-webhook-on-exit`; by default the configuration is kept, so an ordinary
restart does not drop a window of updates.

Polling and webhook are mutually exclusive for the same token; to switch back to
polling just start in the default mode, and the example first calls
`deleteWebhook`, then begins `getUpdates`.
