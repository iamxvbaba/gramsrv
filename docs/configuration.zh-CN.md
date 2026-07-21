# telesrv 配置参数手册

英文版：[configuration.en.md](configuration.en.md)

本文覆盖 `internal/config` 实际读取的全部配置。默认值和校验行为以 `internal/config/config.go` 为权威来源。所有配置修改都需要重启进程；telesrv 当前不支持配置热加载。

## 1. 加载方式、语法与优先级

- `TELESRV_CONFIG` 是选择 env 风格配置文件的**进程环境变量**。默认读取进程工作目录下的 `.env`；显式设为空可关闭文件加载。把它写在配置文件内部不会改变已选定的文件。
- 优先级为：非空进程环境变量 → 非空文件值 → 代码默认值。四个可空监听项 `TELESRV_DEBUG_ADDR`、`TELESRV_BOT_API_ADDR`、`TELESRV_ADMIN_API_ADDR`、`TELESRV_PUBLIC_LINK_WEB_ADDR` 允许用显式空的进程环境变量覆盖文件中的非空值，从而关闭监听。
- 文件支持空行、整行 `#` 注释、可选的 `export ` 前缀和 `KEY=VALUE`；支持单引号、双引号。行尾 `#` 不会被当作内联注释剥离。
- 文件中的键必须以 `TELESRV_` 开头，且只能包含大写 ASCII 字母、数字和下划线。语法合法但当前二进制未知的 `TELESRV_*` 键会被接受但忽略。
- bool 接受 `1/true/TRUE/True/yes/on` 和 `0/false/FALSE/False/no/off`；列表使用逗号分隔；时长使用 Go 格式，例如 `200ms`、`30s`、`5m`、`168h`。
- int、float、bool、duration 的非法文本会回退代码默认值；URL、app scheme、app name 以及登录邮箱依赖关系校验失败会阻止启动。
- 不要提交真实密码、token、私有 DSN 或 TURN secret。生产环境应使用受保护的 service environment 或密钥管理系统。

