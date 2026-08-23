# telesrv 配置参数手册

英文版：[configuration.en.md](configuration.en.md)

本文覆盖 `internal/config` 实际读取的配置。服务入口现在使用按角色拆分的 YAML：
`configs/edge.yaml`、`configs/core.yaml`、`configs/egress.yaml`、`configs/file.yaml`、
`configs/sfu.yaml`、`configs/admin.yaml`、`configs/ton.yaml`。默认值和校验行为以 `internal/config` 下对应 role config Go 文件为权威来源。
所有配置修改都需要重启进程；telesrv 当前不支持配置热加载。

## 1. 加载方式、语法与优先级

- `cmd/telesrv-edge|core|egress|file|sfu|admin|ton` 默认分别读取 `configs/<role>.yaml`；也可用
  `--config <path>` 或进程环境变量 `TELESRV_CONFIG=<path>` 指向自己的 YAML 文件。
- YAML 使用严格解析：未知字段、非法结构、非法时长都会阻止启动。环境变量不再作为普通字段覆盖
  YAML；只在 YAML 字符串中通过 `${NAME}` 展开 secret、DSN 或部署平台注入值。
- `TELESRV_CONFIG` 只选择当前进程的配置文件。不要把它写进 YAML 内部；每个服务的 systemd unit、
  Docker/Kubernetes 配置或本地启动脚本应分别传入自己的 config path。
- YAML bool 使用 `true`/`false`；列表使用 YAML sequence；时长字段使用 Go 格式字符串，例如
  `200ms`、`30s`、`5m`、`168h`。
- 不要提交真实密码、token、私有 DSN 或 TURN secret。生产环境应使用受保护的 service environment 或密钥管理系统。

代码边界同样按角色拆分：`internal/config/edge_config.go`、`core_config.go`、`egress_config.go`、
`file_config.go`、`sfu_config.go`、`admin_config.go`、`ton_config.go` 分别定义对应 YAML schema、`Load<Role>()` 和 runtime config
类型。生产入口和 `internal/node/<role>` 不应再接收全局 `config.Config`；仓库不再提供对外的旧 env 文件入口。

