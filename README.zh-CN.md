# gramsrv

`gramsrv` 是一个用 Go 编写的 Telegram-like MTProto server，以
[`github.com/gotd/td`](https://github.com/gotd/td) v0.144.0 / Layer 225 作为
TL 与 MTProto 基础，第一兼容目标是固定基线的 Telegram Desktop。

`gramsrv` 是独立的非官方项目，与 Telegram 官方及其团队没有关联，也未获得其背书或赞助。

关键词：`MTProto`、`Telegram Desktop`、`gotd/td`、`Telegram-like server`、`Go`、
`self-hosted chat server`。

[English README](README.md)

![Telegram Desktop Alice/Bob connected to gramsrv](docs/assets/tdesktop-dual-session.png)

## 当前状态

本项目适合本地协议研究、Telegram Desktop 兼容性验证和自建 MTProto server 实验。它不是生产级 Telegram 替代品。

当前已覆盖的主路径包括 MTProto key exchange、开发验证码登录、users/contacts/dialogs、私聊消息、超级群/频道、updates difference 恢复、本地 media/files、用户/频道头像、stickers、reactions 和 presence。

暂不默认覆盖大规模公开频道、多 DC / 文件 DC / CDN、Bot API、payments、stories、Premium 商业逻辑、生产风控、生产对象存储等能力。

## 欢迎贡献

欢迎大家参与贡献。现在最有价值的方向包括 Telegram Desktop 兼容性报告、可复现 RPC trace、聚焦的小 bug fix、在线/离线 updates 行为测试、已实现路径的性能优化，以及让本地启动更顺滑的文档改进。

请尽量保持改动范围清晰，并围绕兼容性目标展开。如果改动会影响 Telegram Desktop 可见行为，请在 PR 或说明里写清客户端版本/commit、验证过的 RPC 路径，以及 server 日志是否没有新增 `NOT_IMPLEMENTED`、`Unhandled RPC`、`bad_msg`、panic 或 internal error。

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

## 运行 gramsrv

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

## 编译连接 gramsrv 的 Telegram Desktop

官方 Telegram Desktop 二进制不能直接连接 `gramsrv`，因为它信任的是 Telegram 官方 DC 列表和 RSA keys。你需要编译一个最小 patch 过的客户端。

目标基线：

- Telegram Desktop commit：`9caf32dffc90ddd9bb08ad5777b865f729fa167b`
- TL layer：225
- 本地 DC：`127.0.0.1:2398`，DC id `2`

克隆并固定 Telegram Desktop：

```powershell
git clone --recursive https://github.com/telegramdesktop/tdesktop.git
cd tdesktop
git checkout 9caf32dffc90ddd9bb08ad5777b865f729fa167b
git submodule update --init --recursive
```

编译依赖和各平台完整说明以 Telegram Desktop 上游文档为准：

- Windows：`docs/building-win.md`
- macOS：`docs/building-mac.md`
- Linux：`docs/building-linux.md`

Windows x64 下，固定基线的主要步骤是：

```powershell
Telegram\build\prepare\win.bat
cd Telegram
configure.bat x64 -D TDESKTOP_API_ID=YOUR_API_ID -D TDESKTOP_API_HASH=YOUR_API_HASH
```

然后用 Visual Studio 打开 `out\Telegram.slnx`，构建 `Telegram` project。Debug 二进制会生成在 `out\Debug\Telegram.exe`。

## Patch Telegram Desktop

等 `gramsrv` 生成 `data/server_rsa.pem` 后，导出匹配的公钥：

```powershell
openssl rsa -in data/server_rsa.pem -RSAPublicKey_out -out data/server_rsa.pub
```

修改 Telegram Desktop 文件：

```text
Telegram/SourceFiles/mtproto/mtproto_dc_options.cpp
```

1. 把内置 production/test DC 列表替换为本地 DC 2：

```cpp
const BuiltInDc kBuiltInDcs[] = {
    { 2, "127.0.0.1", 2398 },
};

const BuiltInDc kBuiltInDcsIPv6[] = {
    { 2, "::1", 2398 },
};

const BuiltInDc kBuiltInDcsTest[] = {
    { 2, "127.0.0.1", 2398 },
};

const BuiltInDc kBuiltInDcsIPv6Test[] = {
    { 2, "::1", 2398 },
};
```

2. 把 `kPublicRSAKeys` 和 `kTestPublicRSAKeys` 都替换为 `data/server_rsa.pub` 的内容。
3. 在 `DcOptions::constructFromBuiltIn()` 中给 IPv4 与 IPv6 built-in DC flags 加上 `Flag::f_tcpo_only`。

```cpp
const auto flags = Flag::f_static | Flag::f_tcpo_only;
const auto flags = Flag::f_static | Flag::f_ipv6 | Flag::f_tcpo_only;
```

客户端 patch 应保持最小：只改 DC endpoint、RSA public key 和 TCP-only flags，不要把 UI 改动混入协议兼容 patch。

## 启动两个本地 Desktop 客户端

用不同的 TDesktop working directory，避免 Alice 和 Bob 共用同一个 `tdata`：

```powershell
$tdesktop = "C:\path\to\tdesktop\out\Debug\Telegram.exe"
Start-Process $tdesktop -ArgumentList @("-workdir", "$PWD\.tdata-alice")
Start-Process $tdesktop -ArgumentList @("-workdir", "$PWD\.tdata-bob")
```

用两个不同手机号登录。本地开发默认验证码是 `12345`，除非你修改了 `TELESRV_DEV_AUTH_CODE`。

如果客户端一直重连，优先检查：

- `gramsrv` 是否正在监听 `2398`。
- `data/server_rsa.pub` 是否同时复制到了 TDesktop 的两个 RSA key 数组。
- `TELESRV_ADVERTISE_IP` 是否是客户端可访问的地址。
- TDesktop 是否基于固定 Layer 225 基线构建，或者你已经重新审计了新 layer。

## 文档

- [兼容矩阵](docs/compatibility-matrix.md)
- [Telegram Desktop patch notes](docs/tdesktop-patch-notes.md)
- [持久化层设计](docs/persistence-layer.md)
- [消息模块](docs/message-module.md)
- [频道模块](docs/channel-module.md)
- [性能审计](docs/performance-audit.md)
