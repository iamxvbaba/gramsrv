# gramsrv

`gramsrv` 是一个用 Go 编写的 Telegram-like MTProto server，重点放在真实客户端兼容、可复现协议研究、自建聊天服务实验，以及对 Telegram 客户端新版本行为的持续追踪。

[官网](https://telesrv.net) · [讨论群组](https://t.me/telesrv_chat) · [频道](https://t.me/telesrv) · [English README](README.md)

`gramsrv` 是独立的非官方项目，与 Telegram 官方及其团队没有关联，也未获得其背书或赞助。

## 演示视频

https://github.com/user-attachments/assets/25e651dc-a022-4d60-8b9b-ca3e8bfe216c

## 亮点

- **一个 server 程序即可运行。** PostgreSQL 与 Redis 准备好后，Go server 进程会编排 RSA key 准备、数据库 migration、语言包 seed、MTProto edge、RPC router、updates、media/files 和可靠投递 worker。
- **多设备已实现。** Telegram Desktop 与 Android 客户端可以使用同一套服务端状态，支持 scoped session、在线 fan-out、当前 session 排除，以及通过 updates difference API 做离线恢复。
- **持续维护并追踪 Telegram 新版本。** 公开基线保持可复现，同时通过真实客户端 trace、兼容记录和后续适配任务持续跟踪 Telegram Desktop 与 Android 新版本行为。
- **热路径优化是设计的一部分。** 可靠 outbox 批量投递、PostgreSQL 连接池预热、scoped session 查询、有界 RPC 参数和 seek 分页，减少聊天、同步与媒体路径上的重复工作。
- **Telegram Desktop 是第一兼容目标。** 当前公开版本围绕固定 TDesktop 基线推进，并把兼容性进展写入文档。
- **Android 兼容正在推进。** 当前公开截图已经包含连接到同一服务端路径的 Android 客户端。
- **核心聊天主路径可用。** 已覆盖登录、users、contacts、dialogs、私聊消息、超级群/频道、media/files、用户/频道头像、stickers、reactions、语言包与 presence。
- **生产边界明确。** 大规模公开频道、多 DC / 文件 DC / CDN、Bot API、payments、stories、Premium 商业逻辑、生产风控和生产对象存储不属于当前公开版本范围。

下载、公开说明和当前体验入口见 [telesrv.net](https://telesrv.net)。问题交流、兼容反馈和开发讨论可以加入 [t.me/telesrv_chat](https://t.me/telesrv_chat)。

## 持续维护与版本追踪

`gramsrv` 不会把某一个固定客户端版本当成终点。固定 Telegram Desktop 基线用于保证回归可复现；新的 Telegram Desktop 与 Android 版本会通过真实客户端启动/同步 trace、兼容矩阵更新和聚焦适配任务持续跟进。

当新的 Telegram 客户端路径出现时，推荐流程是先记录调用，约束输入边界，再决定实现、stub 或标记为范围外，并保持仓库文档与实际行为一致。

## 仓库结构

```text
cmd/telesrv/              server 启动入口
deploy/                   docker-compose 与 PostgreSQL migrations
internal/mtprotoedge/     MTProto transport、auth key、session、ack/resend
internal/rpc/             TL router 与 Telegram Desktop 兼容 handlers
internal/app/             domain services
internal/domain/          不依赖协议生成类型的 domain models
internal/store/           store interfaces 与 memory/postgres/redis 后端
docs/                     兼容性记录与模块设计文档
```

## 快速启动

依赖：

- Go 1.25 或更新版本
- Docker Desktop 或带 Compose 的 Docker Engine
- OpenSSL，如果要编译匹配的 Telegram Desktop 客户端

启动 PostgreSQL 和 Redis：

```powershell
docker compose -f deploy/docker-compose.yml up -d
```

编译并启动 server：

```powershell
go build -o bin/gramsrv.exe ./cmd/telesrv
.\bin\gramsrv.exe
```

第一次启动时，`gramsrv` 会创建 `data/server_rsa.pem`，自动执行所有数据库 migrations，导入内置语言包，并监听 `0.0.0.0:2398`。

常用开发环境变量：

| 变量 | 默认值 | 说明 |
|---|---:|---|
| `TELESRV_LISTEN` | `0.0.0.0:2398` | MTProto 监听地址 |
| `TELESRV_ADVERTISE_IP` | `127.0.0.1` | 写入 `help.getConfig` 的客户端连接 IP |
| `TELESRV_DC` | `2` | 自建 DC id |
| `TELESRV_DEV_AUTH_CODE` | `12345` | 本地开发固定登录验证码 |
| `TELESRV_POSTGRES_DSN` | local Compose DSN | PostgreSQL 连接串 |
| `TELESRV_REDIS_ADDR` | `localhost:6399` | Redis 地址 |
| `TELESRV_STICKER_SEED_DIR` | `data/sticker-seed` | 可选的 sticker/reaction 导出种子目录 |

如果 sticker seed 目录不存在，启动时会自动跳过。

## 客户端兼容

官方 Telegram 客户端不能直接连接 `gramsrv`，因为它们信任的是 Telegram 官方 DC 列表和 RSA keys。你可以从 [官网](https://telesrv.net) 获取体验客户端，或者自己编译带最小协议 patch 的客户端。

当前 Telegram Desktop 基线：

- Telegram Desktop commit：`9caf32dffc90ddd9bb08ad5777b865f729fa167b`
- TL layer：225
- 本地 DC：`127.0.0.1:2398`，DC id `2`

等 `gramsrv` 生成 `data/server_rsa.pem` 后，导出匹配的公钥：

```powershell
openssl rsa -in data/server_rsa.pem -RSAPublicKey_out -out data/server_rsa.pub
```

修改 `Telegram/SourceFiles/mtproto/mtproto_dc_options.cpp`：

1. 把内置 production/test DC 列表替换为你的 `gramsrv` endpoint。
2. 把 `kPublicRSAKeys` 和 `kTestPublicRSAKeys` 都替换为 `data/server_rsa.pub`。
3. 给 built-in DC flags 加上 `Flag::f_tcpo_only`。

客户端 patch 应保持最小：只改 endpoint、RSA key 和 TCP-only flags。

## 多设备冒烟验证

使用不同的客户端工作目录，避免多个 session 共用本地 `tdata`：

```powershell
$tdesktop = "C:\path\to\tdesktop\out\Debug\Telegram.exe"
Start-Process $tdesktop -ArgumentList @("-workdir", "$PWD\.tdata-alice")
Start-Process $tdesktop -ArgumentList @("-workdir", "$PWD\.tdata-bob")
```

用不同手机号登录。本地开发默认验证码是 `12345`，除非你修改了 `TELESRV_DEV_AUTH_CODE`。

推荐检查：

- 在两个用户之间发送私聊消息、贴纸、媒体、回复、转发、编辑、删除和已读回执。
- 保持一个设备在线，重启另一个设备，验证离线 `updates.getDifference` 恢复。
- 同一账号打开多个 session，确认当前 session 不重复收到 echo，其它在线 session 能收到更新。
- 检查 server 日志没有新增 `NOT_IMPLEMENTED`、`Unhandled RPC`、`bad_msg`、panic 或 internal error。

## 文档

- [兼容矩阵](docs/compatibility-matrix.md)
- [Telegram Desktop patch notes](docs/tdesktop-patch-notes.md)
- [持久化层设计](docs/persistence-layer.md)
- [消息模块](docs/message-module.md)
- [频道模块](docs/channel-module.md)
- [性能审计](docs/performance-audit.md)

## 参与贡献

欢迎围绕兼容性目标参与贡献。现在最有价值的方向包括 Telegram Desktop / Android 兼容反馈、新 Telegram 客户端版本反馈、可复现 RPC trace、聚焦的小 bug fix、多设备 update 测试、已实现路径的性能优化，以及让本地启动更顺滑的文档改进。

如果改动会影响客户端可见行为，请写清客户端版本/commit、验证过的 RPC 路径，以及 server 日志结果。