## 2. MTProto 监听、传输与资源预算

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_LISTEN` | string / `0.0.0.0:2398` | `cmd/telesrv-edge` 的 MTProto TCP 监听地址，patched 客户端必须能连到它。`cmd/telesrv-core` 不绑定 MTProto listener；不要把 Core 内部地址当作客户端 DC 地址。 |
| `TELESRV_ADVERTISE_IP` | string / `127.0.0.1` | 写入 `help.getConfig.DCOptions`、媒体与通话回退路径的客户端可达 IP。默认 loopback 只适合纯本机 TDesktop；Android、局域网或远端验证必须显式设为宿主机 LAN IP/公网 IP。Windows 本地重启优先用 `scripts\restart-local-server.ps1 -Listen 0.0.0.0:2398 -AdvertiseIP <client-reachable-ip>`，避免手动 `Start-Process` 漏掉环境变量。 |
| `TELESRV_ADVERTISE_PORT` | TCP port / `TELESRV_LISTEN` 端口 | 写入 `help.getConfig.DCOptions` 的客户端可达端口。`cmd/telesrv-edge` 默认跟随 `TELESRV_LISTEN`；`cmd/telesrv-core` 必须显式设为 Edge 对外端口，禁止把 Core 内部地址下发给客户端。 |
| `TELESRV_RSA_KEY` | path / `data/server_rsa.pem` | MTProto RSA 私钥；缺失时自动生成。属于敏感文件，重启和升级间必须稳定保存。 |
| `TELESRV_DC` | positive TL int32 / `2` | 服务端输出配置、媒体/DC 元数据和 `channelFull.stats_dc` 使用的规范 DC ID；必须在 `1..2147483647`，否则启动失败。当前单后端不会按它分区密钥交换状态。 |
| `TELESRV_DEFAULT_COUNTRY_CODE` | ISO alpha-2 / `CN` | `help.getNearestDc` 返回的登录页默认国家。客户端把 `CN` 映射为国际区号 `+86`、`US` 映射为 `+1`。输入会 trim、转大写并校验为国家或自治地区；格式错误或未知值会让启动失败。 |
| `TELESRV_STRICT_DC_CHECK` | bool / `false` | 默认 `false`，永久与临时密钥交换接受任意 wire int32 DC 标签。设为 `true` 时永久标签必须等于 `TELESRV_DC`、临时标签绝对值必须等于 `TELESRV_DC`；它仅是诊断开关，不提供多 DC 隔离。 |
| `TELESRV_INSTANCE_ID` | string / 空 | 当前进程在 Edge/Core 控制、Redis 在线位置索引与 pub/sub 去重中的实例标识。空值由进程启动时按 hostname/pid/time 生成；多实例部署建议显式配置稳定、唯一的值。 |
| `TELESRV_EDGE_LOCATION_TTL` | duration / `90s` | Edge 在线位置与 active raw-key lease 写入 Redis 的 TTL。必须大于零，并且大于心跳间隔；Core orphan-auth-key GC 读取该分布式 lease snapshot，读取失败会跳过删除。 |
| `TELESRV_EDGE_LOCATION_HEARTBEAT_INTERVAL` | duration / `30s` | 当前进程刷新在线 session 位置快照的周期。必须大于零且小于 `TELESRV_EDGE_LOCATION_TTL`。 |
| `TELESRV_GROUPCALL_CONTROL_ADDR` | address / 空 | Core 接收独立 SFU 媒体活性回报的内部 HTTP 监听地址；`cmd/telesrv-core` 必须配置。 |
| `TELESRV_GROUPCALL_CONTROL_URL` | URL / 空 | 独立 `telesrv-sfu` 调用 Core groupcall control API 的内部 URL。 |
| `TELESRV_GROUPCALL_CONTROL_TOKEN` | secret string / 空 | groupcall control API bearer token；Core 与独立 SFU 必须一致。`cmd/telesrv-core` 与独立 `cmd/telesrv-sfu` 都必须显式配置非空值。 |
| `TELESRV_CORE_EXEC_GRPC_ADDR` | address / 空 | `cmd/telesrv-core` 暴露生产 CoreExec gRPC API 的内部监听地址。Core 入口必须配置此项。 |
| `TELESRV_CORE_EXEC_GRPC_RESOLVER` | `static`/`dns` / `static` | Edge -> CoreExec gRPC 服务发现 provider。`static` 从 `TELESRV_CORE_EXEC_GRPC_TARGETS` 读取逗号分隔 endpoint 列表；`dns` 只读取一个 target：可以是裸 `host:port` 服务名，也可以是显式 gRPC DNS URI，例如 `dns:///telesrv-core.default.svc.cluster.local:2500`，或用于本地 canary 的 `dns://127.0.0.1:5353/coreexec.test:2500`。DNS mode 交给 gRPC DNS resolver 展开 A/AAAA 记录，适合 Kubernetes Service/headless DNS，且不需要新增 Consul/etcd/Nacos。非法值会阻止启动。 |
| `TELESRV_CORE_EXEC_GRPC_TARGETS` | resolver target(s) / 空 | `cmd/telesrv-edge` 必填的 CoreExec gRPC endpoint 输入。`static` 下每项必须是裸 `host:port`，端口必须是 `1..65535` 数字，URL scheme 会被拒绝，允许逗号分隔多个 endpoint 并去重；IPv6 必须使用 `[::1]:2440` 这类 bracket 形式。`dns` 下只允许一个 target：裸 `host:port` 会被归一为 `dns:///host:port`，显式 `dns:///name:port` / `dns://authority:port/name:port` URI 也允许；多 Core 地址必须由 DNS 记录返回，不能在 env 中逗号拼接。gRPC client 使用 bearer token、启动 unary health + `GetInfo` 协议版本/能力/实例 ID 握手、`round_robin`，并在 service config 中启用 gRPC client-side health checking；NOT_SERVING Core 不进入 dispatch picker。gRPC 自动 retry 显式关闭；transport timeout/unavailable 会分类到 metrics/log，Edge 侧 gRPC status 错误日志会携带 request_id、operation 与 outcome，但 request_id 不进入 metrics label；Edge 只保留私有 TL admission shell 做 profile 校验和 raw wire 捕获，CoreExec 不可用时没有本地业务 dispatch fallback；业务重放必须经 RPC execution/outbox 语义确认。不能只用单个写死 Core 地址作为生产门禁。 |
| `TELESRV_CORE_EXEC_GRPC_REQUEST_TIMEOUT` | duration / `5s` | Edge -> CoreExec gRPC unary 调用默认 deadline；仅当上游 ctx 没有 deadline 时补上。必须大于 `0` 且不超过 `1m`。覆盖 dispatch、profile evidence、initConnection observer 与 replay commit，避免 Core 故障时 Edge 执行槽无界挂住。 |
| `TELESRV_CORE_EXEC_GRPC_TLS_CERT_FILE` / `TELESRV_CORE_EXEC_GRPC_TLS_KEY_FILE` | path / 空 | CoreExec gRPC server TLS 证书与私钥；二者必须同时配置。为空时保持本地/开发默认明文 gRPC。 |
| `TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CA_FILE` | path / 空 | CoreExec gRPC server 用于验证 Edge client certificate 的 CA bundle；配置后启用 mTLS，并要求 server TLS cert/key 同时配置。 |
| `TELESRV_CORE_EXEC_GRPC_TLS_CA_FILE` | path / 空 | Edge gRPC client 信任 Core server certificate 的 root CA bundle；配置后客户端使用 TLS。 |
| `TELESRV_CORE_EXEC_GRPC_TLS_SERVER_NAME` | string / 空 | Edge gRPC client 校验 Core server certificate 时使用的 server name；静态 target 是 IP 或 LB 地址但证书签给内部 DNS 名时应配置。非空也会启用 TLS。 |
| `TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CERT_FILE` / `TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_KEY_FILE` | path / 空 | Edge gRPC client certificate 与私钥；二者必须同时配置。与 server 的 `TLS_CLIENT_CA_FILE` 配套时形成 mTLS。 |
| `TELESRV_CORE_EXEC_TOKEN` | secret string / 空 | CoreExec gRPC API bearer token；`cmd/telesrv-core` 与 `cmd/telesrv-edge` 都必须显式配置非空值，client 与 server 必须一致，且值不得包含换行。当前 gRPC 通道已支持静态多 endpoint 与 DNS resolver provider；后续可替换为 Envoy/xDS、Consul provider。当前不要配置独立 Consul/etcd/Nacos 组件来“提前解决”CoreExec 服务发现；该能力应保持在可替换 resolver 抽象里。Redis 仍只承担 Edge 在线位置、push/control ACK、短状态和 SFU owner，不作为 CoreExec 普通服务发现。 |
| `TELESRV_FILE_GRPC_ADDR` | address / 空 | 独立 `cmd/telesrv-file` 暴露 FileData gRPC API 的内部监听地址。File 入口必须配置此项。 |
| `TELESRV_FILE_GRPC_RESOLVER` | `static`/`dns` / `static` | Edge/Core -> FileData gRPC 服务发现 provider。规则与 CoreExec resolver 相同：`static` 读取逗号分隔 `host:port` endpoint，`dns` 只读取一个裸 `host:port` 或显式 `dns:///name:port` / `dns://authority:port/name:port` target。非法值阻止启动。 |
| `TELESRV_FILE_GRPC_TARGETS` | resolver target(s) / 空 | `cmd/telesrv-edge` 与 `cmd/telesrv-core` 必填的 FileData gRPC endpoint 输入。Edge 在打开 MTProto listener 前、Core 在暴露 CoreExec 前都会执行 health + `GetInfo` 握手；不可达、token 错误、协议版本不兼容或 FileData 后端不健康都会 fail fast。 |
| `TELESRV_FILE_GRPC_REQUEST_TIMEOUT` | duration / `10s` | Edge/Core -> FileData gRPC 调用默认 deadline；仅当上游 ctx 没有 deadline 时补上。必须大于 `0` 且不超过 `1m`。覆盖上传分片、静态 range download、blob put/get 与 upload parts materialize。 |
| `TELESRV_FILE_GRPC_TLS_CERT_FILE` / `TELESRV_FILE_GRPC_TLS_KEY_FILE` | path / 空 | FileData gRPC server TLS 证书与私钥；二者必须同时配置。为空时保持本地/开发默认明文 gRPC。 |
| `TELESRV_FILE_GRPC_TLS_CLIENT_CA_FILE` | path / 空 | FileData gRPC server 用于验证 Edge/Core client certificate 的 CA bundle；配置后启用 mTLS，并要求 server TLS cert/key 同时配置。 |
| `TELESRV_FILE_GRPC_TLS_CA_FILE` | path / 空 | Edge/Core gRPC client 信任 FileData server certificate 的 root CA bundle；配置后客户端使用 TLS。 |
| `TELESRV_FILE_GRPC_TLS_SERVER_NAME` | string / 空 | Edge/Core gRPC client 校验 FileData server certificate 时使用的 server name；非空也会启用 TLS。 |
| `TELESRV_FILE_GRPC_TLS_CLIENT_CERT_FILE` / `TELESRV_FILE_GRPC_TLS_CLIENT_KEY_FILE` | path / 空 | Edge/Core 调用 FileData gRPC 时使用的 client certificate 与私钥；二者必须同时配置。 |
| `TELESRV_FILE_TOKEN` | secret string / 空 | FileData gRPC API bearer token；`cmd/telesrv-file`、`cmd/telesrv-edge` 与 `cmd/telesrv-core` 都必须显式配置非空值，且不得包含换行。FileData 只承接上传分片、静态 blob range 和 materialize 数据面，不是消息/媒体权限权威。 |
| `TELESRV_EGRESS_ACK_GRPC_ADDR` | address / 空 | `cmd/telesrv-egress` 暴露 late client ACK 写回 API 的内部监听地址。Egress 入口必须配置此项；Edge fabric 服务级确认已足够推进 online outbox，该 API 只用于 indeterminate 写出后的迟到 `msgs_ack`，并同时支持 PTS `dispatch_outbox` 与 non-PTS `edge_delivery_outbox`（后者使用 `pts=0` fence）。 |
| `TELESRV_EGRESS_ACK_GRPC_RESOLVER` | `static`/`dns` / `static` | Edge -> Egress ACK gRPC 服务发现 provider。规则与 CoreExec resolver 相同：`static` 读取逗号分隔 `host:port` endpoint，`dns` 只读取一个裸 `host:port` 或显式 `dns:///name:port` / `dns://authority:port/name:port` target。非法值阻止启动。 |
| `TELESRV_EGRESS_ACK_GRPC_TARGETS` | resolver target(s) / 空 | `cmd/telesrv-edge` 必填的 Egress ACK gRPC endpoint 输入。Edge 在发布 MTProto listener 前会执行 health + `GetInfo` 握手；不可达、token 错误或协议版本不兼容都会 fail fast，避免客户端连接到无法推进 PTS outbox 或 non-PTS delivery queue ACK 的 Edge。 |
| `TELESRV_EGRESS_ACK_GRPC_REQUEST_TIMEOUT` | duration / `5s` | Edge -> Egress ACK gRPC unary 调用默认 deadline；仅当上游 ctx 没有 deadline 时补上。必须大于 `0` 且不超过 `1m`。 |
| `TELESRV_EGRESS_ACK_GRPC_TLS_CERT_FILE` / `TELESRV_EGRESS_ACK_GRPC_TLS_KEY_FILE` | path / 空 | Egress ACK gRPC server TLS 证书与私钥；二者必须同时配置。为空时保持本地/开发默认明文 gRPC。 |
| `TELESRV_EGRESS_ACK_GRPC_TLS_CLIENT_CA_FILE` | path / 空 | Egress ACK gRPC server 用于验证 Edge client certificate 的 CA bundle；配置后启用 mTLS，并要求 server TLS cert/key 同时配置。 |
| `TELESRV_EGRESS_ACK_GRPC_TLS_CA_FILE` | path / 空 | Edge gRPC client 信任 Egress ACK server certificate 的 root CA bundle；配置后客户端使用 TLS。 |
| `TELESRV_EGRESS_ACK_GRPC_TLS_SERVER_NAME` | string / 空 | Edge gRPC client 校验 Egress ACK server certificate 时使用的 server name；非空也会启用 TLS。 |
| `TELESRV_EGRESS_ACK_GRPC_TLS_CLIENT_CERT_FILE` / `TELESRV_EGRESS_ACK_GRPC_TLS_CLIENT_KEY_FILE` | path / 空 | Edge 调用 Egress ACK gRPC 时使用的 client certificate 与私钥；二者必须同时配置。 |
| `TELESRV_EGRESS_ACK_TOKEN` | secret string / 空 | Egress ACK gRPC API bearer token；`cmd/telesrv-edge` 与 `cmd/telesrv-egress` 都必须显式配置非空值，且不得包含换行。 |
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
| `TELESRV_MTPROTO_RPC_GLOBAL_MAX_BYTES` | int64 charge bytes / `536870912` | 进程级已预留/排队/执行中 RPC 内存 charge 预算；legacy 等于 copied body，exact 是 typed decode 前按 wire 与生成对象放大计算的保守 materialization charge。nested gzip 展开后会在 decoder 分配 typed graph 前原子增长该 charge，grow 失败原子拒绝整批候选 RPC；该值不代表可并发接收同等大小的 wire body。 |
| `TELESRV_MTPROTO_RPC_EXECUTION_MAX_ENTRIES` | int / `262144` | pending owner 与未 ACK execution receipt 的全局上限。receipt 只存请求身份、执行结果和 Layer admission 元数据，不存 TL body；收到 `msgs_ack` 立即删除，331 秒仅是无 ACK 时的安全上限。 |
| `TELESRV_MTPROTO_RPC_EXECUTION_AUTH_MAX_ENTRIES` | int / `32768` | 单 raw auth key 的 owner/receipt 条目上限；必须满足 `global >= auth >= session`。 |
| `TELESRV_MTPROTO_RPC_EXECUTION_SESSION_MAX_ENTRIES` | int / `16384` | 单 `raw auth key + session_id` 的 owner/receipt 条目上限。 |
| `TELESRV_MTPROTO_RPC_EXECUTION_PENDING_PER_AUTH` | int / `2048` | 单 raw auth key 的 active pending owner 附加上限；必须不大于 `RPC_GLOBAL_MAX_TASKS` 和 auth entry 上限。 |
| `TELESRV_MTPROTO_INBOUND_FRAME_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | transport wire、最大解密明文以及每个 live outer/nested gzip 输出的进程级在途预算，均在对应 payload 分配前预留。 |
| `TELESRV_MTPROTO_OUTBOUND_QUEUE_SIZE` | int / `128` | 单连接普通 outbound mailbox 容量。 |
| `TELESRV_MTPROTO_OUTBOUND_CONTROL_QUEUE_SIZE` | int / `32` | 单连接控制消息 mailbox 容量。 |
| `TELESRV_MTPROTO_OUTBOUND_TRACKED_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | 所有逻辑 session 未 ACK 出站 body 的唯一全局预算。物理连接重连复用同一份 `msg_id/seq_no/body`；ACK、destroy 或离线 6 分钟回收时释放，不再另建 RPC cache/spool 副本。 |
| `TELESRV_MTPROTO_OUTBOUND_WRITE_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | 并发加密 wire/codec/obfuscation scratch 的全局预算。 |

nested gzip admission 不新增环境变量。代码硬限制为：每个
`gzip_packed` envelope 输出最多 10 MiB；同一 transport frame 内 outer、nested、
sibling、失败尝试和 authoritative-profile re-decode 的累计解压工作最多 32 MiB。
live expanded buffer 释放后会归还进程内存，但不会返还该 frame 的 CPU/work counter；
保留的 typed graph 继续计入上面的 RPC scheduler 预算。

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
| `TELESRV_ADMIN_UI_PERMISSIONS` | comma-separated list / `*` | 使用 `TELESRV_ADMIN_UI_PASSWORD` / `_TOKEN` 登录的 Admin UI 会话权限集合。`*` 表示全部权限且是默认值。读取托管 bot token 需要显式 `bots.token.read` 权限；token 只在命令响应中返回，不会持久化到审计结果。权限名只允许字母、数字和 `._:-`，最多 64 字符，可用 `namespace.*` 授权整个命名空间。空列表或无法解析的权限名会让启动失败。 |
| `TELESRV_ADMIN_SCOPED_TOKENS` | 用 `;` 分隔的 `name:token:perm1,perm2` 条目 / 空 | 额外的 Admin API bearer token，每个 token 只携带指定权限，供集成服务按最小权限访问，避免使用无限制的 `TELESRV_ADMIN_API_TOKEN`。token 不能包含 `:` 或空白字符，每个条目必须至少列出一个权限，name/token 必须唯一，且禁止复用 `TELESRV_ADMIN_API_TOKEN`，否则会把受限 token 静默放大成全权限。任何格式错误都会让启动失败。 |
| `TELESRV_PUBLIC_BASE_URL` | HTTP(S) URL / `https://telesrv.net` | 客户端可见的公开链接根地址；允许 path，禁止 credentials、query、fragment。本地例：`http://127.0.0.1:2401`。 |
| `TELESRV_BRAND_PRODUCT_NAME` | string / `Telesrv` | 母品牌显示名；系统账号、登录通知、邀请、WebAuthn、内置 bot 与上游可见文案替换共用。trim 后非空、无控制字符、最多 64 个 Unicode 字符。 |
| `TELESRV_BRAND_PRODUCT_USERNAME` | username / `telesrv` | 777000 系统账号的公开 username；trim、去掉可选 `@` 后规范成小写，必须为 5–32 位 ASCII username 且首字符为字母。 |
| `TELESRV_BRAND_DESKTOP_APP_NAME` | string / `Telesrv Desktop` | `account.getAuthorizations` 返回的 Desktop/Windows 显示名；校验规则同产品名。 |
| `TELESRV_BRAND_ANDROID_APP_NAME` | string / `Telesrv Android` | 授权列表中的 Android 显示名；校验规则同产品名。 |
| `TELESRV_BRAND_IOS_APP_NAME` | string / `Telesrv iOS` | 授权列表中的 iOS 显示名；校验规则同产品名。 |
| `TELESRV_BRAND_MACOS_APP_NAME` | string / `Telesrv macOS` | 授权列表中的 macOS 显示名；校验规则同产品名。 |
| `TELESRV_BRAND_WEB_A_APP_NAME` | string / `Telesrv Web A` | `telegram-tt` / `weba` 授权列表显示名；持久化检测 token 不变。 |
| `TELESRV_BRAND_WEB_K_APP_NAME` | string / `Telesrv Web K` | `tweb` / `webk` 授权列表显示名；持久化检测 token 不变。 |
| `TELESRV_BRAND_PREMIUM_NAME` | string / `Telesrv Premium` | Premium 状态与相关客户端可见文案中的产品名；校验规则同产品名。 |
| `TELESRV_BRAND_STARS_NAME` | string / `Telesrv Stars` | Stars 支付 label 与相关客户端可见文案中的产品名；校验规则同产品名。 |
| `TELESRV_UPDATE_PUBLIC_URL` | nullable HTTP(S) URL / 空 | 客户端可见的 `cmd/telesrv-update` 根地址，通过 `help.getConfig.autoupdate_url_prefix` 下发；空值关闭 desktop 原生更新。详见 `docs/update-service.md`。 |
| `TELESRV_UPDATE_SERVICE_URL` | nullable HTTP(S) URL / `TELESRV_UPDATE_PUBLIC_URL` | `help.getAppUpdate` 使用的内部 resolver 地址，可指向 loopback/private route；空值回退到公开更新地址。 |
| `TELESRV_UPDATE_REQUEST_TIMEOUT` | duration / `2s` | application update resolver 调用超时，必须大于零且不超过 `30s`；resolver 故障返回 `help.noAppUpdate`，不会转成 RPC 500。 |
| `TELESRV_PUBLIC_APP_SCHEME` | URL scheme / `telesrv` | 落地页自动唤起客户端的 scheme，必须与 patched 客户端注册值一致；禁止 `tg`、`http`、`https`。 |
| `TELESRV_PUBLIC_APP_LINK_BASE` | nullable custom URL base / 空 | 多服务客户端可选的 host-based 根，例如 `owpg://example.com`。配置后生成 `owpg://example.com/oauth`、`owpg://example.com/<username>` 等；只允许精确 `<custom-scheme>://<host>`，禁止端口、path、query、fragment。`TELESRV_PUBLIC_APP_SCHEME` 仍作为旧链接输入兼容。 |
| `TELESRV_PUBLIC_WEB_BASE_URL` | HTTP(S) URL / `https://weba.telesrv.net` | username 页面 Web 客户端入口，校验规则同 `TELESRV_PUBLIC_BASE_URL`。 |
| `TELESRV_PUBLIC_APP_NAME` | string / `TELESRV_BRAND_PRODUCT_NAME` | 公开落地页产品名；trim 后非空、无控制字符、最多 64 个 Unicode 字符。 |
| `TELESRV_PUBLIC_LINK_WEB_ADDR` | nullable address / 空 | username/avatar/sticker/emoji/chatlist/collectible gift 落地页、hash-only 申诉，以及 unique gift / channel revenue 短期令牌确认页监听；空值关闭，并使本地 gift export 与频道收益领取 fail-closed。公开落地页保持只读，只有精确 token POST 可提交对应 aggregate。生产应 loopback + nginx 精确反代并关闭 token 路径 access log；本地可在 `configs/core.yaml` 中启用 `127.0.0.1:2401`。 |
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
| Bot API base URL | bot 应用的 `TELEGRAM_API_URL` 等配置 | bot 应用访问 telesrv 的可达地址，例如 `http://172.17.0.1:8081`。方法地址为 `<base>/bot<TOKEN>/<method>`，文件地址为 `<base>/file/bot<TOKEN>/<file_path>`。 |
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
`0.0.0.0:8081`，同宿主 Docker 容器可能使用 `http://172.17.0.1:8081`；不要请求
`http://0.0.0.0:8081`。

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
# 多服务客户端可选：TELESRV_PUBLIC_APP_LINK_BASE=owpg://example.com

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
curl.exe -I 'http://192.0.2.25:2401/js/telegram-widget.js?23'
```

discovery 返回的 `issuer` 必须等于配置值，`authorization_endpoint`、`token_endpoint`
和 `jwks_uri` 必须可从依赖方访问。使用反向代理时需原样转发
`/.well-known/openid-configuration`、`/.well-known/jwks.json`、`/auth`、`/auth/status`、
`/token`、`/crossapp`、`/inapp`、`/telegram-login.js`、`/js/telegram-login.js`、
`/telegram-widget.js`、`/js/telegram-widget.js` 以及 `POST /telegram-widget/resolve`。

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

已有前端若使用 Telegram 的 attribute-based Login Widget，只替换 widget script 的
来源；仅存在于 telesrv 的 bot 不得继续使用
`https://telegram.org/js/telegram-widget.js`：

