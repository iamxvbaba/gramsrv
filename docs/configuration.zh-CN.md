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
| `TELESRV_BOT_API_ADDR` | nullable address / 空 | 最小 HTTP Bot API 监听；空值关闭，与 MTProto 共用 app/store 事实。 |
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
| `TELESRV_PUBLIC_LINK_WEB_ADDR` | nullable address / 空 | 只读 username/avatar/sticker/emoji/chatlist 落地页监听；空值关闭。生产应 loopback + nginx 精确反代；`.env.example` 为开发启用 `127.0.0.1:2401`。 |

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
| `TELESRV_BLOB_DIR` | path / `data/blobs` | 本地开发 blob backend 的媒体字节根目录。 |
| `TELESRV_STICKER_SEED_DIR` | path / `data/sticker-seed` | 导入 documents、sticker sets、blob 的贴纸/reaction seed 目录。 |
| `TELESRV_STICKER_SEED_MAX_SETS` | int / `300` | 启动时导入的常规贴纸集上限；`<=0` 表示不限。 |

## 5. 登录、邮箱验证码、SMTP 与 passkey

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_DEV_AUTH_CODE` | sensitive string / `12345` | 固定开发登录码；生产短信/风控尚未接入，不得把默认值暴露在公网环境。 |
| `TELESRV_AUTH_CODE_TTL` | duration / `5m` | 登录/注册/邮箱验证码有效期，必须为正数。 |
| `TELESRV_AUTH_CODE_MAX_ATTEMPTS` | int / `5` | 单 code/hash 最大错误次数，必须为正数。 |
| `TELESRV_AUTH_CODE_PHONE_RATE_LIMIT` | int / `5` | 每个规范化手机号摘要在窗口内的发码上限；`<=0` 关闭该维度。 |
| `TELESRV_AUTH_CODE_AUTH_KEY_RATE_LIMIT` | int / `20` | 每个 raw auth key 在窗口内的发码上限；`<=0` 关闭该维度。 |
| `TELESRV_AUTH_CODE_RATE_WINDOW` | duration / `10m` | 手机号与 auth-key 发码限流共用窗口。 |
| `TELESRV_LOGIN_EMAIL_ENABLE` | bool / `false` | 启用登录邮箱验证码投递；开启后 SMTP 配置成为必填。 |
| `TELESRV_LOGIN_EMAIL_REQUIRE_SETUP` | bool / `false` | 强制没有登录邮箱的账号设置邮箱；要求 `TELESRV_LOGIN_EMAIL_ENABLE=true`。 |
| `TELESRV_LOGIN_EMAIL_CODE_LENGTH` | int / `6` | 邮箱验证码长度，允许 `4..10`。 |
| `TELESRV_SMTP_HOST` | string / 空 | SMTP host；启用登录邮箱时必填。 |
| `TELESRV_SMTP_PORT` | int / `587` | SMTP 端口；启用登录邮箱时必须为 `1..65535`。 |
| `TELESRV_SMTP_USERNAME` | sensitive string / 空 | SMTP 用户名；`TELESRV_SMTP_FROM` 为空时也用作发件人。 |
| `TELESRV_SMTP_PASSWORD` | secret string / 空 | SMTP 密码。 |
| `TELESRV_SMTP_FROM` | email/string / 空 | envelope/header 发件人；启用登录邮箱时它与 SMTP username 至少一个非空。 |
| `TELESRV_SMTP_FROM_NAME` | string / `telesrv` | 登录邮件展示的发件人名称。 |
| `TELESRV_SMTP_TLS` | enum / `starttls` | 仅允许 `starttls`、`tls`、`none`，其它值阻止启动。 |
| `TELESRV_SMTP_TIMEOUT` | duration / `10s` | SMTP 操作超时；启用登录邮箱时必须为正数。 |
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

生产至少应显式检查并替换这些开发值：PostgreSQL DSN 与 TLS、Redis 密码和网络暴露、RSA 私钥持久化、固定开发验证码暴露、Admin 凭证/session key、启用邮件时的 SMTP secret、AI/Mapbox API key、TURN secret 与防火墙端口、公开 URL/scheme 与客户端一致性，以及真机所需的非 loopback SFU/TURN advertise IP。
