# gramsrv - 开源 Telegram Server / MTProto Server Go 实现

`gramsrv` 是一个用 Go 编写的开源 Telegram server 实现和 MTProto server。
它是一个 Telegram-like backend，面向真实客户端兼容、自建聊天实验、协议研究，
以及一条长期可演进的社区 server 路线。

如果你正在搜索 **Telegram server 实现**、**MTProto server 实现**、
**Telegram 后端**、**Telegram clone server**、**自建 Telegram-like 聊天服务器**，
这个仓库就是可以运行、研究和共同优化的 server 侧实现。

[English README](README.md) · [官网](https://telesrv.net) · [讨论群](https://t.me/telesrv_chat) · [频道](https://t.me/telesrv)

`gramsrv` 是独立的非官方项目，与 Telegram 官方及其团队没有关联，也未获得其背书或赞助。

## 搜索关键词

`telegram server` · `telegram server implementation` · `mtproto server` ·
`mtproto server in go` · `telegram backend` · `telegram-like server` ·
`self-hosted telegram` · `telegram desktop compatible server` ·
`android telegram compatible server` · `open source chat server` ·
`telegram server 实现` · `mtproto server 实现` · `自建 Telegram server`

## Demo Video

https://github.com/user-attachments/assets/25e651dc-a022-4d60-8b9b-ca3e8bfe216c

## 项目特性

| 状态 | 特性 | 说明 |
|---|---|---|
| ✅ | 一个程序直接启动 | 一个 Go 二进制完成 RSA key、数据库迁移、内置数据导入、MTProto 监听、RPC handlers、updates 分发和后台 worker。 |
| ✅ | 所有 server 功能开源 | 协议接入、业务服务、存储层、兼容 handlers、媒体链路、updates、管理后台和实验模块都在本仓库。 |

## 功能清单

下面这些是开源代码里已经实现的 server 侧功能。

| 状态 | 功能 | 当前已实现 |
|---|---|---|
| ✅ | MTProto server 接入层 | TCP transport、RSA key exchange、auth key、加密 session、salt、ack/resend、bad message、RPC dispatch、layer 兼容辅助。 |
| ✅ | 登录与账号 | 开发验证码登录、sign-in、sign-up、log-out、授权设备、账号设置、SRP/password 状态、email/passkey 相关路径。 |
| ✅ | 用户与联系人 | 用户资料、username、头像、联系人导入/搜索、block/privacy 状态、presence、last seen。 |
| ✅ | 会话与同步 | dialog list、置顶、手动未读、folders/filters、草稿、read boundary、durable updates、在线 fan-out、离线 difference 恢复。 |
| ✅ | 私聊消息 | send、history、read receipts、edit、delete、forward、reply、富文本实体、媒体/相册消息、reactions、scheduled/TTL 相关路径。 |
| ✅ | 富文本消息 | Telegram Desktop rich text message、富文本内容转换、send/edit/scheduled 流程、dialog/history 投影，以及 memory/PostgreSQL 持久化。 |
| ✅ | AI 输入框与 ChatBot | 输入框改写/润色、默认和自定义 tone、addstyle 预览、本地与外部 provider 链、流式 `@ChatBot` 草稿回复、Business AI 回复钩子。 |
| ✅ | 超级群与频道 | create、join、leave、邀请链接、成员、管理员、forum topics、history、send/edit/delete/read、reactions、公开搜索和预览。 |
| ✅ | 媒体与文件 | upload、download、本地 blob 存储、照片、文档、缩略图、外链媒体抓取、网页预览、地图缩略图缓存、用户/频道头像。 |
| ✅ | Stickers 与 Reactions | sticker/reaction catalog、seed 支持、recent reactions、top reactions、default reactions、reaction moderation 相关路径。 |
| ✅ | Gifts 与 Stars | star gifts、本地 stars ledger 基础，用于兼容和后续功能扩展。 |
| ✅ | Bots 与 Mini Apps | bot 服务基础、callbacks、inline helpers、webview/mini-app 路径、最小 Bot API gateway、demo 工具。 |
| ✅ | 通话与实时能力 | 私聊通话信令基础、group call 状态、SFU/TURN building blocks、liveness 与 expiry worker。 |
| ✅ | 管理与运维 | Admin API/UI backend、PostgreSQL migrations、Redis 易失态、retention workers、pprof/debug hooks、load-test helpers。 |
| ✅ | Desktop、Android 与 Web 兼容 | Telegram Desktop 是第一目标，Android 与 Web 兼容路径也由同一套 server 持续覆盖。 |

其中一部分能力仍是兼容优先或实验性质，但它们都是真实开放的 server 代码，不是隐藏的产品版功能。下一步希望大家一起把这些路径打磨得更稳、更快、更好用。

## 快速启动

依赖：

- Go 1.25 或更新版本
- Docker Desktop 或带 Compose 的 Docker Engine
- OpenSSL，如果要编译匹配的 Telegram Desktop 客户端

启动 PostgreSQL 和 Redis：

```powershell
docker compose -f deploy/docker-compose.yml up -d
```

编译并启动唯一的 server 程序：

```powershell
go build -o bin/gramsrv.exe ./cmd/telesrv
.\bin\gramsrv.exe
```

第一次启动时，`gramsrv` 会创建 `data/server_rsa.pem`，自动执行数据库 migrations，导入内置语言包，准备可选媒体资源，在 `0.0.0.0:2398` 监听 MTProto，并在同一进程里启动 updates、media、后台调度等 worker。

常用本地环境变量：

| 变量 | 默认值 | 说明 |
|---|---:|---|
| `TELESRV_LISTEN` | `0.0.0.0:2398` | MTProto 监听地址 |
| `TELESRV_ADVERTISE_IP` | `127.0.0.1` | 下发给兼容客户端的连接 IP |
| `TELESRV_DC` | `2` | 自建 DC id |
| `TELESRV_DEV_AUTH_CODE` | `12345` | 本地开发固定登录验证码 |
| `TELESRV_POSTGRES_DSN` | local Compose DSN | PostgreSQL 连接串 |
| `TELESRV_REDIS_ADDR` | `127.0.0.1:6399` | Redis 地址 |
| `TELESRV_LANGPACK_SEED_DIR` | `data/langpack` | 内置语言包种子目录 |
| `TELESRV_BLOB_DIR` | `data/blobs` | 本地媒体 blob 目录 |
| `TELESRV_STICKER_SEED_DIR` | `data/sticker-seed` | 可选 sticker/reaction 种子目录 |
| `TELESRV_AI_ENABLED` | `true` | 启用 AI compose 入口 |
| `TELESRV_AI_PROVIDERS` | `local` | AI provider 调用链，例如 `local` 或 `kimi,local` |
| `TELESRV_AI_TIMEOUT` | `15s` | 单次 AI provider 调用超时 |
| `TELESRV_AI_RATE_LIMIT` | `20` | 每个账号的 AI compose 请求额度 |
| `TELESRV_AI_RATE_WINDOW` | `1m` | AI compose 限流窗口 |
| `TELESRV_AI_LOG_CONTENT` | `false` | 日志是否允许记录 prompt/生成文本 |
| `TELESRV_BUSINESS_AI_PROVIDER` | `echo` | Business automation 回复 provider |

如果 sticker seed 目录不存在，启动时会自动跳过。
可选的 OpenAI-compatible、Kimi/Moonshot、Gemini、Anthropic provider 变量见 `.env.example`。

## 客户端兼容

官方 Telegram 客户端不能直接连接 `gramsrv`，因为它们信任的是 Telegram 官方 DC 列表和 RSA keys。你可以使用 [官网](https://telesrv.net) 提供的体验客户端，也可以自己做最小协议 patch。

当前 Telegram Desktop 基线：

- Telegram Desktop commit：`9caf32dffc90ddd9bb08ad5777b865f729fa167b`
- TL layer：227
- 本地 DC：`127.0.0.1:2398`，DC id `2`

等 `gramsrv` 生成 `data/server_rsa.pem` 后，导出匹配的公钥：

```powershell
openssl rsa -in data/server_rsa.pem -RSAPublicKey_out -out data/server_rsa.pub
```

修改 `Telegram/SourceFiles/mtproto/mtproto_dc_options.cpp`：

1. 把内置 production/test DC 列表替换为你的 `gramsrv` endpoint。
2. 把 `kPublicRSAKeys` 和 `kTestPublicRSAKeys` 都替换为 `data/server_rsa.pub`。
3. 给 built-in DC flags 加上 `Flag::f_tcpo_only`。

客户端 patch 应保持最小：只改 endpoint、RSA key 和 TCP-only flags，不要把 UI 改动混入协议兼容 patch。

## 多端冒烟验证

用不同的 TDesktop working directory，避免 Alice 和 Bob 共用同一个 `tdata`：

```powershell
$tdesktop = "C:\path\to\tdesktop\out\Debug\Telegram.exe"
Start-Process $tdesktop -ArgumentList @("-workdir", "$PWD\.tdata-alice")
Start-Process $tdesktop -ArgumentList @("-workdir", "$PWD\.tdata-bob")
```

用两个不同手机号登录。本地开发默认验证码是 `12345`，除非你修改了 `TELESRV_DEV_AUTH_CODE`。

推荐检查：

- 两个用户之间发送私聊消息、sticker、媒体、reply、forward、edit、delete 和 read receipts。
- 一个设备保持在线，另一个设备重启，验证离线 `updates.getDifference` 恢复。
- 同一账号多 session 登录，确认当前 session 不重复 echo，其它在线 session 能收到 updates。
- 检查 server 日志没有新增 `NOT_IMPLEMENTED`、`Unhandled RPC`、`bad_msg`、panic 或 internal error。

## 贡献者

- [ajarshia](https://github.com/ajarshia) - Android Persian (`fa`) 语言包。

## 仓库结构

```text
cmd/telesrv/              server 启动入口
cmd/telesrv-admin/        管理后台 backend 与 web UI
deploy/                   docker-compose、migrations、部署辅助
data/                     内置语言包与可选种子数据
internal/mtprotoedge/     MTProto transport、auth key、session、ack/resend
internal/rpc/             TL router 与客户端兼容 handlers
internal/app/             domain services
internal/domain/          不依赖协议生成类型的 domain models
internal/store/           memory/postgres/redis 存储后端
internal/seed/            内置 seed catalog 加载器
internal/sfu/             SFU 实验模块
internal/turnsrv/         TURN/STUN building blocks
```

## 一起优化

`gramsrv` 非常欢迎大家一起跑、一起测、一起拆问题、一起优化。尤其欢迎这些贡献：

- Telegram Desktop 和 Android 兼容性报告，最好带可复现步骤。
- 启动、同步、聊天、媒体、通话、bots 或边界场景的 RPC trace。
- 围绕已实现路径的小而准的 bug fix。
- 在线/离线 updates、多端 session、read state、媒体、频道行为的测试。
- fan-out、分页、存储查询、媒体上传/下载、连接层等热点路径的性能优化。
- 让“一个程序直接启动”的本地体验更顺滑的改进。

如果改动会影响客户端可见行为，请说明客户端版本/commit、验证过的 RPC 路径，以及 server 日志是否没有新增 `NOT_IMPLEMENTED`、`Unhandled RPC`、`bad_msg`、panic 或 internal error。

## 授权协议

`gramsrv` 使用 [Apache License 2.0](LICENSE) 发布。你可以在 Apache-2.0 条款下使用、修改、分发，也可以商用。

## 付费定制开发

如需付费定制开发功能，可以通过讨论群或官网联系作者。定制范围不限于某一端，可覆盖 server 功能、Telegram Desktop、Android、Web、部署、兼容适配，或围绕本项目的其它客户端/服务端路径。