```html
<script async src="https://<telesrv-issuer>/js/telegram-widget.js?23"
  data-telegram-login="BotName"
  data-size="large"
  data-radius="10"
  data-request-access="write"
  data-lang="zh"
  data-onauth="onTelegramAuth(result)"></script>
<script>
  function onTelegramAuth(result) {
    // telesrv SDK 返回：{ id_token, user }，失败时为 { error }
  }
</script>
```

同时提供 `data-client-id="<数字 bot user id>"` 时优先使用该值；只有
`data-telegram-login` 时，shim 仅在本服 username 当前指向 bot、其 Login client 已
启用且浏览器精确 Origin 已为该 bot 登记时返回 `client_id`。`data-size` / `data-radius`
控制本地简单按钮，`data-lang` 透传给 Login SDK；`data-request-access="write"` 请求
`openid profile telegram:bot_access`，未设置时请求 `openid profile`。`data-onauth`
收到现有 telesrv SDK 的 `{id_token, user}`，不会伪造 Telegram 官方 widget payload。

Mini App 是独立接口，必须继续加载官方
`https://telegram.org/js/telegram-web-app.js`。telesrv 的 `telegram-widget.js` shim 不
替换该脚本，也不改变 Mini App 行为。

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
| `TELESRV_POSTGRES_DSN` | secret DSN / `postgres://telesrv:telesrv@127.0.0.1:5432/telesrv_v2?sslmode=disable` | 主业务持久库；本地 `main` / `v2` 因迁移历史不同，分别使用 `telesrv_main` / `telesrv_v2`；生产必须替换开发凭证与 TLS 策略。 |
| `TELESRV_POSTGRES_MAX_CONNS` | int / `50` | pgxpool 最大连接数；`<=0` 使用 pgx 默认值，该默认通常不足以覆盖生产 outbox/RPC 并发。 |
| `TELESRV_POSTGRES_MIN_CONNS` | int / `16` | pgxpool 预热最小连接数。 |
| `TELESRV_REDIS_ADDR` | address / `127.0.0.1:6399` | 验证码、限流、共享更新/缓存易失态使用的 Redis。 |
| `TELESRV_REDIS_PASSWORD` | secret string / 空 | Redis 密码。 |
| `TELESRV_REDIS_DB` | int / `0` | Redis 逻辑库编号。 |
| `TELESRV_LANGPACK_SEED_DIR` | path / `data/langpack` | TDesktop `.strings` 语言包 seed 目录。 |
| `TELESRV_OFFICIAL_GIFTS_DIR` | path / `data/official-gifts` | `cmd/giftfetch` 生成的只读官方礼物快照；供管理后台选择、验哈希并显式导入。 |
| `TELESRV_BLOB_BACKEND` | enum / `localfs` | 唯一永久媒体后端：`localfs` 或 `s3`。不会跨后端回退、双读或双写；库内已有另一 backend 的 `file_blobs` 时启动失败。 |
| `TELESRV_BLOB_DIR` | path / `data/blobs` | `localfs` 永久媒体与临时上传分片根目录；`s3` 模式不会从此目录回退读取。 |
| `TELESRV_BLOB_STAGING_DIR` | path / `data/blob-staging` | `s3` 模式的本地临时上传分片与写入 spool；不是永久后端。 |
| `TELESRV_S3_ENDPOINT` | host[:port] / 空 | `s3` 模式必填，不含 `http://`/`https://`。 |
| `TELESRV_S3_REGION` | string / `us-east-1` | S3 region。 |
| `TELESRV_S3_BUCKET` | string / 空 | `s3` 模式必填的永久媒体 bucket。 |
| `TELESRV_S3_ACCESS_KEY_ID` | secret / 空 | `s3` 模式必填；无弱默认凭据。 |
| `TELESRV_S3_SECRET_ACCESS_KEY` | secret / 空 | `s3` 模式必填；不得写入日志。 |
| `TELESRV_S3_USE_SSL` | bool / `true` | S3 endpoint 使用 TLS。 |
| `TELESRV_S3_PATH_STYLE` | bool / `false` | 强制 path-style bucket 地址；本地 MinIO 常设为 `true`。 |
| `TELESRV_S3_CREATE_BUCKET` | bool / `false` | 仅显式为 `true` 时允许启动创建缺失 bucket；否则缺桶即失败。 |
| `TELESRV_STORAGE_LOW_SPACE_GUARD_ENABLE` | bool / `true` | 启用存储容量门禁；启动时必须先取得有效快照，运行期刷新失败保留最后一个有效快照并告警。 |
| `TELESRV_STORAGE_MIN_FREE_BYTES` | int64 / `1073741824` | 本地文件系统最小剩余字节。`localfs` 保护永久文件与分片；`s3` 保护本地分片和 S3 写入 spool。`0` 关闭该维度。 |
| `TELESRV_STORAGE_MAX_TOTAL_BYTES` | int64 / `0` | S3 永久后端预算，按 `file_blobs` 中去重后的唯一 `object_key` 字节统计；`0` 不限制。它不是 bucket 实际账单用量。 |
| `TELESRV_STORAGE_USAGE_REFRESH_INTERVAL` | duration / `1m` | 容量快照刷新周期；容量门禁启用时必须为正数。 |
| `TELESRV_STICKER_SEED_DIR` | path / `data/sticker-seed` | 导入 documents、sticker sets、blob 的贴纸/reaction seed 目录。 |
| `TELESRV_STICKER_SEED_MAX_SETS` | int / `300` | 启动时导入的常规贴纸集上限；`<=0` 表示不限。 |
| `TELESRV_GIF_SEED_DIR` | path / `data/gifs` | 可选的内置 `@gif` 启动导入目录，只接受 `.gif/.mp4`，目录总量最多 50；缺失时跳过，同文件名内容变化或无效媒体会阻止启动。产物只写当前显式 blob backend。 |
| `TELESRV_PREMIUM_PROMO_SEED_DIR` | path / `data/premium-promo` | `help.getPremiumPromo` 导出的 manifest、MP4 视频与 JPEG 缩略图目录。目录缺失时保留无视频兼容响应；目录存在但内容非法或不完整时启动失败。 |

