# Bedolaga formatted-text + Telegram Login demo

这个 demo 复刻 Bedolaga 的 Bot 工厂关键配置：

```python
Bot(
    ...,
    default=DefaultBotProperties(parse_mode=ParseMode.HTML),
)
```

因此 `/start` 的 `message.answer()` 不显式传 `parse_mode`，仍会由 aiogram 自动向
telesrv 发送 `parse_mode=HTML`。`/formatdemo` 依次发送默认 HTML、legacy Markdown、
MarkdownV2，用于验证完整的 `aiogram → telesrv Bot API → MTProto message/update →
TDesktop` 链路。

`/richdemo` 进一步复刻 Bedolaga 的 rich menu：调用 `sendRichMessage` 发送 HTML 与
Markdown `InputRichMessage`，携带 inline callback keyboard，再通过
`editMessageText.rich_message` 编辑 HTML 菜单。HTML 样例覆盖 heading、divider、
bordered/striped table、`tg-time`、details、blockquote、code 与 footer。第一次请求
故意带远程 logo；当前本地 blob backend 返回 `WEBPAGE_MEDIA_EMPTY` 后，demo 按
Bedolaga 的既有策略自动去掉 logo 重试，正文与按钮不会降级成 classic menu。

## 安装

建议使用虚拟环境，token 只通过环境变量传入：

```powershell
python -m venv "$env:TEMP\telesrv-bedolaga-demo-venv"
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" -m pip install `
  -r .\cmd\bots\bedolagaformat\requirements.txt

$env:TELESRV_BOT_TOKEN = "<bot_id>:<secret>"
$env:TELESRV_BOT_API_SERVER = "http://127.0.0.1:8081"
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\demo.py --drop-pending
```

随后在 TDesktop 中向 bot 发送：

```text
/start
/formatdemo
/richdemo
```

也可以不启动 polling，直接向指定私聊发送三条格式测试消息：

```powershell
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\demo.py `
  --send-only `
  --send-chat-id 1780243200 `
  --marker BEDOLAGA-LOCAL-VERIFY
```

只主动验证 rich menu（HTML + Markdown + 按钮 + 编辑 + logo fallback）：

```powershell
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\demo.py `
  --send-only `
  --rich-only `
  --send-chat-id 1780243200 `
  --marker BEDOLAGA-RICH-VERIFY
```

`--base-url` 只接受 API server 根地址，不要追加 `/bot`。脚本不会打印 token，也不会
把 token 写入文件。

## Telegram Login 全链路

同一个 demo 还提供 `/logindemo`，覆盖三个互相独立的公开契约：

1. Bot API `login_url` 按钮 → TDesktop/Android 的
   `messages.requestUrlAuth/acceptUrlAuth` → legacy HMAC 回调；
2. telesrv 本地 `/telegram-login.js` → popup `postMessage` → JWKS 验签；
3. 服务端 Authorization Code + PKCE S256 → `/token` Basic Client Secret →
   JWKS 验签和 `issuer/audience/nonce/subject` 复核。

先在 telesrv 的 @BotFather 中对目标 bot 运行 `/setlogin`。选择一次 bot 后，可逐条发送，
也可把下面三行作为一条多行消息粘贴，无需每次重新运行 `/setlogin` 或重选 bot：

```text
add origin http://127.0.0.1:3000
add redirect http://127.0.0.1:3000/oauth/callback
enable
```

发送 `/done` 退出配置会话并查看最终摘要。`/cancel` 只退出，不回滚已经成功应用的命令。

`/setlogin` 首次创建 client 时只展示一次 OIDC Client Secret；不要写进仓库。可用
`/logininfo` 查看 Client ID 和登记结果，或用 `/resetloginsecret` 轮换 secret。
使用 HTTP 域名/IP 时，在 telesrv 配置 `TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP=true`；
demo 会接受任意合法 HTTP(S) issuer/public origin，不再限制为 loopback。

把一次性 secret 和 Client ID 放入进程环境，再启动：

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

向 bot 发送 `/logindemo`。第一颗按钮必须出现 Telegram 客户端原生授权确认框，批准
后网页显示 `login_url HMAC verified`；第二颗按钮打开测试页，可分别运行 JS SDK popup
和 Authorization Code + PKCE。页面只展示验签后的 claims，不展示 access token 或
Client Secret。省略 `TELESRV_BOT_LOGIN_CLIENT_SECRET` 时仍可验证 JS popup，但服务端
code flow 会明确禁用。

移动 Chrome 会在跳入 Telegram/DrKLO 前让 popup 把短期 browser token 交给原 RP
标签；父标签随后以精确登记 origin 轮询 `/auth/status`。因此外部 app round-trip
关闭 popup 或丢失 `window.opener` 时，原标签仍能完成 JWKS 验签。不要给
`/auth/status` 配 wildcard CORS，也不要在 RP 中记录 browser token 或 ID token。

demo 的 flow/state/nonce 只保存在单进程内存中，带 10 分钟过期和 256 条上限，专用于
本地与受控测试部署端到端验证，不是生产 relying-party 实现。官方 iOS/Android SDK
目前把 `https://oauth.telegram.org` 写死；验证自建 issuer 时需使用项目记录的最小
base-URL patch 或等价测试构建，不能把官方生产 SDK 未修改的结果误判为自建服务结果。

测试命令：

```powershell
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\test_demo.py -v
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\test_login_demo.py -v
```