## 2. MTProto 监听、传输与资源预算

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_LISTEN` | string / `0.0.0.0:2398` | MTProto TCP 监听地址，必须与 patched 客户端可达地址/端口一致。 |
| `TELESRV_ADVERTISE_IP` | string / `127.0.0.1` | 媒体、通话等回退路径使用的客户端可达 IP；当前 TDesktop 静态 DC patch 不从这里获取 MTProto 地址。 |
| `TELESRV_RSA_KEY` | path / `data/server_rsa.pem` | MTProto RSA 私钥；缺失时自动生成。属于敏感文件，重启和升级间必须稳定保存。 |
| `TELESRV_DC` | int / `2` | 服务端 DC ID，必须与客户端 patch 及媒体/DC 元数据一致。 |
| `TELESRV_WEBSOCKET_ENABLE` | bool / `true` | 在 MTProto 监听端口启用 MTProto-over-WebSocket 分流。 |
| `TELESRV_WEBSOCKET_ALLOWED_ORIGINS` | list / `http://localhost:1234,http://127.0.0.1:1234` | 浏览器 WebSocket origin 白名单；`*` 只用于临时调试。 |
| `TELESRV_MTPROTO_MAX_CONNECTIONS` | int / `200000` | 全局物理连接 admission 上限；负数关闭该门禁。 |
| `TELESRV_MTPROTO_MAX_CONNECTIONS_PER_IP` | int / `4096` | 单来源 IP 物理连接上限；负数关闭该门禁。 |
| `TELESRV_MTPROTO_MAX_CONCURRENT_HANDSHAKES` | int / `256` | 高成本 RSA/DH 握手并发上限；负数关闭该门禁。 |
| `TELESRV_MTPROTO_RPC_MAX_INFLIGHT` | int / `32` | 单连接同时执行的 RPC 上限；非正值由 edge 归一为安全默认值。 |
| `TELESRV_MTPROTO_RPC_QUEUE_SIZE` | int / `64` | 单连接 RPC 排队容量；非正值使用 edge 默认值。 |
| `TELESRV_MTPROTO_RPC_TIMEOUT` | duration / `30s` | 调度后 RPC handler 的端到端超时。 |
| `TELESRV_MTPROTO_RPC_GLOBAL_WORKERS` | int / `256` | 共享公平调度器 worker 数。 |
| `TELESRV_MTPROTO_RPC_GLOBAL_MAX_TASKS` | int / `8192` | 进程级排队与执行中的 RPC task 上限。 |
| `TELESRV_MTPROTO_RPC_GLOBAL_MAX_BYTES` | int64 charge bytes / `536870912` | 进程级已预留/排队/执行中 RPC 内存 charge 预算；legacy 等于 copied body，exact 是 typed decode 前按 wire 与生成对象放大计算的保守 materialization charge，不代表可并发接收同等大小的 wire body。 |
| `TELESRV_MTPROTO_RPC_RESULT_CACHE_MAX_ENTRIES` | int / `262144` | 331 秒进程内重放窗口中，pending owner、completed `rpc_result` 与容量 tombstone 的全局 ownership 条目上限。owner 执行前先占 1 条，转 completed 时不重复计数。 |
| `TELESRV_MTPROTO_RPC_RESULT_CACHE_MAX_BYTES` | int64 bytes / `67108864` | 上述 ownership 的全局 retained-byte 上限；owner 先占 1 byte，Put 转移为真实 body 或 1-byte identity tombstone。不得低于 `16775168`（单条合法 outbound body 上限）。 |
| `TELESRV_MTPROTO_RPC_RESULT_CACHE_AUTH_MAX_ENTRIES` | int / `32768` | 单 raw auth key 的 ownership 条目上限；与全局、session 层同时计费，防一个 auth key 吃满进程缓存。必须 `global >= auth >= session`。 |
| `TELESRV_MTPROTO_RPC_RESULT_CACHE_AUTH_MAX_BYTES` | int64 bytes / `33554432` | 单 raw auth key retained-byte 上限；必须不低于单条合法 outbound body，且满足 byte 层级关系。 |
| `TELESRV_MTPROTO_RPC_RESULT_CACHE_SESSION_MAX_ENTRIES` | int / `16384` | 单 `raw auth key + session_id` ownership 条目上限；不同 session 不共享该局部额度。 |
| `TELESRV_MTPROTO_RPC_RESULT_CACHE_SESSION_MAX_BYTES` | int64 bytes / `16777216` | 单 `raw auth key + session_id` retained-byte 上限；默认略高于单条合法 outbound body，确保空预算时任一合法结果可完整进入。 |
| `TELESRV_MTPROTO_RPC_RESULT_PENDING_PER_AUTH` | int / `2048` | 单 raw auth key 的 active pending owner 附加上限；必须不大于 `RPC_GLOBAL_MAX_TASKS` 和 auth entry 上限。Put/Abort 都立即归还此 active 额度。 |
| `TELESRV_MTPROTO_INBOUND_FRAME_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | transport wire 与最大解密明文的进程级在途预算，在分配 payload 前预留。 |
| `TELESRV_MTPROTO_OUTBOUND_QUEUE_SIZE` | int / `128` | 单连接普通 outbound mailbox 容量。 |
| `TELESRV_MTPROTO_OUTBOUND_CONTROL_QUEUE_SIZE` | int / `32` | 单连接控制消息 mailbox 容量。 |
| `TELESRV_MTPROTO_OUTBOUND_TRACKED_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | resend pending message body 的全局预算。 |
| `TELESRV_MTPROTO_OUTBOUND_WRITE_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | 并发加密 wire/codec/obfuscation scratch 的全局预算。 |

## 3. HTTP 端点、公开链接与管理后台

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_DEBUG_ADDR` | nullable address / `127.0.0.1:6060` | pprof/debug 监听；空值关闭。生产必须保持 loopback，通过 SSH 隧道抓取。 |
| `TELESRV_BOT_API_ADDR` | nullable address / 空 | 最小 HTTP Bot API 监听；空值关闭，与 MTProto 共用 app/store 事实。`setWebhook` 接受任意合法 `http://` 或 `https://` 主机/IP 与 `1..65535` 端口。 |
| `TELESRV_ADMIN_API_ADDR` | nullable address / 空 | 进程内 Admin 写 API；空值关闭，生产应只监听 loopback。 |
| `TELESRV_ADMIN_API_TOKEN` | secret string / 空 | Admin API bearer token；启用 Admin API 时必须显式配置，并与 Admin UI 使用的 token 一致。 |
| `TELESRV_ADMIN_UI_ADDR` | address / `127.0.0.1:2600` | 独立 `cmd/telesrv-admin` 监听地址。 |
| `TELESRV_ADMIN_UI_PASSWORD` | secret string / 空 | Admin UI 登录密码；它与 `TELESRV_ADMIN_UI_TOKEN` 至少配置一个。 |
| `TELESRV_ADMIN_UI_TOKEN` | secret string / 空 | Admin UI 替代登录凭证；管理写调用仍使用独立的 `TELESRV_ADMIN_API_TOKEN`。 |
| `TELESRV_ADMIN_SESSION_KEY` | secret string / 空 | 加密/签名 Admin UI session cookie；生产至少使用 32 字节随机值，修改会使已有会话失效。 |
| `TELESRV_PUBLIC_BASE_URL` | HTTP(S) URL / `https://telesrv.net` | 客户端可见的公开链接根地址；允许 path，禁止 credentials、query、fragment。本地例：`http://127.0.0.1:2401`。 |
| `TELESRV_PUBLIC_APP_SCHEME` | URL scheme / `telesrv` | 落地页自动唤起客户端的 scheme，必须与 patched 客户端注册值一致；禁止 `tg`、`http`、`https`。 |
| `TELESRV_PUBLIC_WEB_BASE_URL` | HTTP(S) URL / `https://web.telesrv.net` | username 页面 Web 客户端入口，校验规则同 `TELESRV_PUBLIC_BASE_URL`。 |
| `TELESRV_PUBLIC_APP_NAME` | string / `telesrv` | 公开落地页产品名；trim 后非空、无控制字符、最多 64 个 Unicode 字符。 |
| `TELESRV_PUBLIC_LINK_WEB_ADDR` | nullable address / 空 | 只读 username/avatar/sticker/emoji/chatlist/collectible gift 落地页监听；空值关闭。生产应 loopback + nginx 精确反代；`.env.example` 为开发启用 `127.0.0.1:2401`。 |
| `TELESRV_TELEGRAM_LOGIN_ENABLE` | bool / `false` | 在 `TELESRV_PUBLIC_LINK_WEB_ADDR` 上挂载自建 Telegram Login/OIDC Provider；启用时必须同时配置该 listener 与下列全部密钥文件。 |
| `TELESRV_TELEGRAM_LOGIN_ISSUER` | 绝对 origin URL / `TELESRV_PUBLIC_BASE_URL` | discovery 与 token 使用的精确公开 issuer；默认必须 HTTPS，禁止 path、credentials、query、fragment。开启下一项后可直接配置任意 HTTP 域名/IP。 |
| `TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP` | bool / `false` | 开启后允许任意合法 HTTP issuer、BotFather Web origin、redirect URI 和 native HTTP callback，不限制为 loopback，也不限制 IP 网段或端口。关闭时这些 Web URL 仍必须 HTTPS。 |
| `TELESRV_TELEGRAM_LOGIN_SIGNING_KEYS_FILE` | path / `data/telegram-login/signing-keys.json` | 由 `cmd/telegramloginkeygen` 生成的 JOSE 私钥环；JWKS 会发布 active 和仍在退役窗口内的公钥。 |
| `TELESRV_TELEGRAM_LOGIN_CODE_KEYS_FILE` | path / `data/telegram-login/code-keys.json` | 用于可恢复一次性 authorization code 的 AES-256-GCM envelope key ring。 |
| `TELESRV_TELEGRAM_LOGIN_SECRET_PEPPER_FILE` | path / `data/telegram-login/client-secret-pepper` | HMAC-SHA-256 Client Secret 摘要的部署 pepper 文件，内容必须是恰好 32 个随机字节的 base64 编码。 |
| `TELESRV_TELEGRAM_LOGIN_REQUEST_TTL` | duration / `5m` | pending authorization 生命周期，限定 `1m..15m`。 |
| `TELESRV_TELEGRAM_LOGIN_CODE_TTL` | duration / `2m` | 一次性 code 生命周期，限定 `30s..10m`。 |
| `TELESRV_TELEGRAM_LOGIN_ID_TOKEN_TTL` | duration / `1h` | ID token 生命周期，限定 `1m..24h`；退役签名公钥必须覆盖该窗口。 |
| `TELESRV_TELEGRAM_LOGIN_TRUSTED_PROXY_CIDRS` | 逗号分隔 CIDR / 空 | 只有直连 peer 落在该列表时才信任 `Forwarded`/`X-Forwarded-*` 客户端元数据；文档中的单机 nginx 部署使用 `127.0.0.1/32,::1/128`。 |
| `TELESRV_TELEGRAM_LOGIN_RETENTION` | duration / `168h` | terminal request/code/revocation 后的保留期，限定 `1h..90d`。 |
| `TELESRV_TELEGRAM_LOGIN_SWEEP_INTERVAL` | duration / `5m` | retention worker 周期，限定 `10s..1h`。 |
| `TELESRV_TELEGRAM_LOGIN_SWEEP_BATCH` | int / `500` | 每轮最大清理行数，限定 `1..1000`。 |

### 3.1 Bot API webhook 故障排查

先区分三个地址，禁止把 webhook 接收域名当成 Bot API 地址：