语言包 seed 以文件 manifest 为事实源。新增语言时放入 `data/langpack/<pack>/<pack>_<lang>_v<version>.strings` 并重启 `telesrv`；`pack` 必须与所在一级目录一致，允许 Telegram 已使用的字母、数字、`-` 与 `_`（例如 `android_x`），`lang` 会统一为小写、连字符形式（例如 `pt_BR` 归一为 `pt-br`）。同一语言存在多个文件时只读取最高版本。修改已有语言的有效内容必须提高版本；同版本有效内容变化或版本倒退会阻止启动。删除语言文件或整个 pack 子目录后，下次重启会原子移除对应数据库目录和字符串。启动先流式计算源文件 SHA-256；未变化文件复用上次原子 manifest，不解析字符串也不写库，只有新增或变化文件才解析并通过 PostgreSQL `COPY` 整包替换。

对象存储保持单后端运行。在显式离线迁移完成对象复制、size/SHA-256 校验并更新 `file_blobs.backend` 之前，不得直接翻转 `TELESRV_BLOB_BACKEND`。

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
| `TELESRV_SMTP_FROM_NAME` | string / `TELESRV_BRAND_PRODUCT_NAME` | 登录邮件展示的发件人名称；未显式配置时继承母品牌显示名。 |
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
| `TELESRV_AUTH_USER_CACHE_TTL` | duration / `1m` | 正向 auth-key→user 授权缓存复核周期。写侧 Redis invalidation 仍是主路径；TTL 用于限制跨 Core 失效/控制消息遗漏后的旧授权陈旧窗口。`<=0` 关闭 TTL 兜底。 |
| `TELESRV_AUTH_KEY_CACHE_MAX_ENTRIES` | int / `262144` | Edge raw auth-key 热缓存容量。Edge 不连接 PostgreSQL；CoreExec/PG 是 auth key 权威，Edge 只缓存 key material、server salt 和 expiry，不缓存 Layer/client metadata。必须大于 `0`。 |
| `TELESRV_AUTH_KEY_CACHE_TTL` | duration / `30m` | Edge auth-key 热缓存资源 TTL；只用于限制内存占用。`destroy_auth_key` 通过 Core 发布 Redis 失效事件并关闭 raw-key sessions，正确性不依赖 TTL。必须为 `(0,24h]`。 |
| `TELESRV_CHANNEL_ROW_CACHE_MAX` | int / `50000` | 共享 channel row 缓存容量；`<=0` 同时关闭缓存及 LISTEN/NOTIFY listener。 |
| `TELESRV_CHANNEL_MEMBER_CACHE_MAX` | int / `100000` | channel member/access read-model 缓存容量；`<=0` 关闭。 |
| `TELESRV_CHANNEL_DIALOG_CACHE_MAX` | int / `100000` | viewer/channel dialog 投影缓存容量；`<=0` 关闭。 |
| `TELESRV_CHANNEL_BOOST_CACHE_MAX` | int / `100000` | channel boost read-model 缓存容量；`<=0` 关闭。 |
| `TELESRV_CHANNEL_BOOST_CACHE_TTL` | duration / `10s` | boost 失效通知遗漏时允许的最大陈旧窗口。 |

