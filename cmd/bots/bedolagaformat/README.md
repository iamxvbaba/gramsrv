# Bedolaga formatted-text demo

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
