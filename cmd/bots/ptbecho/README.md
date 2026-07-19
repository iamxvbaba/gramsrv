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

主动发送一条消息并退出：

```powershell
python .\cmd\bots\ptbecho\echo.py `
  --send-only `
  --send-chat-id -1000000000002 `
  --send-text "hello from python-telegram-bot"
```

长轮询 echo 启动后立即主动发送一条消息：

```powershell
python .\cmd\bots\ptbecho\echo.py `
  --send-chat-id -1000000000002 `
  --send-text "ptbecho is online"
```

发送 reply keyboard 与 inline callback 两条验证消息并保持 polling：

```powershell
python .\cmd\bots\ptbecho\echo.py `
  --buttons-chat-id 1780243200
```

reply keyboard 与 inline keyboard 都会各显示蓝/绿/红三种语义色；点击 reply
button 会按普通文本消息进入 echo 链，点击 inline button 会由 `callback_query` handler 调用
`answerCallbackQuery` 并显示 `telesrv inline callback OK`。也可以在私聊中发送
`/buttons` 生成同样的两条消息。

可选参数：

- `--send-count N`：连续主动发送 N 条。
- `--send-interval SEC`：连续发送之间的间隔。
- `TELESRV_BOT_DEMO_CHAT_ID` / `TELESRV_BOT_DEMO_SEND_TEXT`：主动发送参数的环境变量形式。
- `--buttons-chat-id` / `TELESRV_BOT_DEMO_BUTTONS_CHAT_ID`：发送两类键盘验证消息并监听 callback。

本地超级群 chat id 使用 Bot API 形式 `-100<channel_id>`；例如 channel id 为
`2` 时是 `-1000000000002`。

Implemented telesrv Bot API surface for this demo: `getMe`, `getUpdates`,
`deleteWebhook`, `sendMessage`, and file URL configuration. The wider gateway
also has basic `sendPhoto`, `sendDocument`, `editMessageText`, `deleteMessage`,
`answerCallbackQuery`, `getFile`, and `/file/bot...` support.