## 9. Outbox、推送、限流、retention 与 GC

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_OUTBOX_WORKERS` | int / `4` | `cmd/telesrv-egress` 并发 outbox worker 数；稳定逻辑分片保持 PTS `dispatch_outbox` 与 non-PTS `edge_delivery_outbox` 的单用户队头顺序。 |
| `TELESRV_OUTBOX_BATCH` | int / `100` | `cmd/telesrv-egress` 每批最大 claim 行数；增大提高吞吐，也增加 DB/推送突发。 |
| `TELESRV_OUTBOX_LEASE_TIMEOUT` | duration / `30s` | `dispatching` 行可被其它 Egress 重新 claim 的超时；必须大于最坏单批投递耗时。 |
| `TELESRV_OUTBOX_POISON_RETENTION` | duration / `1m` | terminal failed 投递头的排障保留窗口；durable update 仍可经 difference 恢复。 |
| `TELESRV_OUTBOX_POISON_CLEANUP_INTERVAL` | duration / `15s` | terminal failed head 清理周期，独立于大表 retention。 |
| `TELESRV_OUTBOUND_PUSH_TIMEOUT` | duration / `200ms` | Egress 等待 Edge 确认在线 update 入队的最长时间；超时视为未确认投递，保留 durable outbox 等待 lease/reclaim。 |
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
| `TELESRV_PREMIUM_BOT_USERNAME` | username / `premiumbot` | 内置 Premium 商店 bot 的保留 username；开头的 `@` 会被移除，非法值会使启动失败。 |
| `TELESRV_PREMIUM_BOT_USER_ID` | int64 / `1250000015` | 内置 Premium bot 的稳定用户 ID；必须为正且不能与其他系统账号冲突。 |
| `TELESRV_PREMIUM_PLANS` | `months:days:stars` CSV / `3:90:750,6:180:1300,12:365:2400` | 初始化并同步由配置管理的 Premium 套餐；月份不得重复，所有值必须为正且在边界内。在管理界面保存后，该行转为管理员管理，后续重启不会覆盖它。目录变更会提升持久化版本，使旧价格 form 失效。 |
| `TELESRV_PREMIUM_GRANT_MONTHS` | int / `3` | 新注册账号默认 Premium 月数；`0` 关闭新赠送，不影响已有迁移 backfill。 |
| `TELESRV_STARS_STARTING_GRANT` | int64 / `1000` | 对所有账号幂等惰性授予的 Stars 起始余额；`0` 关闭自动赠送。 |
| `TELESRV_PREMIUM_SWEEP_INTERVAL` | duration / `1m` | 过期 Premium 清理/推送周期；读取路径独立即时派生到期状态。 |
| `TELESRV_PREMIUM_SWEEP_BATCH` | int / `500` | 单次 sweep 最大处理行数。 |
| `star_gifts.sweep_interval` | duration / `15s` | Core 的 Star Gift 报价/竞拍本地生命周期清扫周期。 |
| `star_gifts.sweep_batch` | int / `1000` | 单次礼物生命周期清扫最多处理的报价、竞拍与 outbox 工作量。 |
| `star_gifts.ton_starting_grant` | int64 / `10000000000` | 每个用户首次访问 telesrv 内部 TON 账本时幂等授予的 nanoton；`0` 关闭赠送。它不是链上资产。 |
| `star_gifts.transfer_stars` | int64 / `25` | collectible 转赠费用；设为 `0` 时使用免费转赠 RPC。 |
| `star_gifts.drop_original_details_stars` | int64 / `25` | 移除 collectible 原始发送者/附言信息所需 Stars。 |
| `star_gifts.offer_min_stars` | int / `1` | collectible 签发时固化的用户持有礼物最低 Stars 报价；`0` 不开放报价入口。 |
| `star_gifts.stars_proceeds_permille` | int / `1000` | Stars 成交时卖方实收比例（千分比）；差额作为平台佣金写入成交记录。 |
| `star_gifts.ton_proceeds_permille` | int / `1000` | 内部 TON 成交时卖方实收比例（千分比）；只影响本地账本。 |
| `star_gifts.export_delay` | duration / `0s` | collectible 签发时固化到 `can_export_at` 的等待期。 |
| `star_gifts.transfer_delay` | duration / `0s` | 签发时固化到 `can_transfer_at` 的等待期。 |
| `star_gifts.resell_delay` | duration / `0s` | 签发时固化到 `can_resell_at` 的等待期。 |
| `star_gifts.craft_delay` | duration / `0s` | 签发时固化到 `can_craft_at` 的等待期；可 Craft 礼物即使为 `0s` 也写升级时间这一正数能力边界，0 只表示不具备 Craft 能力或已终结。 |
| `star_gifts.craft_chance_permille` | int / `250` | 每份输入礼物贡献的本地合成成功概率，累计上限 1000‰。 |
| `star_gifts.export.mode` | `local` / `ton` / `disabled`; 默认 `local` | `local` 保持现有 same-origin 礼物导出；`disabled` 只关闭礼物导出；`ton` 交给独立 TON worker。三个模式都不改变频道 Stars/内部 TON 收益领取 provider。主网 worker 未满足 admission 时必须 fail-closed，不能回退 local。 |

Core 的 `star_gifts.export.ton` 仅在 `mode: ton` 时启用正式链路：

| YAML 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `network` | `mainnet` / `testnet`; 默认 `mainnet` | Core intent、TON Proof 和 worker 必须使用同一网络；生产必须显式填写。 |
| `collection_address` | string / 必填 | 固定 TEP-62 collection 地址，必须与 worker 配置一致。 |
| `collection_code_hash` | 32-byte hex/Base64 / 必填 | collection code hash；Core 会与 worker 链上预检后写入的 durable admission 比较。 |
| `mint_abi` | `basic-collection-v1` / 必填 | TON reference collection 的单笔 mint ABI（opcode `0x00000001`）。TEP-62 不规定 mint opcode，其他值启动失败。 |
| `initial_item_index` | uint64 十进制字符串 / 必填 | telesrv 分配 item index 的起点；上线后不得回退或复用。 |
| `proof_domain` | hostname / 必填 | TON Connect proof domain；必须与公开 Claim/Export Web 域名一致。 |
| `capability_secret_file` | path / 必填 | 文件内容为 Base64，解码后必须为 32--64 bytes；只用于 capability HMAC，不得与 bot token 或 relayer 助记词复用。 |
| `allow_user_ids` | positive int64 sequence / 空 | 空表示允许所有合格 user 创建新 TON intent；非空是主网灰度白名单。重复、0 或负值使 Core 启动失败。它不停止已有 intent 的确认/finalize/owner sync。 |
| `ttl` | duration / `30m` | Export capability 有效期，范围 `5m..24h`。 |
| `challenge_ttl` | duration / `5m` | Export/Claim TON Proof challenge 有效期，范围 `1m..10m`。 |
| `finalizer.batch` | int / `20` | Core 每轮最多领取的 finalize/sync-owner job，范围 `1..100`。 |
| `finalizer.poll_interval` | duration / `2s` | Core finalizer 轮询周期，必须为正。 |
| `finalizer.lease_timeout` | duration / `30s` | Core job lease，范围 `10s..1h`。 |
| `finalizer.request_timeout` | duration / `10s` | 单 job 超时，至少 `1s` 且短于 lease。 |
| `finalizer.retry_delay` | duration / `5s` | 可重试失败的退避，必须为正。 |
| `claim.enabled` | bool / `false` | 启用 `/ton-gift/claim` Mini App。 |
| `claim.bot_token_file` | path / claim 启用时必填 | 文件必须只有一个当前 active local bot token。Core 会校验 bot ID/secret，并幂等设置该 bot 的 main/side/attach menu app；不匹配则启动失败。 |
| `claim.init_data_ttl` | duration / `15m` | Telegram Mini App `tgWebAppData` 最大年龄，范围 `1m..24h`。 |

独立 worker 使用 `cmd/telesrv-ton --config configs/ton.yaml`：

| YAML 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `ton.network` | `mainnet` / `testnet`; 必填 | 必须与 Core intent 网络一致。 |
| `ton.lite_servers[]` | address + public_key; 至少 1 个 | 固定 lite endpoint 与公钥；重复或空值使启动失败。 |
| `ton.trusted_block` | masterchain block ID | 必须提供 `workchain: -1`、非零 shard/seqno、root hash 和 file hash。 |
| `ton.collection.address` / `code_hash` | string / 必填 | 启动 preflight 校验 collection active 和 code hash；每个 job 再校验 export collection。 |
| `ton.collection.mint_abi` | `basic-collection-v1` / 必填 | 与 Core、export 和 durable admission 一致的固定 mint payload ABI。 |
| `ton.collection.initial_item_index` | uint64 十进制字符串 / 必填 | 首次上线必须等于链上 `next_item_index`，之后作为不可变准入基线；实际编号只在 wallet proof 成功时分配。 |
| `ton.relayer.mnemonic_file` | path / mint 启用时必填 | 只允许 24-word v4r2 relayer，文件不得进入仓库。read-only worker 可省略。 |
| `ton.relayer.min_balance` | TON decimal / `1` | mint preflight 的余额保留线，必须大于 0。 |
| `ton.relayer.message_ttl` | duration / `15m` | 签名外部消息有效期，至少为 request timeout 两倍且不超过 `1h`。 |
| `ton.mint.enabled` | bool / `false` | `false` 撤销新 intent/proof 的 durable admission并停止 mint job；已分配 index 的 resolve、confirm 和 reconcile owner 仍可继续。这是事故停写开关。 |
| `ton.mint.send_amount` / `forward_amount` | TON decimal / `0.05`、`0.01` | collection 消息和 item forward 金额，最多 9 位小数；send amount 必须大于 0。 |
| `ton.mint.finality_depth` | int / `12` | masterchain finality 深度，范围 `1..1000`。 |
| `ton.worker.batch` | int / `10` | 每轮 job 数，范围 `1..100`。 |
| `ton.worker.poll_interval` | duration / `2s` | 范围 `100ms..1m`。 |
| `ton.worker.lease_timeout` | duration / `2m` | 范围 `10s..1h`。 |
| `ton.worker.request_timeout` | duration / `90s` | 至少 `1s` 且短于 lease。 |

完整无密钥样例见 `configs/examples/core.ton-mainnet.yaml` 和 `configs/examples/ton.mainnet.yaml`。生产启用前必须完成
固定 checkpoint/code hash、testnet、停写、单用户 canary、对账与回滚门禁，不能直接按样例开启 mint。

### 本地账号评分与 collectible username

账号评分是 gramsrv 自己的本地风控/信誉复合分，组合 Stars 收支、账号活跃和管理处罚；它不承诺 1:1 复刻 Telegram 的私有评分算法。已计算的本地等级会投影到 `userFull.stars_rating`，本人还会收到 `stars_my_pending_rating` 与生效时间，让官方客户端直接显示；他人的 pending 永不下发。资料页只读取后台 worker 已持久化的评分并复用现有 30 分钟 `userFull` 投影缓存，不会同步重算或写库。Collectible username 由管理员签发，本功能不访问外部市场、钱包或区块链节点。

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_RATING_ENABLED` | bool / `true` | 启用本地复合评分及客户端等级投影；关闭时拒绝评分写入且客户端 rating flags 保持未设置。 |
| `TELESRV_RATING_PENDING_DELAY` | duration / `24h` | 本地评分上涨进入可见后台等级前的等待期；下降立即生效。允许 `0..720h`，`0` 表示立即应用。 |
| `TELESRV_RATING_RECOMPUTE_INTERVAL` | duration / `15m` | 后台重算周期，必须为正数。 |
| `TELESRV_RATING_RECOMPUTE_BATCH` | int / `500` | 每轮重算的 stale projection 数，必须为 `1..10000`。 |
| `TELESRV_RATING_STALE_AFTER` | duration / `6h` | 超过该年龄的评分进入重算，必须为正数。 |
| `TELESRV_RATING_WEIGHT_STARS_RECEIVED_PERMILLE` | int64 / `1000` | Stars 收入权重（千分比）。 |
| `TELESRV_RATING_WEIGHT_STARS_SPENT_PERMILLE` | int64 / `250` | Stars 支出权重（千分比）。 |
| `TELESRV_RATING_WEIGHT_MESSAGE_SENT` | int64 / `1` | 每条已发送消息贡献分。 |
| `TELESRV_RATING_WEIGHT_ACCOUNT_AGE_DAY` | int64 / `2` | 每个账号存续日贡献分。 |
| `TELESRV_RATING_WEIGHT_GIFT_RECEIVED` | int64 / `25` | 每份持有 collectible gift 贡献分。 |
| `TELESRV_RATING_WEIGHT_MODERATION_CASE` | int64 / `150` | 每个成立管理案件的扣分幅度。 |
| `TELESRV_RATING_WEIGHT_SCAM_PENALTY` | int64 / `5000` | scam 标记固定扣分。 |
| `TELESRV_RATING_WEIGHT_FAKE_PENALTY` | int64 / `5000` | fake 标记固定扣分。 |
| `TELESRV_RATING_ACTIVITY_CAP` | int64 / `5000` | 活跃分上限；`0` 表示不封顶。 |
| `TELESRV_COLLECTIBLE_USERNAME_URL_TEMPLATE` | URL template / 空 | 管理员未显式给 URL 时写入资产的落地页模板；空值派生 `<TELESRV_PUBLIC_BASE_URL>/nft/username/<username>`。配置值须为无 userinfo 的绝对 http(s) URL，可含 `{username}`；无占位符时把 username 追加为最后路径段。 |