| 名称 | 配置/来源 | 方向与用途 |
|---|---|---|
| Bot API listener | telesrv 的 `TELESRV_BOT_API_ADDR` | telesrv 的监听地址；空值表示关闭。`0.0.0.0` 只能用于 bind，不能作为客户端请求目标。 |
| Bot API base URL | bot 应用的 `TELEGRAM_API_URL` 等配置 | bot 应用访问 telesrv 的可达地址，例如 `http://172.17.0.1:8088`。方法地址为 `<base>/bot<TOKEN>/<method>`，文件地址为 `<base>/file/bot<TOKEN>/<file_path>`。 |
| Webhook receiver URL | bot 应用的 `WEBHOOK_URL + WEBHOOK_PATH`，经 `setWebhook` 登记 | telesrv 主动 POST update 的目标，例如 `https://bot.example.com/webhook`。它不是 Bot API base URL。 |

网络方向也不同：polling 是 `bot 应用 -> telesrv Bot API`，webhook 是
`telesrv -> bot 应用 webhook receiver`。因此 polling 正常只能证明前一条路径可达，
不能证明 webhook 的 DNS、出站 TCP、TLS、反向代理或 Docker hairpin 路径正常。

#### 1. 从 Bot API 查询真实 webhook 状态

应在 bot 应用容器中使用它实际配置的 Bot API base URL；不要把 token 展开后粘贴到
聊天、工单或截图：

```sh
curl -sS -X POST \
  "${TELEGRAM_API_URL%/}/bot${BOT_TOKEN}/getWebhookInfo" | jq
```

若没有 `TELEGRAM_API_URL` 这个变量，就把它替换成与
`TELESRV_BOT_API_ADDR` 对应的**客户端可达地址**。例如 telesrv 监听
`0.0.0.0:8088`，同宿主 Docker 容器可能使用 `http://172.17.0.1:8088`；不要请求
`http://0.0.0.0:8088`。

按下表判读响应：

| 结果 | 结论与下一步 |
|---|---|
| `url` 为空 | webhook 没有登记到这台 telesrv；检查 bot 应用是否确实使用该 Bot API base URL，以及启动时 `setWebhook` 是否成功。 |
| `pending_update_count` 增长 | update 已进入 telesrv durable queue，但没有成功交付；继续看 `last_error_message`。 |
| `last_error_message` 为 HTTP `401`/`403` | 接收端已可达，但 webhook secret 不一致或请求被认证层拒绝。 |
| `dial tcp ... i/o timeout` | telesrv 到目标 IP/端口的连接超时；检查出站防火墙、Docker 网络、回环 NAT/hairpin 和安全组。 |
| `connection refused` | 目标地址可达，但相应端口没有监听或端口映射/反代 upstream 错误。 |
| DNS/`no such host` | telesrv 所在运行环境无法解析 webhook hostname。 |
| TLS/`x509` 错误 | 证书链、hostname、SNI 或容器 CA trust 有问题。HTTPS 使用系统信任链。 |
| `allowed_updates` 不含目标类型 | 新产生的该类型 update 不会入队；普通 `/start` 至少需要 `message`。 |
| pending 归零但应用无响应 | telesrv 已收到 2xx；转查接收应用内部 queue、worker、dispatcher 和 handler 日志。 |

`getWebhookInfo` 查询的是 telesrv 持久化的交付事实；应用自己的 `/health` 只能证明
接收路由和 worker 已启动，不能代替这一步。

#### 2. 用正确请求头验证接收端

Telegram webhook secret 与 Bot token、OIDC Client Secret、API key 都是不同凭据。
接收端校验的标准请求头是 `X-Telegram-Bot-Api-Secret-Token`，不是
`Authorization: Bearer`：

```sh
curl -i -X POST "${WEBHOOK_URL%/}${WEBHOOK_PATH}" \
  -H 'Content-Type: application/json' \
  -H "X-Telegram-Bot-Api-Secret-Token: ${WEBHOOK_SECRET_TOKEN}" \
  -d '{"update_id":2147483000}'
```

预期为 HTTP 2xx。`401 invalid_secret_token` 表示请求已经到达应用，但 header 缺失或
值不匹配。编辑 `.env` 后必须重建/重启读取该配置的应用；只修改磁盘文件不会更新
已经登记到 telesrv 的 secret，也不会更新接收进程启动时捕获的 secret。

#### 3. 从 telesrv 的实际网络命名空间测试

浏览器或官方 Telegram 能访问公网 webhook，只能证明公网入站正常。必须从实际运行
telesrv 的宿主机、容器或 network namespace 再测一次：

```sh
docker exec <telesrv-container> sh -lc \
  'getent hosts bot.example.com; curl -vk --connect-timeout 10 https://bot.example.com/health/unified'
```

如果公网客户端正常而这里 `dial tcp ...:443: i/o timeout`，常见原因是同机公网 IP
回环失败。优先使用 split DNS 或容器 host mapping，让公网 hostname 在 telesrv 容器
内解析到反向代理的内部入口，同时保留原 hostname、HTTPS SNI 和证书校验。例如反代
的 443 已发布到 Docker 宿主机时，可先验证：

```sh
curl -vk --resolve bot.example.com:443:172.17.0.1 \
  https://bot.example.com/health/unified
```

验证通过后，可在 telesrv Compose 中使用与实际网络匹配的配置：

```yaml
extra_hosts:
  - "bot.example.com:host-gateway"
```

其它可选修复包括：把 telesrv 接入反向代理所在 Docker network、为 Docker subnet
放行宿主机 443，或修正云安全组/NAT hairpin。telesrv 允许登记内部 HTTP receiver，
但只有在两端共享受控内网且调用方的 `WEBHOOK_URL` 不同时承担 OIDC、支付或公开媒体
回调时才应使用；不要为绕过网络问题盲目把应用的全局公开 URL 改成内部地址。

#### 4. 修复后的闭环验证

1. 重新启动 bot 应用，让它用当前 URL、secret 和 `allowed_updates` 再次调用
   `setWebhook`。
2. 发送一条新的 `/start` 或点击 callback 按钮。
3. 再次调用 `getWebhookInfo`；`pending_update_count` 应下降到 `0`，且不再出现新的
   `last_error_date`。
4. 检查 telesrv Warning 日志中的 `bot api webhook delivery failed`。日志包含
   `bot_user_id`、`retry_in` 和失败原因，但不得记录 webhook URL、Bot token 或 secret。
5. 检查接收应用是否记录并处理该 `update_id`。webhook 是 at-least-once，应用必须能
   安全处理失败重试带来的重复 update。

若凭据曾出现在命令历史、聊天或截图中，立即轮换 Bot token、webhook secret、OIDC
Client Secret 及同屏暴露的其它 API key/数据库密码；排查资料只保留脱敏结果。

### 3.2 Telegram Login / OIDC 完整启用流程

#### 1. 一次性生成 `data/telegram-login`

在 `telesrv` 仓库根目录执行：

```powershell
go run ./cmd/telegramloginkeygen -mode init -dir data/telegram-login
Get-ChildItem .\data\telegram-login
```

Linux 部署也可使用同一命令；生成后应限制目录权限：

```bash
go run ./cmd/telegramloginkeygen -mode init -dir data/telegram-login
chmod 0700 data/telegram-login
chmod 0600 data/telegram-login/*
```

