# aiogram 3 echo demo

该示例使用标准 aiogram 3 API，仅把 API server 指向 telesrv。aiogram 的
`TelegramAPIServer.from_base()` 会自动拼出 telesrv 已支持的
`/bot<TOKEN>/<method>` 与 `/file/bot<TOKEN>/<path>`。

```powershell
python -m pip install -r .\cmd\bots\aiogramecho\requirements.txt
$env:TELESRV_BOT_TOKEN = "<bot_id>:<secret>"
python .\cmd\bots\aiogramecho\echo.py --drop-pending
```

## Ephemeral echo（Bot API 10.2）

先通过 `setMyCommands` 把 `private` 注册为 `is_ephemeral=true`，再在 TDesktop Layer 228
的群组中发送：

```text
/private@你的Bot用户名
```

aiogram 3.30.0 原生解析 `ephemeral_message_id`。示例在 15 秒 action 窗口内携带
`receiver_user_id` 和 `ReplyParameters(ephemeral_message_id=...)` 回复
`ephemeral echo: ...`；失败不会降级成普通消息。Alice 应看到两条带
可见性提示的消息：自己发出的命令显示“Only visible to @Bot”，bot 回复显示
“Only visible to you”；Bob 不应看到其中任何一条。

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
后推进 `update_id`。aiogram 可直接登记 HTTP/HTTPS 域名或 IP，也可以由 Caddy/Nginx/Tunnel 提供公网 HTTPS：

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

Webhook URL 可使用任意合法 HTTP/HTTPS 域名或 IP 及 `1..65535` 端口；本机监听地址
也可以直接使用 HTTP。`secret_token` 会由 telesrv 放入
`X-Telegram-Bot-Api-Secret-Token`，aiogram 会自动校验。若希望进程退出时删除配置，
再加 `--delete-webhook-on-exit`；默认保留配置，以免普通重启造成更新丢窗。

同一个 token 的 polling 与 webhook 互斥；切回轮询时直接以默认模式启动，示例会先
调用 `deleteWebhook`，再开始 `getUpdates`。