`fragment.collectibleInfo.amount` 与 `crypto_amount` 使用币种最小单位：例如 USD 1000 表示 10 美元，TON 900 表示 900 nanotons，XTR 无子单位。后台 UI 负责整币单位与最小单位转换；直接调用 Admin API 的集成必须自行传最小单位。

### 官方平台认证

官方认证对应 `user.verified` / `channel.verified`。申请由内置 `@verifybot` 收集、管理后台审核；提交和批准时都会重新校验目标存在、公开 username、申请者控制权、账号状态与系统账号禁入，badge 写入和决定状态在同一事务提交。申请 URL 只做 public http(s) 形状校验，服务端不会抓取，避免 SSRF。

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_VERIFICATION_ENABLED` | bool / `true` | 启用官方认证；关闭时拒绝新操作，既有 badge 保留。 |
| `TELESRV_VERIFICATION_ALLOW_USER_TARGETS` | bool / `false` | 是否允许普通用户账号成为认证目标。 |
| `TELESRV_VERIFICATION_REJECT_COOLDOWN` | duration / `720h` | 同申请者/目标被拒后的等待期；允许 `0..8760h`。 |
| `TELESRV_VERIFICATION_APPLY_RATE_LIMIT` | int / `3` | 每个申请者在窗口内可新建的申请数；`0` 关闭。 |
| `TELESRV_VERIFICATION_APPLY_RATE_WINDOW` | duration / `24h` | 申请预算窗口；limit>0 时必须为正数。 |
| `TELESRV_VERIFICATION_BOT_RATE_LIMIT` | int / `30` | `@verifybot` 每个申请者的对话限流；`0` 关闭。 |
| `TELESRV_VERIFICATION_BOT_RATE_WINDOW` | duration / `1m` | bot 对话限流窗口；limit>0 时必须为正数。 |
| `TELESRV_VERIFICATION_NOTIFY_INTERVAL` | duration / `15s` | durable 通知 outbox worker 周期，必须为正数。 |
| `TELESRV_VERIFICATION_NOTIFY_BATCH` | int / `50` | 每轮投递通知数，必须为 `1..500`。 |
| `TELESRV_BROADCAST_WORKER_INTERVAL` | duration / `3s` | 系统广播 worker 周期，必须为正数。 |
| `TELESRV_BROADCAST_WORKER_LEASE` | duration / `30s` | recipient 崩溃恢复租约，必须为正数且不超过 `1h`。 |
| `TELESRV_BROADCAST_MATERIALIZE_BATCH` | int / `200` | 单次数据库 keyset 物化的全体用户上限，必须为 `1..1000`；ID 不离开 PostgreSQL。 |
| `TELESRV_BROADCAST_DELIVERY_BATCH` | int / `50` | 每轮领取并投递的 recipient 上限，必须为 `1..500`；每条消息独立提交。 |
| `TELESRV_VERIFICATION_MAX_ACTIVE_PER_USER` | int / `3` | 每个申请者可保持的 active 申请数；允许 `0..50`。 |

### 第三方 bot 认证

第三方认证对应 `botVerification`，由管理员授权的 verifier bot 使用自己的 custom emoji document 与描述标记 user/channel，不会授予官方 checkmark。Icon 必须是客户端可通过 `messages.getCustomEmojiDocuments` 读取的真实 document。每个 peer 只保留一个 wire-visible mark；新的 verifier 替换旧 mark，禁止旧 badge 在撤销或 kill switch 后意外复活。客户端自定义描述上限通过 appConfig `bot_verification_description_length_limit=70` 发布。

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_BOT_VERIFICATION_ENABLED` | bool / `true` | 启用第三方认证；关闭时拒绝新 mutation，既有 mark 仍投影。 |
| `TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER` | int / `10000` | 单 verifier 可标记的 peer 数；`0` 仅关闭 service cap，storage 硬上限仍为 10000。 |
| `TELESRV_BOT_VERIFICATION_REQUEST_RATE_LIMIT` | int / `5` | 每个申请者跨 verifier 的申请预算；`0` 关闭。 |
| `TELESRV_BOT_VERIFICATION_REQUEST_RATE_WINDOW` | duration / `24h` | 第三方认证申请预算窗口；limit>0 时必须为正数。 |