初始化会生成以下私密文件，命令不会把密钥内容输出到终端，并会拒绝覆盖已经存在的
`signing-keys.json`、`code-keys.json` 或 `client-secret-pepper`：

- `signing-keys.json` 和三个 `signing-*.pem`：RS256、ES256、EdDSA ID token 签名私钥及清单；
- `code-keys.json`：一次性 authorization code 使用的 AES-256-GCM envelope key ring；
- `client-secret-pepper`：保存和校验 OIDC Client Secret 摘要时使用的 32 字节部署 pepper。

`data/*` 默认已被仓库 `.gitignore` 排除。不要把该目录放入 Git、发布压缩包、日志或
普通备份；多实例必须挂载同一份受保护的文件，并在轮换后一起重启。丢失 pepper 会让
现有 Client Secret 无法验证，丢失仍在发布窗口内的签名私钥会让尚未过期的 ID token
无法继续通过 JWKS 验证。

#### 2. 配置并启动 Provider

以下示例直接通过 `http://192.0.2.25:2401` 对外提供 OIDC；请替换成客户端实际可达的
服务器 IP。直接监听局域网/公网网卡时使用 `0.0.0.0:2401`，仅由同机反向代理转发时
应改回 `127.0.0.1:2401`：

```env
TELESRV_PUBLIC_BASE_URL=http://192.0.2.25:2401
TELESRV_PUBLIC_LINK_WEB_ADDR=0.0.0.0:2401
TELESRV_PUBLIC_APP_SCHEME=telesrv

TELESRV_TELEGRAM_LOGIN_ENABLE=true
TELESRV_TELEGRAM_LOGIN_ISSUER=http://192.0.2.25:2401
TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP=true
TELESRV_TELEGRAM_LOGIN_SIGNING_KEYS_FILE=data/telegram-login/signing-keys.json
TELESRV_TELEGRAM_LOGIN_CODE_KEYS_FILE=data/telegram-login/code-keys.json
TELESRV_TELEGRAM_LOGIN_SECRET_PEPPER_FILE=data/telegram-login/client-secret-pepper
```

使用 HTTPS 时，把 `TELESRV_TELEGRAM_LOGIN_ISSUER` 和公开根地址改成精确 HTTPS
origin，并保持 `TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP=false`。issuer 是 token 的 `iss`
以及 discovery 中所有端点的根地址，scheme、host 和 port 必须与依赖方访问的地址完全
一致。启动或重启 `telesrv` 后，先验证公开端点：

```powershell
curl.exe http://192.0.2.25:2401/.well-known/openid-configuration
curl.exe http://192.0.2.25:2401/.well-known/jwks.json
curl.exe -I http://192.0.2.25:2401/js/telegram-login.js
```

discovery 返回的 `issuer` 必须等于配置值，`authorization_endpoint`、`token_endpoint`
和 `jwks_uri` 必须可从依赖方访问。使用反向代理时需原样转发
`/.well-known/openid-configuration`、`/.well-known/jwks.json`、`/auth`、`/auth/status`、
`/token`、`/crossapp`、`/inapp`、`/telegram-login.js` 和 `/js/telegram-login.js`。

#### 3. 用本服 `@BotFather` 创建 OIDC Client

先用 `/newbot` 创建或选择已有 bot，然后在本服 `@BotFather` 中执行 `/setlogin` 并选择
该 bot。首次配置会返回：

- `Client ID`：bot user ID 的十进制字符串；
- `Client Secret`：只显示一次，与 Bot API token 不同，必须立即保存到密钥管理系统。

选择一次 bot 后会持续停留在它的配置会话中，无需为每项修改重复 `/setlogin` 和 bot
username。可以逐条发送，也可以像下面这样在一条消息中粘贴多行命令（每条消息最多
32 行）。下面假设依赖方页面运行在 `http://192.0.2.30:3000`：

```text
add origin http://192.0.2.30:3000
add redirect http://192.0.2.30:3000/oauth/callback
algorithm RS256
enable
```

全部修改成功后发送 `/done`，BotFather 会退出配置会话并返回最终配置摘要。各条修改会
立即生效；`/cancel` 只关闭当前会话，不会回滚已经成功的修改。多行消息若中途失败，
BotFather 会明确列出已应用项、失败行以及未执行的后续行，并保留当前 bot 选择供修正
后继续操作。

`origin` 只能是无 path/query/fragment 的精确 Web origin，用于 JS SDK、popup CORS 和
legacy `login_url`；`redirect` 是 Authorization Code Flow 返回 code 的精确完整 URI。
不支持 wildcard 或 prefix 匹配。用 `/logininfo` 检查状态和登记值；用 `/setlogin`
增删 URL、切换签名算法或 disable；用 `/resetloginsecret` 轮换 Client Secret。可用的
签名算法为 RS256、ES256、EdDSA，以及仅在对应构建和 key ring 已提供时可选的 ES256K；
EdDSA/ES256K 只允许 `openid` scope。

#### 4. 依赖方接入标准 OIDC

依赖方应首先读取：

```text
http://192.0.2.25:2401/.well-known/openid-configuration
```

标准流程为 Authorization Code + PKCE S256：

1. 生成随机 `state`、`nonce` 和 PKCE `code_verifier`，计算 S256 `code_challenge`；
2. 浏览器打开 discovery 中的 `authorization_endpoint`，携带 `client_id`、精确
   `redirect_uri`、`response_type=code`、包含 `openid` 的 `scope`、`state`、`nonce`、
   `code_challenge` 和 `code_challenge_method=S256`；
3. 用户在 TDesktop/Android 中确认后，依赖方 callback 校验 `state` 并取得一次性 code；
4. 服务端向 discovery 中的 `token_endpoint` POST `grant_type=authorization_code`、code、
   同一 `redirect_uri` 和 `code_verifier`，机密 client 使用 HTTP Basic 或
   `client_secret_post` 提交 Client Secret；
5. 用 discovery 的 `jwks_uri` 验证 ID token 签名，并严格校验 `iss`、`aud`、`exp`、
   `nonce` 和非空 `sub`。不要只解码而不验签。

支持的 scope 为 `openid`、`profile`、`phone`、`telegram:bot_access`。当前不提供
UserInfo、refresh token 或 introspection endpoint。浏览器前端可以加载
`<issuer>/js/telegram-login.js` 使用本地 JS SDK；Client Secret 只能留在服务端。

#### 5. 使用 Bedolaga demo 验证完整链路

安装 demo 依赖：

```powershell
python -m venv "$env:TEMP\telesrv-bedolaga-demo-venv"
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" -m pip install `
  -r .\cmd\bots\bedolagaformat\requirements.txt
