# Bedolaga formatted-text + Telegram Login demo

This demo replicates the key Bot factory configuration of Bedolaga:

```python
Bot(
    ...,
    default=DefaultBotProperties(parse_mode=ParseMode.HTML),
)
```

So `/start`'s `message.answer()` does not pass `parse_mode` explicitly, yet
aiogram still sends `parse_mode=HTML` to telesrv automatically. `/formatdemo`
sends the default HTML, legacy Markdown, and MarkdownV2 in turn, to verify the
full `aiogram → telesrv Bot API → MTProto message/update → TDesktop` chain.

`/richdemo` further replicates Bedolaga's rich menu: it calls `sendRichMessage`
to send HTML and Markdown `InputRichMessage`s with an inline callback keyboard,
then edits the HTML menu via `editMessageText.rich_message`. The HTML sample
covers heading, divider, bordered/striped table, `tg-time`, details, blockquote,
code and footer. The first request deliberately carries a remote logo; after the
current local blob backend returns `WEBPAGE_MEDIA_EMPTY`, the demo drops the logo
and retries per Bedolaga's existing strategy, without degrading the body and
buttons into a classic menu.

## Installation

A virtual environment is recommended; pass the token only through environment
variables:

```powershell
python -m venv "$env:TEMP\telesrv-bedolaga-demo-venv"
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" -m pip install `
  -r .\cmd\bots\bedolagaformat\requirements.txt

$env:TELESRV_BOT_TOKEN = "<bot_id>:<secret>"
$env:TELESRV_BOT_API_SERVER = "http://127.0.0.1:8081"
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\demo.py --drop-pending
```

Then send to the bot in TDesktop:

```text
/start
/formatdemo
/richdemo
```

You can also skip polling and send the three formatting test messages directly
to a specified private chat:

```powershell
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\demo.py `
  --send-only `
  --send-chat-id 1780243200 `
  --marker BEDOLAGA-LOCAL-VERIFY
```

Verify the rich menu proactively only (HTML + Markdown + buttons + edit + logo
fallback):

```powershell
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\demo.py `
  --send-only `
  --rich-only `
  --send-chat-id 1780243200 `
  --marker BEDOLAGA-RICH-VERIFY
```

`--base-url` only accepts the API server root address; do not append `/bot`. The
script never prints the token, nor writes it to a file.

## Telegram Login end to end

The same demo also provides `/logindemo`, covering three mutually independent
public contracts:

1. Bot API `login_url` button → TDesktop/Android
   `messages.requestUrlAuth/acceptUrlAuth` → legacy HMAC callback;
2. telesrv local `/telegram-login.js` → popup `postMessage` → JWKS signature
   verification;
3. Server-side Authorization Code + PKCE S256 → `/token` Basic Client Secret →
   JWKS verification and `issuer/audience/nonce/subject` re-check.

First run `/setlogin` for the target bot in telesrv's @BotFather. After choosing
a bot once, you can send the commands one by one, or paste the three lines below
as a single multiline message, without re-running `/setlogin` or re-selecting
the bot each time:

```text
add origin http://127.0.0.1:3000
add redirect http://127.0.0.1:3000/oauth/callback
enable
```

Send `/done` to exit the configuration session and view the final summary.
`/cancel` only exits and does not roll back commands that were already applied
successfully.

`/setlogin` shows the OIDC Client Secret only once when it first creates the
client; do not commit it to the repository. Use `/logininfo` to view the Client
ID and registration result, or `/resetloginsecret` to rotate the secret. When
using an HTTP domain/IP, configure `TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP=true` on
telesrv; the demo accepts any valid HTTP(S) issuer/public origin and no longer
restricts to loopback.

Put the one-time secret and Client ID into the process environment, then start:

```powershell
$env:TELESRV_BOT_LOGIN_DEMO = "1"
$env:TELESRV_BOT_LOGIN_ISSUER = "http://127.0.0.1:2401"
$env:TELESRV_BOT_LOGIN_CLIENT_ID = "<numeric bot user id>"
$env:TELESRV_BOT_LOGIN_CLIENT_SECRET = "<one-time OIDC client secret>"
$env:TELESRV_BOT_LOGIN_PUBLIC_URL = "http://127.0.0.1:3000"
$env:TELESRV_BOT_LOGIN_LISTEN = "127.0.0.1:3000"

& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\demo.py --drop-pending --login-demo
```

Send `/logindemo` to the bot. The first button must bring up the Telegram
client's native authorization confirmation; after approval the page shows
`login_url HMAC verified`. The second button opens the test page, where you can
run the JS SDK popup and Authorization Code + PKCE separately. The page only
shows signed claims, never the access token or Client Secret. When
`TELESRV_BOT_LOGIN_CLIENT_SECRET` is omitted the JS popup can still be verified,
but the server-side code flow is explicitly disabled.

Mobile Chrome makes the popup hand the short-lived browser token to the original
RP tab before jumping into Telegram/DrKLO; the parent tab then polls
`/auth/status` from the exact registered origin. So when the external app
round-trip closes the popup or loses `window.opener`, the original tab can still
complete JWKS verification. Do not configure wildcard CORS for `/auth/status`,
and do not log the browser token or ID token in the RP.

The demo's flow/state/nonce are kept only in single-process memory, with a
10-minute expiry and a 256-entry cap, solely for end-to-end verification on
local and controlled test deployments, not a production relying-party
implementation. The official iOS/Android SDK currently hardcodes
`https://oauth.telegram.org`; verifying a self-hosted issuer requires the
minimal base-URL patch recorded by the project or an equivalent test build, and
you must not mistake an unmodified official production SDK result for a
self-hosted service result.

Test commands:

```powershell
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\test_demo.py -v
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\test_login_demo.py -v
```