## 11. 私聊通话、群通话、TURN、SFU 与直播

| 参数 | 类型 / 代码默认值 | 说明与约束 |
|---|---|---|
| `TELESRV_CALL_RING_TIMEOUT` | duration / `90s` | 私聊通话 ringing/accepted 服务端兜底超时，应与客户端 `callRingTimeoutMs` 保持一致。 |
| `TELESRV_CALL_TOMBSTONE_TTL` | duration / `60s` | 终态通话 tombstone 的幂等/晚到 RPC 吸收窗口。 |
| `TELESRV_CALL_MAX_ACTIVE_PER_USER` | int / `4` | 单用户非终态私聊通话上限；非正值由 phone service 归一。 |
| `TELESRV_CALL_REGISTRY_MAX_ENTRIES` | int / `10000` | 进程级私聊通话 registry 硬上限；满载返回 `CALL_OCCUPY_FAILED`，不按年龄驱逐已建立通话。 |
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
| `TELESRV_SFU_UDP_PORT` | int / `12399` | Pion ICE UDPMux 端口，必须放行防火墙。 |
| `TELESRV_SFU_ADVERTISE_IP` | string / 空 | 下发给客户端的 ICE candidate IP；空值回退 `TELESRV_ADVERTISE_IP`，loopback 会静默破坏真机媒体。 |
| `TELESRV_SFU_OWNER_TTL` | duration / `2m` | `callID -> sfu instance` owner 租约 TTL；多 SFU 下同一 call 必须粘同一 owner。 |
| `TELESRV_SFU_OWNER_HEARTBEAT_INTERVAL` | duration / `30s` | 独立 SFU active room 刷新 owner 租约的周期，必须小于 owner TTL。 |
| `TELESRV_SFU_INSTANCE_TTL` | duration / `90s` | 可用 SFU 媒体实例注册到 Redis 的 TTL；Core 只会选择未过期实例，并在选择前通过 gRPC health + `GetInfo.instance_id` 探测候选 control。 |
| `TELESRV_SFU_INSTANCE_HEARTBEAT_INTERVAL` | duration / `30s` | `telesrv-sfu` 刷新 instance registry 的周期，必须小于 instance TTL。 |
| `TELESRV_SFU_INSTANCE_HEALTH_TIMEOUT` | duration / `1s` | Core remote owner selector 对单个候选 SFU gRPC control 健康探测的上限；超时仅视为该候选不可用并继续检查其它未满载实例，避免 Redis TTL 窗口内的半故障 SFU 阻塞 join。 |
| `TELESRV_SFU_INSTANCE_MAX_ACTIVE_CALLS` | int / `0` | 单个 SFU instance 可承载的 active call 上限；`0` 表示不限制。heartbeat 会发布当前 active call 数，Core remote owner selector 跳过已满实例和健康探测失败实例，并在可用实例中优先选择低负载者。 |
| `TELESRV_SFU_CONTROL_GRPC_ADDR` | address / 空 | 独立 `cmd/telesrv-sfu` 暴露 SFU owner-control gRPC API 的内部监听地址；`cmd/telesrv-sfu` 必须配置。 |
| `TELESRV_SFU_CONTROL_GRPC_URL` | gRPC target / 空 | 写入 SFU instance/owner record 的可达 gRPC control 地址；为空回退 `TELESRV_SFU_CONTROL_GRPC_ADDR`。可用裸 `host:port` 或 `grpc://host:port`。 |
| `TELESRV_SFU_CONTROL_GRPC_REQUEST_TIMEOUT` | duration / `5s` | Core -> SFU gRPC control unary 调用默认 deadline；必须大于 `0` 且不超过 `1m`。 |
| `TELESRV_SFU_CONTROL_GRPC_TLS_CERT_FILE` / `TELESRV_SFU_CONTROL_GRPC_TLS_KEY_FILE` | path / 空 | SFU gRPC control server TLS 证书与私钥；二者必须同时配置。 |
| `TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CA_FILE` | path / 空 | SFU gRPC control server 信任的 client CA；配置后要求并验证 Core/probe client certificate，必须同时配置 server cert/key。 |
| `TELESRV_SFU_CONTROL_GRPC_TLS_CA_FILE` | path / 空 | Core/probe 调 SFU gRPC control 时信任的 root CA bundle。 |
| `TELESRV_SFU_CONTROL_GRPC_TLS_SERVER_NAME` | string / 空 | Core/probe gRPC client 校验 SFU control server 证书时使用的 server name。 |
| `TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CERT_FILE` / `TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_KEY_FILE` | path / 空 | Core/probe 调 SFU gRPC control 时使用的 client certificate；二者必须同时配置，配合 `*_CLIENT_CA_FILE` 启用 mTLS。 |
| `TELESRV_SFU_CONTROL_TOKEN` | secret string / 空 | SFU control API bearer token；非空时远端 client 必须使用同一值。独立 `cmd/telesrv-sfu` 必须显式配置非空值，避免生产媒体控制面无鉴权启动。 |
| `TELESRV_LIVESTREAM_ENABLE` | bool / `true` | 启用频道 RTMP ingest 与 ffmpeg 切段。 |
| `TELESRV_LIVESTREAM_RTMP_ADDR` | address / `:2400` | RTMP ingest TCP 监听地址。 |
| `TELESRV_LIVESTREAM_RTMP_URL` | URL string / 空 | 返回 OBS 的服务器地址；空值派生 `rtmp://<AdvertiseIP>:2400/live`。 |
| `TELESRV_LIVESTREAM_FFMPEG_PATH` | path/command / `ffmpeg` | ffmpeg 可执行文件路径，默认从 `PATH` 解析。 |
| `TELESRV_LIVESTREAM_WORK_DIR` | path / 空 | segment 临时工作目录；空值使用系统临时目录。 |
| `TELESRV_LIVESTREAM_SEGMENT_KEEP` | int seconds / `32` | 每路直播在内存保留的 segment 秒数/窗口；非正值由 livestream service 归一。 |

## 12. 生产部署最低检查清单

生产至少应显式检查并替换这些开发值：PostgreSQL DSN 与 TLS、Redis 密码和网络暴露、RSA 私钥持久化、固定开发验证码暴露、Admin 凭证/session key、OTP Webhook/SMTP secret、AI/Mapbox API key、CoreExec/FileData/Egress ACK/SFU gRPC control TLS/mTLS、TURN secret 与防火墙端口、公开 URL/scheme 与客户端一致性，以及真机所需的非 loopback SFU/TURN advertise IP。启用 TON Star Gift 时还必须完成固定 lite server/checkpoint/collection code hash、独立 capability/bot/relayer secret、单用户 allowlist、`mint.enabled: false` 预检，以及完整的 testnet/canary/回滚门禁。