```

将第 3 步得到的 Client ID/Secret 和同一个 Bot API token 仅放入进程环境：

```powershell
$env:TELESRV_BOT_TOKEN = "<bot_id>:<bot_api_secret>"
$env:TELESRV_BOT_API_SERVER = "http://192.0.2.25:8081"
$env:TELESRV_BOT_LOGIN_DEMO = "1"
$env:TELESRV_BOT_LOGIN_ISSUER = "http://192.0.2.25:2401"
$env:TELESRV_BOT_LOGIN_CLIENT_ID = "<BotFather 返回的 Client ID>"
$env:TELESRV_BOT_LOGIN_CLIENT_SECRET = "<只显示一次的 OIDC Client Secret>"
$env:TELESRV_BOT_LOGIN_PUBLIC_URL = "http://192.0.2.30:3000"
$env:TELESRV_BOT_LOGIN_LISTEN = "0.0.0.0:3000"

& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\demo.py --drop-pending --login-demo
```

确保 BotFather 登记的 origin 等于 `TELESRV_BOT_LOGIN_PUBLIC_URL`，redirect 等于
`<TELESRV_BOT_LOGIN_PUBLIC_URL>/oauth/callback`。在客户端向 bot 发送 `/logindemo`：第一颗
按钮验证 Bot API `login_url` 和 HMAC 回调，第二颗按钮页面分别验证本地 JS SDK popup
以及 Authorization Code + PKCE/JWKS。省略 Client Secret 时只能验证 JS popup，服务端
code flow 会明确禁用。

#### 6. 密钥轮换

签名 key 轮换时，旧公钥发布窗口必须至少覆盖配置的 ID token TTL 再加 10 分钟；操作
完成后所有实例一起重启：

```powershell
go run ./cmd/telegramloginkeygen -mode rotate-signing -algorithm RS256 `
  -id-token-ttl 1h -publish-for 2h -dir data/telegram-login
go run ./cmd/telegramloginkeygen -mode rotate-code -dir data/telegram-login
```

`rotate-signing` 可分别用于 RS256、ES256、EdDSA；`rotate-code` 保留旧 code key 并新增
active key。不要手工编辑 manifest 或 PEM，不要在各实例上分别生成不一致的 key ring。

## 4. PostgreSQL、Redis、文件与 seed

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_POSTGRES_DSN` | secret DSN / `postgres://telesrv:telesrv@127.0.0.1:5432/telesrv?sslmode=disable` | 主业务持久库；生产必须替换开发凭证与 TLS 策略。 |
| `TELESRV_POSTGRES_MAX_CONNS` | int / `50` | pgxpool 最大连接数；`<=0` 使用 pgx 默认值，该默认通常不足以覆盖生产 outbox/RPC 并发。 |
| `TELESRV_POSTGRES_MIN_CONNS` | int / `16` | pgxpool 预热最小连接数。 |
| `TELESRV_REDIS_ADDR` | address / `127.0.0.1:6399` | 验证码、限流、共享更新/缓存易失态使用的 Redis。 |
| `TELESRV_REDIS_PASSWORD` | secret string / 空 | Redis 密码。 |
| `TELESRV_REDIS_DB` | int / `0` | Redis 逻辑库编号。 |
| `TELESRV_LANGPACK_SEED_DIR` | path / `data/langpack` | TDesktop `.strings` 语言包 seed 目录。 |
| `TELESRV_OFFICIAL_GIFTS_DIR` | path / `data/official-gifts` | `cmd/giftfetch` 生成的只读官方礼物快照；供管理后台选择、验哈希并显式导入。 |
| `TELESRV_BLOB_DIR` | path / `data/blobs` | 本地开发 blob backend 的媒体字节根目录。 |
| `TELESRV_STICKER_SEED_DIR` | path / `data/sticker-seed` | 导入 documents、sticker sets、blob 的贴纸/reaction seed 目录。 |
| `TELESRV_STICKER_SEED_MAX_SETS` | int / `300` | 启动时导入的常规贴纸集上限；`<=0` 表示不限。 |

语言包 seed 以文件 manifest 为事实源。新增语言时放入 `data/langpack/<pack>/<pack>_<lang>_v<version>.strings` 并重启 `telesrv`；`pack` 必须与所在一级目录一致，允许 Telegram 已使用的字母、数字、`-` 与 `_`（例如 `android_x`），`lang` 会统一为小写、连字符形式（例如 `pt_BR` 归一为 `pt-br`）。同一语言存在多个文件时只读取最高版本。修改已有语言的有效内容必须提高版本；同版本有效内容变化或版本倒退会阻止启动。删除语言文件或整个 pack 子目录后，下次重启会原子移除对应数据库目录和字符串。启动先流式计算源文件 SHA-256；未变化文件复用上次原子 manifest，不解析字符串也不写库，只有新增或变化文件才解析并通过 PostgreSQL `COPY` 整包替换。

## 5. 登录、OTP Provider、SMTP 与 passkey

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_DEV_AUTH_CODE` | sensitive string / `12345` | `PHONE_CODE_DELIVERY_PROVIDER=development` 使用的固定开发登录码；不得把默认值暴露在公网环境。 |
| `TELESRV_AUTH_CODE_TTL` | duration / `5m` | 登录/注册/邮箱验证码有效期，必须为正数。 |
| `TELESRV_AUTH_CODE_MAX_ATTEMPTS` | int / `5` | 单 code/hash 最大错误次数，必须为正数。 |
| `TELESRV_PHONE_CODE_LENGTH` | int / `5` | `webhook` phone provider 生成的随机 SMS 验证码长度，允许 `4..10`。 |
| `TELESRV_AUTH_CODE_PHONE_RATE_LIMIT` | int / `5` | 每个规范化手机号摘要在窗口内的发码上限；`<=0` 关闭该维度。 |
| `TELESRV_AUTH_CODE_AUTH_KEY_RATE_LIMIT` | int / `20` | 每个 raw auth key 在窗口内的发码上限；`<=0` 关闭该维度。 |
| `TELESRV_AUTH_CODE_RATE_WINDOW` | duration / `10m` | 手机号与 auth-key 发码限流共用窗口。 |
| `TELESRV_PHONE_CODE_DELIVERY_PROVIDER` | enum / `development` | `development` 使用固定码；`webhook` 为登录、注册、改号生成随机 SMS code 并调用 OTP Webhook。已有账号在两种模式下都先 durable 写入同码 777000 消息，Webhook 只是附加渠道。 |
| `TELESRV_EMAIL_CODE_DELIVERY_PROVIDER` | enum / `smtp` | 登录邮箱、邮箱 setup/change 的投递实现：`smtp` 或 `webhook`。已有账号的登录邮箱码会先同码镜像到 777000；邮箱 setup/change 仍只走 provider。 |
| `TELESRV_OTP_WEBHOOK_URL` | absolute URL / 空 | 任一 provider 选择 `webhook` 时必填；固定 v1 协议见 [otp-delivery.md](otp-delivery.md)。允许任意合法 `http://` 或 `https://` 主机/IP 与端口，不得含 userinfo。 |
| `TELESRV_OTP_WEBHOOK_SECRET` | secret string / 空 | 可选 HMAC-SHA256 签名密钥；非空时发送 `X-Telesrv-Signature`。 |
| `TELESRV_OTP_WEBHOOK_TIMEOUT` | duration / `5s` | Webhook HTTP 请求超时，启用 Webhook 时必须为正数。 |
| `TELESRV_LOGIN_EMAIL_ENABLE` | bool / `false` | 启用登录邮箱验证码；email provider 为 `smtp` 时要求 SMTP 配置，`webhook` 时不依赖 SMTP。 |
| `TELESRV_LOGIN_EMAIL_REQUIRE_SETUP` | bool / `false` | 强制没有登录邮箱的账号设置邮箱；要求 `TELESRV_LOGIN_EMAIL_ENABLE=true`。 |
| `TELESRV_LOGIN_EMAIL_CODE_LENGTH` | int / `6` | 邮箱验证码长度，允许 `4..10`。 |
| `TELESRV_SMTP_HOST` | string / 空 | SMTP host；启用登录邮箱且 email provider 为 `smtp` 时必填。 |
| `TELESRV_SMTP_PORT` | int / `587` | SMTP 端口；使用 SMTP provider 时必须为 `1..65535`。 |
| `TELESRV_SMTP_USERNAME` | sensitive string / 空 | SMTP 用户名；`TELESRV_SMTP_FROM` 为空时也用作发件人。 |
| `TELESRV_SMTP_PASSWORD` | secret string / 空 | SMTP 密码。 |
| `TELESRV_SMTP_FROM` | email/string / 空 | envelope/header 发件人；启用登录邮箱时它与 SMTP username 至少一个非空。 |
| `TELESRV_SMTP_FROM_NAME` | string / `telesrv` | 登录邮件展示的发件人名称。 |
| `TELESRV_SMTP_TLS` | enum / `starttls` | 仅允许 `starttls`、`tls`、`none`，其它值阻止启动。 |
| `TELESRV_SMTP_TIMEOUT` | duration / `10s` | SMTP 操作超时；使用 SMTP provider 时必须为正数。 |
| `TELESRV_PASSKEY_RP_ID` | hostname / `telesrv.net` | WebAuthn relying-party ID，用于校验 `rpIdHash`；Android Credential Manager 必须与公网 `assetlinks.json` 对齐。 |
| `TELESRV_PASSKEY_ALLOWED_ORIGINS` | list / 空 | WebAuthn origin 白名单；空值不做显式 origin 校验，因为服务端可能无法预知 Android APK-key-hash origin。 |

