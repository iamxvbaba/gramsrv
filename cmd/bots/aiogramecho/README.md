# aiogram 3 echo demo

该示例使用标准 aiogram 3 API，仅把 API server 指向 telesrv。aiogram 的
`TelegramAPIServer.from_base()` 会自动拼出 telesrv 已支持的
`/bot<TOKEN>/<method>` 与 `/file/bot<TOKEN>/<path>`。

```powershell
python -m pip install -r .\cmd\bots\aiogramecho\requirements.txt
$env:TELESRV_BOT_TOKEN = "<bot_id>:<secret>"
python .\cmd\bots\aiogramecho\echo.py --drop-pending
```

发送三种语义色的 reply keyboard 与 inline callback 按钮：

```powershell
python .\cmd\bots\aiogramecho\echo.py `
  --buttons-chat-id 1780243200 `
  --drop-pending
```

可选的 `--button-icon-id <custom_emoji_document_id>` 同时验证按钮自定义 emoji
图标。Telegram 官方会按 bot owner Premium / Fragment 权限限制图标使用；颜色只接受
`primary`（蓝）、`success`（绿）、`danger`（红），不接受任意 RGB。

只主动发消息、不启动轮询：

```powershell
python .\cmd\bots\aiogramecho\echo.py `
  --send-only `
  --send-chat-id 1780243200 `
  --send-text "hello from aiogram"
```

默认 API server 是 `http://127.0.0.1:8081`，可用 `--base-url` 或
`TELESRV_BOT_API_SERVER` 覆盖。不要在这里追加 `/bot`；这与 ptbecho 的
`--base-url http://127.0.0.1:8081/bot` 参数格式不同。

轮询模式会回答本示例的 `aiogram-*` callback，也会对同一测试 bot 先前由
其它 demo 创建的 inline callback 给出兜底确认，避免 TDesktop 按钮一直转圈。

## Webhook 模式

telesrv 现在会持久化 webhook 配置，通过跨实例租约投递，并且只在目标返回 2xx
后推进 `update_id`。aiogram 可监听本机 HTTP，由 Caddy/Nginx/Tunnel 提供公网 HTTPS：

```powershell
$env:TELESRV_BOT_WEBHOOK_URL = "https://bot.example.com/webhook"
$env:TELESRV_BOT_WEBHOOK_SECRET = "replace_with_a_random_secret"
python .\cmd\bots\aiogramecho\echo.py `
  --mode webhook `
  --listen-host 127.0.0.1 `
  --listen-port 8082 `
  --webhook-path /webhook `
  --drop-pending
```

公网 URL 必须是 HTTPS，端口限 Telegram 标准的 443/80/88/8443；本机监听地址
可以是 HTTP，因为 TLS 通常在反向代理终止。`secret_token` 会由 telesrv 放入
`X-Telegram-Bot-Api-Secret-Token`，aiogram 会自动校验。若希望进程退出时删除配置，
再加 `--delete-webhook-on-exit`；默认保留配置，以免普通重启造成更新丢窗。

同一个 token 的 polling 与 webhook 互斥；切回轮询时直接以默认模式启动，示例会先
调用 `deleteWebhook`，再开始 `getUpdates`。