## 6. 地图、外链媒体、链接预览与上传

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_MAPBOX_TOKEN` | secret string / 空 | `upload.getWebFile` 地图缩略图使用的 Mapbox Static Images token；空值使用确定性占位图。 |
| `TELESRV_MAPTILE_CACHE_DIR` | path / `data/maptiles` | 地图缩略图磁盘缓存，保证分片下载字节稳定并控制上游配额。 |
| `TELESRV_EXTERNAL_MEDIA_ENABLE` | bool / `true` | 启用带 SSRF 防护的外链 photo/document 抓取。 |
| `TELESRV_EXTERNAL_MEDIA_MAX_BYTES` | int bytes / `10485760` | 单次外链媒体响应体上限；下游把 `<=0` 归一为 10 MiB 安全默认值。 |
| `TELESRV_EXTERNAL_MEDIA_RATE_PER_MIN` | int / `60` | 全局每分钟外链媒体抓取数；下游把 `<=0` 归一为默认值。 |
| `TELESRV_WEBPAGE_PREVIEW_ENABLE` | bool / `true` | 启用带 SSRF 防护的网页元数据/图片抓取和链接预览。 |
| `TELESRV_WEBPAGE_PREVIEW_MAX_BYTES` | int bytes / `5242880` | 预览 HTML 与图片抓取共用的响应体上限；下游把 `<=0` 归一为 5 MiB。 |
| `TELESRV_WEBPAGE_PREVIEW_RATE_PER_MIN` | int / `300` | 全局每分钟预览上游请求数；一次解析最多产生两次请求。 |
| `TELESRV_UPLOAD_PART_TTL` | duration / `24h` | 未组装上传分片保留期。 |
| `TELESRV_UPLOAD_PART_GC_INTERVAL` | duration / `30m` | upload part GC 轮询间隔。 |
| `TELESRV_UPLOAD_PART_GC_BATCH` | int / `10000` | 单批 upload part GC 最大删除行数。 |
| `TELESRV_UPLOAD_INFLIGHT_MAX_BYTES` | int64 bytes / `4194304000` | 单用户未组装上传字节上限；`<=0` 表示不限。 |
| `TELESRV_UPLOAD_INFLIGHT_MAX_PARTS` | int / `8000` | 单用户未组装分片行数上限；`<=0` 表示不限。 |
| `TELESRV_UPLOAD_INFLIGHT_MAX_FILES` | int / `64` | 单用户并发未组装 `file_id` 上限；`<=0` 表示不限。 |

## 7. AI compose 与 Business automation

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_BUSINESS_AI_PROVIDER` | string / `echo` | Business 自动回复生成器。可填 `echo`/空值（回显触发文本）、`template`/`quick_reply`/`quick-reply`（使用 quick reply 模板），或 `ai`/`compose_ai`/`ai_compose`/`aicompose`/`kimi`（复用 `TELESRV_AI_PROVIDERS` provider 链）。这里不接受任意 provider 名；例如使用 Ollama 时填 `TELESRV_BUSINESS_AI_PROVIDER=ai`，实际 provider 由 `TELESRV_AI_PROVIDERS=ollama,local` 决定。 |
| `TELESRV_AI_ENABLED` | bool / `true` | 启用客户端输入框改写/润色；关闭时返回空 tone 集合并隐藏入口。 |
| `TELESRV_AI_PROVIDERS` | list / `local` | 按顺序尝试的 provider 链；空列表回退确定性 `local`，不访问外网。 |
| `TELESRV_AI_TIMEOUT` | duration / `15s` | 单次 provider 调用总超时。 |
| `TELESRV_AI_RATE_LIMIT` | int / `20` | 单账号每窗口 compose 次数。 |
| `TELESRV_AI_RATE_WINDOW` | duration / `1m` | compose AI 限流窗口。 |
| `TELESRV_AI_LOG_CONTENT` | bool / `false` | false 时日志只写长度/provider/状态；开启可能暴露用户输入和生成文本。 |
| `TELESRV_TRANSLATION_ENABLED` | bool / `true` | 启用 `messages.translateText`；仍需至少一个远程 AI provider，local 回显 provider 不会被用作翻译。 |
| `TELESRV_TRANSLATION_PROVIDERS` | list / 空 | 从 `TELESRV_AI_PROVIDERS` 选择用于翻译的 provider 名；空表示使用其中全部远程 provider。 |
| `TELESRV_TRANSLATION_TIMEOUT` | duration / `15s` | 一批翻译的总超时；批内最多 20 条、provider 并发固定为 4。 |
| `TELESRV_TRANSLATION_RATE_LIMIT` | int / `60` | 单账号每窗口允许的翻译文本条数；一批 20 条计 20，防止批量请求放大 provider 调用。 |
| `TELESRV_TRANSLATION_RATE_WINDOW` | duration / `1m` | 翻译限流窗口。 |

聊天翻译会把用户主动选择翻译的消息正文发送给所配置的外部 provider。默认日志不记录正文，但部署者仍应在隐私政策中披露上游处理方；只配置 `local` 时服务端返回 `TRANSLATIONS_DISABLED`，不会回原文冒充译文。

对 `TELESRV_AI_PROVIDERS` 中的每个名称，telesrv 会转大写并把非字母数字字符替换为 `_`，再读取下列动态参数。例如 `openai-compatible` 对应 suffix `OPENAI_COMPATIBLE`。

| 动态参数 | 类型 / 默认值 | 说明 |
|---|---|---|
| `TELESRV_AI_<NAME>_KIND` | string / 由名称推导 | adapter 类型。内置值包括 `local`、`openai_responses`、`openai_chat`、`gemini`、`anthropic`；常用名称会自动映射。 |
| `TELESRV_AI_<NAME>_BASE_URL` | URL string / 空 | provider endpoint 覆盖；兼容接口或自托管 provider 通常需要。 |
| `TELESRV_AI_<NAME>_API_KEY` | secret string / provider fallback | provider 凭证；已知 provider 可回退到下述进程环境变量。 |
| `TELESRV_AI_<NAME>_MODEL` | string / 空 | provider model id；外部 provider 通常必填。 |
| `TELESRV_AI_<NAME>_MAX_OUTPUT_TOKENS` | int / `1024` | 请求的输出 token 上限。 |
| `TELESRV_AI_<NAME>_TEMPERATURE` | float / `0.2` | 采样 temperature。 |
| `TELESRV_AI_<NAME>_OMIT_TEMPERATURE` | bool / `false` | 对拒绝 temperature 字段的模型/provider 不发送该字段。 |
| `TELESRV_AI_<NAME>_THINKING` | string / 空 | provider 特定 reasoning/thinking 模式，统一转小写，例如 `disabled`。 |

下列 fallback 只支持**进程环境变量**，因为 env 文件会拒绝不以 `TELESRV_` 开头的键：`OPENAI_API_KEY`、`GEMINI_API_KEY`、`ANTHROPIC_API_KEY`。显式 `TELESRV_AI_<NAME>_API_KEY` 优先级更高。

## 8. Read-model 与 auth-key 缓存

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_TEMP_KEY_CACHE_MAX_ENTRIES` | int / `262144` | Router temp→perm auth-key binding 缓存容量。 |
| `TELESRV_TEMP_KEY_CACHE_TTL` | duration / `30m` | 复核周期；正常写入由 bind/revoke 精确失效，TTL 兜底跨进程/异常路径。 |
| `TELESRV_CHANNEL_ROW_CACHE_MAX` | int / `50000` | 共享 channel row 缓存容量；`<=0` 同时关闭缓存及 LISTEN/NOTIFY listener。 |
| `TELESRV_CHANNEL_MEMBER_CACHE_MAX` | int / `100000` | channel member/access read-model 缓存容量；`<=0` 关闭。 |
| `TELESRV_CHANNEL_DIALOG_CACHE_MAX` | int / `100000` | viewer/channel dialog 投影缓存容量；`<=0` 关闭。 |
| `TELESRV_CHANNEL_BOOST_CACHE_MAX` | int / `100000` | channel boost read-model 缓存容量；`<=0` 关闭。 |
| `TELESRV_CHANNEL_BOOST_CACHE_TTL` | duration / `10s` | boost 失效通知遗漏时允许的最大陈旧窗口。 |

## 9. Outbox、推送、限流、retention 与 GC

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_OUTBOX_WORKERS` | int / `4` | 并发 outbox worker 数；稳定逻辑分片保持单用户 pts 顺序。 |
| `TELESRV_OUTBOX_BATCH` | int / `100` | 每次 poll 最大 claim 行数；增大提高吞吐，也增加 DB/推送突发。 |
| `TELESRV_OUTBOX_INTERVAL` | duration / `200ms` | 两次 outbox claim 之间的等待。 |
| `TELESRV_OUTBOX_LEASE_TIMEOUT` | duration / `30s` | `dispatching` 行可被重新 claim 的超时；必须大于最坏单批投递耗时。 |
| `TELESRV_OUTBOX_POISON_RETENTION` | duration / `1m` | terminal failed 投递头的排障保留窗口；durable update 仍可经 difference 恢复。 |
| `TELESRV_OUTBOX_POISON_CLEANUP_INTERVAL` | duration / `15s` | terminal failed head 清理周期，独立于大表 retention。 |
| `TELESRV_OUTBOUND_PUSH_TIMEOUT` | duration / `200ms` | best-effort 在线 update 入队最长等待。 |
| `TELESRV_SEND_RATE_LIMIT` | int / `30` | 单账号每发送窗口允许的消息数；`<=0` 关闭。 |
| `TELESRV_SEND_RATE_WINDOW` | duration / `1m` | 发送限流窗口。 |
| `TELESRV_CATCHUP_RATE_LIMIT` | int / `0` | 单用户每窗口 difference/catch-up RPC 数；`<=0` 关闭。 |
| `TELESRV_CATCHUP_RATE_WINDOW` | duration / `1m` | catch-up 限流窗口。 |
| `TELESRV_CHANNEL_NUDGE_MAX_TARGETS` | int / `0` | 单次 channel fan-out nudge 目标上限；`<=0` 使用内置默认值。 |
| `TELESRV_UPDATE_EVENT_RETENTION` | duration / `168h` | durable update log 保留期；只删除已被协议安全水位/状态覆盖的事件。 |
| `TELESRV_BOT_API_UPDATE_RETENTION` | duration / `24h` | Bot API update 队列最长保留期；已确认行另有固定短宽限。 |
| `TELESRV_ORPHAN_AUTH_KEY_RETENTION` | duration / `24h` | 没有 authorization/temp binding/活跃连接的握手 auth key 最短保留期。 |
| `TELESRV_RETENTION_INTERVAL` | duration / `1h` | 通用 retention worker 周期。 |
| `TELESRV_RETENTION_BATCH` | int / `10000` | 单次通用 retention 最大删除行数。 |

## 10. Premium 与 Stars 开发赠送

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_PREMIUM_GRANT_MONTHS` | int / `3` | 新注册账号默认 Premium 月数；`0` 关闭新赠送，不影响已有迁移 backfill。 |
| `TELESRV_STARS_STARTING_GRANT` | int64 / `1000` | 对所有账号幂等惰性授予的 Stars 起始余额；`0` 关闭自动赠送。 |
| `TELESRV_PREMIUM_SWEEP_INTERVAL` | duration / `1m` | 过期 Premium 清理/推送周期；读取路径独立即时派生到期状态。 |
| `TELESRV_PREMIUM_SWEEP_BATCH` | int / `500` | 单次 sweep 最大处理行数。 |
| `TELESRV_STARGIFT_SWEEP_INTERVAL` | duration / `15s` | Star Gift 报价/竞拍本地生命周期清扫周期；不会连接区块链。 |
| `TELESRV_STARGIFT_SWEEP_BATCH` | int / `1000` | 单次礼物生命周期清扫最多处理的报价、竞拍与 outbox 工作量。 |
| `TELESRV_STARGIFT_TON_STARTING_GRANT` | int64 / `10000000000` | 每个用户首次访问 telesrv 内部 TON 账本时幂等授予的 nanoton；`0` 关闭赠送。它不是链上资产。 |
| `TELESRV_STARGIFT_TRANSFER_STARS` | int64 / `25` | collectible 转赠费用；设为 `0` 时使用免费转赠 RPC。 |
| `TELESRV_STARGIFT_DROP_DETAILS_STARS` | int64 / `25` | 移除 collectible 原始发送者/附言信息所需 Stars。 |
| `TELESRV_STARGIFT_OFFER_MIN_STARS` | int / `1` | collectible 签发时固化的用户持有礼物最低 Stars 报价；`0` 不开放报价入口。 |
| `TELESRV_STARGIFT_STARS_PROCEEDS_PERMILLE` | int / `1000` | Stars 成交时卖方实收比例（千分比）；差额作为平台佣金写入成交记录。 |
| `TELESRV_STARGIFT_TON_PROCEEDS_PERMILLE` | int / `1000` | 内部 TON 成交时卖方实收比例（千分比）；只影响本地账本。 |
| `TELESRV_STARGIFT_EXPORT_DELAY` | duration / `0s` | collectible 签发时固化到 `can_export_at` 的等待期。 |
| `TELESRV_STARGIFT_TRANSFER_DELAY` | duration / `0s` | 签发时固化到 `can_transfer_at` 的等待期。 |
| `TELESRV_STARGIFT_RESELL_DELAY` | duration / `0s` | 签发时固化到 `can_resell_at` 的等待期。 |
| `TELESRV_STARGIFT_CRAFT_DELAY` | duration / `0s` | 签发时固化到 `can_craft_at` 的等待期；可 Craft 礼物即使为 `0s` 也写升级时间这一正数能力边界，0 只表示不具备 Craft 能力或已终结。 |
| `TELESRV_STARGIFT_CRAFT_CHANCE_PERMILLE` | int / `250` | 每份输入礼物贡献的本地合成成功概率，累计上限 1000‰。 |

## 11. 私聊通话、群通话、TURN、SFU 与直播

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_CALL_RING_TIMEOUT` | duration / `90s` | 私聊通话 ringing/accepted 服务端兜底超时，应与客户端 `callRingTimeoutMs` 保持一致。 |
| `TELESRV_CALL_TOMBSTONE_TTL` | duration / `60s` | 终态通话 tombstone 的幂等/晚到 RPC 吸收窗口。 |
| `TELESRV_CALL_MAX_ACTIVE_PER_USER` | int / `4` | 单用户非终态私聊通话上限；非正值由 phone service 归一。 |
| `TELESRV_CALL_SIGNALING_MAX_BYTES` | int bytes / `65536` | 单条 `phone.sendSignalingData` 载荷上限。 |
| `TELESRV_CALL_SIGNALING_RATE` | int / `50` | 单通话每秒信令转发上限，超限静默丢弃。 |
| `TELESRV_CALL_EXPIRY_INTERVAL` | duration / `1s` | 通话 expiry dispatcher 轮询间隔。 |
| `TELESRV_GROUPCALL_CHECK_TTL` | duration / `45s` | 群通话参与者 liveness 水位过期阈值，客户端与 SFU reporter 都会刷新。 |
| `TELESRV_GROUPCALL_SWEEP_INTERVAL` | duration / `10s` | 幽灵参与者 sweep 周期。 |
| `TELESRV_GROUPCALL_MAX_PARTICIPANTS` | int / `32` | 当前小规模实现的单房间参与者上限。 |
| `TELESRV_TURN_ENABLE` | bool / `true` | 启用内嵌 TURN/STUN 与私聊通话 relay 下发；false 回退 LAN/P2P-only。 |
| `TELESRV_TURN_UDP_PORT` | int / `12400` | 内嵌 TURN/STUN UDP 监听端口；必须与 SFU 端口不同并放行防火墙。 |
| `TELESRV_TURN_ADVERTISE_IP` | string / 空 | 客户端可达 relay IP；空值依次回退 SFU advertise IP、通用 advertise IP。 |
| `TELESRV_TURN_SECRET` | secret string / 空 | TURN REST credential HMAC secret；空值生成进程级随机值，多实例/外部 coturn 必须显式共享稳定值。 |
| `TELESRV_TURN_RELAY_MIN_PORT` | int / `12500` | relay 分配端口范围下界（含）。 |
| `TELESRV_TURN_RELAY_MAX_PORT` | int / `12999` | relay 分配端口范围上界（含），不得小于下界，防火墙需放行整个范围。 |
| `TELESRV_CALL_TURN_CREDENTIAL_TTL` | duration / `6h` | 按通话签发的 TURN credential 有效期。 |
| `TELESRV_CALL_FORCE_RELAY` | bool / `false` | 强制 `p2p_allowed=false`，用于验证 TURN relay 路径。 |
| `TELESRV_SFU_ENABLE` | bool / `true` | 启用内嵌群通话媒体转发；false 保留仅信令 M0 模式。 |
| `TELESRV_SFU_UDP_PORT` | int / `12399` | Pion ICE UDPMux 端口，必须放行防火墙。 |
| `TELESRV_SFU_ADVERTISE_IP` | string / 空 | 下发给客户端的 ICE candidate IP；空值回退 `TELESRV_ADVERTISE_IP`，loopback 会静默破坏真机媒体。 |
| `TELESRV_LIVESTREAM_ENABLE` | bool / `true` | 启用频道 RTMP ingest 与 ffmpeg 切段。 |
| `TELESRV_LIVESTREAM_RTMP_ADDR` | address / `:2400` | RTMP ingest TCP 监听地址。 |
| `TELESRV_LIVESTREAM_RTMP_URL` | URL string / 空 | 返回 OBS 的服务器地址；空值派生 `rtmp://<AdvertiseIP>:2400/live`。 |
| `TELESRV_LIVESTREAM_FFMPEG_PATH` | path/command / `ffmpeg` | ffmpeg 可执行文件路径，默认从 `PATH` 解析。 |
| `TELESRV_LIVESTREAM_WORK_DIR` | path / 空 | segment 临时工作目录；空值使用系统临时目录。 |
| `TELESRV_LIVESTREAM_SEGMENT_KEEP` | int seconds / `32` | 每路直播在内存保留的 segment 秒数/窗口；非正值由 livestream service 归一。 |

## 12. 生产部署最低检查清单

生产至少应显式检查并替换这些开发值：PostgreSQL DSN 与 TLS、Redis 密码和网络暴露、RSA 私钥持久化、固定开发验证码暴露、Admin 凭证/session key、OTP Webhook/SMTP secret、AI/Mapbox API key、TURN secret 与防火墙端口、公开 URL/scheme 与客户端一致性，以及真机所需的非 loopback SFU/TURN advertise IP。
