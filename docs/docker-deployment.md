# Docker 部署

> Status: operation.
> Scope: 单机 Docker Compose 下的完整 `file/core/egress/sfu/edge` 拓扑。
> English version: [`docker-deployment.en.md`](docker-deployment.en.md).

这套部署不是旧单体兼容模式。默认启动 PostgreSQL、Redis、一次性 schema migrator 和五个
长期运行的应用角色：

```text
patched client
      |
      v
 Edge :2398  ---> CoreExec/Core :2440 ---> PostgreSQL
      |                 |                    ^
      +----> FileData/File :2520 ------------+
      |
      +----> Egress ACK/Egress :2510 --------+

 Core <----> SFU control :2450
                 |
                 +---- UDP :12399 ---> clients

 Edge/Core/Egress/SFU <---- Redis control/location fabric
```

Edge 的 Compose readiness 同时检查本地 MTProto listener、Redis，以及 CoreExec、FileData 和
Egress ACK 的认证 gRPC health；Core/File/Egress/SFU 的探针也会实际认证 PostgreSQL/Redis 或
检查各自直接后端。任一必需依赖不可用时相应容器会标记为 `unhealthy`，业务 RPC 不会回退
进程内执行。Docker Compose 不会仅因 `unhealthy` 自动重启容器，生产上仍须告警并由值班或
外部 watchdog 执行恢复。FileData 是强制角色；不能删除 File 服务或恢复 Core/Edge 本地文件
fallback。

## 1. 前置条件

- Docker Engine 24+ 或 Docker Desktop，并安装 Compose v2.24+。
- 至少 4 GiB 可用内存；本地构建镜像时建议 8 GiB，并为 Core/File 临时转码卷预留至少 2 GiB
  可用磁盘空间。
- patched client 可访问的 IPv4 或 IPv6 地址。内嵌 TURN 当前是 IPv4 listener；需要 TURN
  时必须提供客户端可达的 IPv4。
- 默认启动需要访问 `ghcr.io`、Docker Hub；只有使用 `-Build` 本地构建时才需要访问 Go module
  和 Alpine package 源。

默认 `localfs` blob backend 适合单机部署。多主机、高可用、TLS/mTLS、外部 PostgreSQL/
Redis、S3 和负载均衡属于下一层部署边界，不能通过复制本 Compose 或执行 `--scale` 就假装
已经完成；固定宿主端口、localfs、SFU 媒体地址和稳定 instance ID 都需要编排层重新设计。

## 2. 生成部署环境

Windows PowerShell 5.1+ 或 PowerShell 7+（使用系统密码学随机数、收紧文件 ACL，已有 `.env`
时拒绝覆盖）：

```powershell
.\scripts\new-docker-env.ps1 `
  -AdvertiseIP 192.0.2.10 `
  -PublicBaseURL https://chat.example.com `
  -PublicWebBaseURL https://web.chat.example.com
```

本机开发可只传 `-AdvertiseIP 127.0.0.1`；脚本会把公开端口也收紧到 loopback，并生成本机
HTTP public URL。非 loopback 地址必须显式提供自有 public URL，脚本不会静默沿用项目域名。

Linux/macOS 可以复制模板后，用 `openssl rand -hex 32` 分别生成互不相同的数据库、Redis
和内部服务密钥：

```bash
cp deploy/docker/.env.example deploy/docker/.env
chmod 600 deploy/docker/.env
${EDITOR:-vi} deploy/docker/.env
```

必须检查：

- `.env` 的赋值中不能保留任何 `CHANGEME` 占位值；容器 entrypoint 会 fail fast。
- `TELESRV_ADVERTISE_IP` 必须是客户端可达 IP，不能写 Docker service name 或 DNS 名。
- IPv4 部署默认启用 TURN；`TELESRV_TURN_ADVERTISE_IP` 必须是客户端可达 IPv4，
  `TELESRV_TURN_SECRET` 必须是独立随机值。TURN 主端口和 relay range 必须原端口转发，不能把
  `12500-12999/udp` 平移到另一段外部端口。IPv6-only 生成配置会显式关闭内嵌 TURN。
- RTMP 默认启用；`TELESRV_LIVESTREAM_RTMP_URL` 必须是 OBS/推流端可达的
  `rtmp://<host>:2400/live` 地址。
- development 登录码只有在 `TELESRV_ALLOW_INSECURE_DEVELOPMENT_AUTH=true` 时才允许；生成器
  仅为 loopback 部署自动打开它，局域网临时体验可显式传
  `-AllowInsecureDevelopmentAuth`。公网部署必须设置
  `TELESRV_PHONE_CODE_DELIVERY_PROVIDER=webhook`、`TELESRV_OTP_WEBHOOK_URL` 和独立的
  `TELESRV_OTP_WEBHOOK_SECRET`，协议见 `docs/otp-delivery.md`。
- `TELESRV_POSTGRES_DSN` 中的密码必须与 `POSTGRES_PASSWORD` 相同；手工使用特殊字符时先做
  URL 编码。
- 五种内部 token 和 TURN secret 必须彼此独立，不能复用数据库、Redis 或管理员密码。

生成脚本不会提供“强制覆盖”。直接重生成 `.env` 会令现有 PostgreSQL 密码、Redis 密码和
新旧容器 token 失配；凭据轮换必须逐项修改后协调重启，不能当作初始化重跑。

角色 YAML 只展开文件内出现的 `${NAME}`，普通进程环境变量不会覆盖未写入 YAML 的字段。
因此 Docker 可配置项以 `deploy/docker/config/*.yaml` 和 Compose 的角色级 `environment`
白名单为准，不以一份无限扩张的 `.env` 为准。每个角色镜像只内置自己的 YAML，默认 Compose
不从宿主 bind mount 配置，确保 image revision 与配置 revision 一致；修改 tracked YAML 后必须
重建全部受影响镜像。确需部署专属 override 时，用单独 Compose override 挂载单个角色文件，
并把该 override 纳入加密备份和同版本回滚。

## 3. 校验、拉取与启动

Windows 本机首次体验直接从仓库根目录运行；脚本会在缺少 `.env` 时自动按 loopback 安全默认值
初始化，随后从 GHCR 拉取 `v2` 镜像、启动并等待 readiness：

```powershell
.\scripts\start-docker.ps1
```

脚本完成后会直接打印默认 development 登录码 `12345`、TURN/STUN 端点、relay range 和 RTMP
ingest URL。Docker Desktop 首次创建完整 TURN UDP range 时可能需要几分钟，请等待脚本显示
ready。局域网临时体验可以一条命令初始化；把示例地址替换为宿主机的局域网地址：

```powershell
.\scripts\start-docker.ps1 -AdvertiseIP 192.168.1.20 -PublicBaseURL http://192.168.1.20:2401 -PublicWebBaseURL http://192.168.1.20:2401 -AllowInsecureDevelopmentAuth
```

局域网明文 HTTP 地址下，Core 会记录一条警告并关闭 Star 礼物/收益提现链接（提现 URL 是 bearer
链接，loopback 之外只允许 HTTPS），其余功能照常可用；需要提现时改用 HTTPS 的 public base URL。

真正的公网部署先单独运行 `new-docker-env.ps1` 生成配置，按 §2 把 phone-code provider 改为
webhook，再运行无参数 `start-docker.ps1`。已有 `.env` 时启动脚本始终复用原凭据；传入的新地址
或 development-auth 参数只会警告而不会覆盖，需要变更时应显式编辑 `.env` 后重启。

默认镜像前缀是 `ghcr.io/iamxvbaba/gramsrv`，包含 `migrate`、`file`、`core`、`egress`、
`sfu`、`admin`、`edge` 七个 package。体验 tag 是 `v2`，每次发布同时产生不可变的 `sha-<commit>` tag。
自动随 `v2` push 发布目前已暂停；维护者需要在 GitHub Actions 中手动运行
`Publish v2 container images` workflow。GHCR 首次创建 package 时默认是 private；维护者需要在
GitHub Packages 中把这七个 package 分别设置成 Public，之后普通用户无需登录即可拉取。若暂时
保持 private，先执行 `docker login ghcr.io`。

修改源码后要验证本地镜像时，显式构建；Compose 中的 Edge 本地构建目标同样是带公开测试密钥的
`edge-test`：

```powershell
.\scripts\start-docker.ps1 -Build
```

之后重启这套本地构建的镜像时加 `-NoPull`（`start-docker.sh` 为 `--no-pull`）：脚本不再访问镜像仓库，
缺少镜像时直接失败，不会用已发布的镜像悄悄覆盖本地 tag。与 `-Build` 同用时不刷新基础镜像。

需要拆开执行或在 Linux/macOS 上运行时：

```bash
docker compose --project-directory deploy/docker --env-file deploy/docker/.env -f deploy/docker/compose.yaml config --quiet
docker compose --project-directory deploy/docker --env-file deploy/docker/.env -f deploy/docker/compose.yaml pull
docker compose --project-directory deploy/docker --env-file deploy/docker/.env -f deploy/docker/compose.yaml up -d --no-build --wait
```

或者进入 `deploy/docker` 后使用短命令：

```powershell
Set-Location deploy\docker
docker compose config --quiet
docker compose pull
docker compose up -d --no-build --wait
```

空库启动时 PostgreSQL 与 Redis 先并行启动；PostgreSQL healthy 后运行一次性 `migrate`。
迁移成功后 File 启动，Egress 在 Redis 也 healthy 后启动；File healthy 后 Core 启动，Core healthy
后 SFU 与 Edge 才具备各自的启动条件。Edge 等待 Redis/Core/File/Egress，但有意不等待 SFU，
避免媒体服务阻塞消息入口。这样不会让多个长期进程在首次建库时争抢包含
`CREATE INDEX CONCURRENTLY` 的 migration；File/Core/Egress 自身仍会幂等确认同一套 migrations
作为第二道门。`up --wait` 最终要求全部长期服务健康且 migrator 成功完成。每个应用角色
使用独立 Dockerfile target；镜像内只包含当前角色、健康探针、精选 seed 和该角色所需运行库，
基础镜像锁定 digest。所有应用容器以 UID `10001` 运行，root filesystem 只读，drop all capabilities，
限制为 512 个 PID，并启用 `no-new-privileges`。

查看状态和 ready 日志：

```powershell
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml ps --all
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml logs --tail 100 file core egress sfu edge
```

期望 `migrate` 为 `Exited (0)`，五个长期应用均为 `healthy`，并分别出现：

- `telesrv-migrate: schema ready ...`
- `telesrv file ready`
- `telesrv core role ready`
- `turn listening`
- `live stream rtmp ingest listening`
- `telesrv egress ready`
- SFU standalone ready/heartbeat 日志
- `telesrv edge ready`

## 4. 网络和端口

| 宿主端口 | 默认绑定 | 用途 |
|---|---|---|
| `2398/tcp` | 零参数为 `127.0.0.1`；非 loopback 初始化为 `0.0.0.0`/`::` | MTProto Edge，客户端入口 |
| `12399/udp` | 零参数为 `127.0.0.1`；非 loopback 初始化为 `0.0.0.0`/`::` | SFU media |
| `12400/udp` | 零参数为 `127.0.0.1`；非 loopback IPv4 初始化为 `0.0.0.0` | TURN/STUN 主 listener |
| `12500-12999/udp` | 与 `12400/udp` 相同；宿主/容器端口 1:1 | TURN relay allocations |
| `2400/tcp` | 零参数为 `127.0.0.1`；非 loopback 初始化为 `0.0.0.0`/`::` | OBS 等推流端的 RTMP ingest |
| `2401/tcp` | 默认 `127.0.0.1`；直接使用 advertise IP 的 HTTP URL 时绑定该 IP | public-link HTTP 或宿主 nginx/Caddy 反代 |

PostgreSQL、Redis、CoreExec、FileData、Egress ACK、SFU control 和 debug/pprof 均不发布到
宿主机。`data` 与 `control` 是 internal Docker network；内部 gRPC 当前使用 bearer token 和
明文传输，因此 Docker daemon、宿主 root 与同一 control network 都属于信任边界。跨主机前
必须增加 TLS/mTLS overlay，不能直接复用本探针配置。

IPv4 Docker 部署默认启用内嵌 TURN 和 RTMP；路由器/NAT 与 Windows/Linux 防火墙必须同时放行
`12400/udp`、完整的 `12500-12999/udp` 以及 `2400/tcp`。TURN relay range 必须 1:1 转发，
`TELESRV_TURN_ADVERTISE_IP` 必须填写客户端看到的地址；只改 `.env` 或只开防火墙都不构成完整转发。
当前 v2 `compose.yaml` 已按 `.env` 配置 listener 和端口发布，不需要手工修改 Compose；更改这些
变量后重新执行启动脚本，让 Compose 重建 Core 容器即可。

若宿主有多块网卡，设置 `TELESRV_PUBLIC_BIND_IP` 和 IPv4 TURN 使用的
`TELESRV_TURN_BIND_IP` 为明确地址。生成器发现 PublicBaseURL 是
`http://<AdvertiseIP>:...` 时，会让 2401 直接绑定该地址，便于局域网首次体验；公网反代 2401
时保留宿主 loopback binding，并让 nginx/Caddy 终止 TLS。不要直接公开 pprof 或内部 gRPC。

Admin 默认启用，登录密码保存在 owner-only 的 `deploy/docker/.env`；它只读挂载 `file_data` 以显示
本机 blob/spool 磁盘容量，所有管理写操作都经 Core Admin API。TON worker 没有在默认 Compose 中启用；
需要时应增加显式配置和验证。

HTTP Bot API 网关默认关闭。在 `.env` 设置 `TELESRV_BOT_API_ADDR=0.0.0.0:8088` 即可启用（容器内端口固定 8088）；
一键脚本随之加载 `compose.bot-api.yaml`，按 `TELESRV_BOT_API_BIND_IP:TELESRV_BOT_API_PORT`（默认
`127.0.0.1:8088`）发布端口。手动运行 Compose 时在 `compose.yaml` 之后追加
`-f deploy/docker/compose.bot-api.yaml`。Bot API 是明文 HTTP，公网访问请保留 loopback 并经 TLS 反向代理。

## 5. 数据和 RSA 身份

| Volume | 内容 | 丢失后果 |
|---|---|---|
| `postgres_data` | 所有 durable business/auth/update facts 和 schema | 服务数据丢失 |
| `redis_data` | location/control/cache/AOF 状态 | 在线状态重建，活跃会话受影响 |
| `file_data` | localfs blobs、upload parts、map cache | 媒体不可恢复 |
| `file_tmp` | File 的 GIF/video 转码临时文件 | 可安全重建，不应备份 |
| `core_state` | 可选 seeds、map cache、OIDC key files | 对应功能不可用或身份变化 |
| `core_tmp` | ffmpeg/ffprobe 和 RTMP 分段的磁盘临时文件 | 可安全重建，不应备份 |
| `edge_state` | MTProto RSA private/public key | patched clients 无法再握手 |

### 5.1 一键启动后数据放在哪里

全新部署不需要用户手工创建或填充宿主机 `data/` 目录。Compose 会自动创建上表中的 Docker
named volumes，并把数据库、媒体和 RSA 身份写入这些卷。默认
`COMPOSE_PROJECT_NAME=telesrv` 时，实际卷名类似 `telesrv_postgres_data`、
`telesrv_file_data` 和 `telesrv_edge_state`；修改 project name 后前缀也会改变。

查看当前部署的卷和 Docker 管理的宿主位置：

```powershell
docker volume ls --filter label=com.docker.compose.project=telesrv
docker volume inspect telesrv_postgres_data
docker volume inspect telesrv_file_data
docker volume inspect telesrv_edge_state
```

Linux Docker Engine 通常把 named volume 放在 `/var/lib/docker/volumes/`；Docker Desktop for
Windows/macOS 则保存在 Docker Desktop 的虚拟磁盘中。不要直接编辑这些内部目录，应通过
PostgreSQL 工具、File 容器、`docker compose cp` 或 §6 的备份/恢复流程操作数据。

仓库根目录的 `data/` 主要服务于宿主进程开发和镜像构建输入：

- `internal/seed/catalog`、`internal/seed/appearance` 通过 `go:embed` 编译进 Core；
  `data/langpack/` 会复制到 `/usr/share/telesrv/langpack/`。这些已经随 GHCR Core 镜像提供，
  用户不需要准备或 mount `data/`。镜像内清单是 `/usr/share/telesrv/seed-manifest.json`。
- 几百 MB 的 sticker/reaction 导出、official gifts 和 Premium promo media 没有塞进默认镜像；
  它们仍是按功能导入的可选数据，避免一键下载体积失控。
- 当前测试私钥的 Base64 fixture 与公钥固定在 `deploy/docker/assets/test-server-rsa.pem.b64` 和
  `deploy/docker/assets/test-server-rsa.pub`，并只复制到 `edge-test`/GHCR `edge:v2` 镜像。
- 数据库、媒体 blob 和 Redis 状态没有统一映射到宿主 `data/` 目录，而是按角色分别存入 named
  volumes。这样容器重建时可以保留数据，也避免 Windows/Linux bind-mount 权限差异影响一键启动。

因此，全新一键启动时不要把空目录或示例文件复制进 volumes。已有部署迁移也不能把整个
`data/` 目录直接覆盖进去：数据库用 dump/restore，媒体恢复到 `file_data`，RSA private key
恢复到 `edge_state`，具体流程见 §6。

### 5.2 一键启动后的 RSA 公私钥

默认 `.env` 使用 `TELESRV_RSA_IDENTITY_MODE=test`。Edge entrypoint 会在空 `edge_state` 卷中
把镜像内公开的测试私钥 fixture 解码一次，再导出匹配的 PKCS#1 公钥；这对密钥就是当前测试
阶段的固定身份，私钥也是公开测试数据。固定路径为：

| 文件 | 仓库/镜像路径 | 运行时 volume 路径 | 用途 |
|---|---|---|---|
| RSA private key | `deploy/docker/assets/test-server-rsa.pem.b64` / `/usr/share/telesrv/keys/test-server-rsa.pem.b64` | `/var/lib/telesrv-edge/server_rsa.pem` | Base64 只是便于公开发布，运行时还原原始 PKCS#1；不放进客户端 |
| RSA public key | `deploy/docker/assets/test-server-rsa.pub` / `/usr/share/telesrv/keys/test-server-rsa.pub` | `/var/lib/telesrv-edge/server_rsa.pub` | 写入 patched TDesktop/Android 客户端 |

因此，使用默认 `edge:v2` 的客户端可以直接读取仓库内的
`deploy/docker/assets/test-server-rsa.pub`，不需要等服务启动后再找公钥。要确认实际 volume 中
正在使用的身份，可导出到被 Git 和 Docker build context 排除的
`data/docker-server_rsa.pub` 并比较：

```powershell
if (Test-Path data\docker-server_rsa.pub) { throw "data/docker-server_rsa.pub already exists; compare it before replacing" }
docker compose --project-directory deploy/docker --env-file deploy/docker/.env -f deploy/docker/compose.yaml cp edge:/var/lib/telesrv-edge/server_rsa.pub data/docker-server_rsa.pub
```

Linux/macOS 使用同一条 `docker compose ... cp` 命令即可。entrypoint 永远不会覆盖已存在的
`edge_state/server_rsa.pem`，所以重新创建容器不会换 key；若旧 volume 内已经是另一套密钥，
它也会继续使用旧身份并在日志中提示。默认 test 模式删除 volume 后会重新安装同一对测试密钥，
测试客户端 fingerprint 保持不变。

如果把这套 Compose 用作正式环境，应在新部署的 `.env` 中设置
`TELESRV_RSA_IDENTITY_MODE=generated`、`TELESRV_EDGE_BUILD_TARGET=edge` 并运行
`.\scripts\start-docker.ps1 -Build` 构建不含测试私钥的 Edge，或提前把自有
PKCS#1 私钥放进 `edge_state`。正式私钥仍须备份且不能入库；多 Edge 使用同一份正式身份。本节
公开测试私钥不能当作正式秘密使用。

`file_tmp` 和 `core_tmp` 使用宿主磁盘而不是 64 MiB `/tmp` tmpfs。正常请求会删除临时文件；
进程崩溃可能留下 `telesrv-*` 文件，应监控卷容量，并只在对应服务停止后清理孤儿文件。

## 6. 备份与恢复

停止业务 writer 后做一致性备份。下面的流程让二进制归档直接落盘，不经过 Windows PowerShell
5.1 的文本重定向：

```powershell
$backupDir = (New-Item -ItemType Directory -Force deploy\docker\backups\manual).FullName
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml stop edge sfu egress core file
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml exec -T postgres sh -ec 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc -f /tmp/telesrv.dump'
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml cp postgres:/tmp/telesrv.dump "$backupDir/telesrv.dump"
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml exec -T postgres rm -f /tmp/telesrv.dump
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml cp edge:/var/lib/telesrv-edge/server_rsa.pem "$backupDir/server_rsa.pem"
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml run --rm --no-deps --user 0:0 --volume "${backupDir}:/backup" file tar -C /var/lib/telesrv-file -czf /backup/file_data.tar.gz .
Copy-Item -LiteralPath deploy\docker\.env -Destination "$backupDir/.env"
```

还应备份 Docker YAML override、`core_state` 内显式启用的 OIDC/seeds，以及所用镜像 tag/digest。
备份目录已被 Git/build context 排除，但仍应设置 owner-only ACL、离机加密并做 checksum。S3
backend 不备份 `file_data` 作为 blob 真相；应使用对象存储自身的版本化/复制策略并与数据库
快照对齐。备份完成后重新 `up -d --wait`。

恢复必须先在隔离的空项目/空 volumes 演练。放回备份 `.env` 后，可执行：

```powershell
$backupDir = (Resolve-Path deploy\docker\backups\manual).Path
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml up -d --wait postgres redis
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml cp "$backupDir/telesrv.dump" postgres:/tmp/telesrv.dump
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml exec -T postgres sh -ec 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists --no-owner /tmp/telesrv.dump'
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml run --rm --no-deps --user 0:0 --volume "${backupDir}:/backup:ro" file tar -C /var/lib/telesrv-file -xzf /backup/file_data.tar.gz
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml run --rm --no-deps --user 0:0 --volume "${backupDir}:/backup:ro" edge sh -ec 'install -m 0600 -o 10001 -g 10001 /backup/server_rsa.pem /var/lib/telesrv-edge/server_rsa.pem'
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml up -d --wait
```

若显式使用了 `core_state` 中的登录密钥或 seeds，也要恢复其独立备份。Redis 在线状态可以从空
卷重建，但客户端会重连。最后核对 RSA fingerprint、schema、媒体 range read 和真实客户端
登录；只恢复数据库而漏掉 blob 或 RSA 不算可用备份。

## 7. 升级与回滚

五个角色必须来自同一个 source revision。升级前先备份，停止旧 writer，再构建/拉取全部角色：

```powershell
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml stop edge sfu egress core file
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml pull
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml run --rm --no-deps migrate
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml up -d --wait file
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml up -d --wait core egress
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml up -d --wait sfu edge
```

独立 migrator 先应用当前 revision 的 schema，File/Core/Egress 启动时再校验同一套 migrations；
这不代表任意旧 binary 都与新 schema 向后兼容。发生不兼容迁移时，回滚必须恢复升级前数据库
和对应 file/RSA 状态；不能只把 image tag 改回旧值。

普通停止使用 `docker compose stop` 或 `docker compose down`。`docker compose down -v` 会
删除数据库、媒体和 RSA 身份，只允许在明确重置测试环境时使用。

## 8. localfs 与 S3

默认 `TELESRV_BLOB_BACKEND=localfs`，File 是唯一 localfs data-plane owner。若改用 S3：

1. 在 `.env` 填写 endpoint/region/bucket/access key/secret、TLS 和 path-style。
2. 把 `TELESRV_BLOB_BACKEND` 改为 `s3`。
3. 在隔离环境验证 bucket 权限、range read、multipart/upload-part GC 和容量告警。
4. 按[配置参考](configuration.zh-CN.md)中的离线迁移约束执行；已有数据不能靠改环境变量自动搬迁，
   读写也不会 fallback 到另一 backend。

Compose 不默认捆绑 MinIO，避免把开发对象存储误当生产高可用 S3。需要自包含的 MinIO 测试
环境时，用单独 override，并保留 bucket 初始化、持久卷和健康检查。

## 9. 最低验收矩阵

部署交付前至少验证：

```powershell
docker compose --project-directory deploy/docker --env-file deploy/docker/.env -f deploy/docker/compose.yaml config --quiet
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml up -d --no-build --wait
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml exec edge id -u
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml ps
```

- `id -u` 为 `10001`；对 root filesystem 的写入失败，但声明的数据卷可写。
- 宿主只能看到预期的 `2398/tcp`、`12399/udp`、`12400/udp`、`12500-12999/udp`、
  `2400/tcp`，以及按 `.env` 绑定的 `2401/tcp`。
- 停止 Core/File/Egress 后 Edge 必须转为 `unhealthy`，相应业务请求 fail closed，不能回退进程
  内执行；后端恢复后应自动回到 `healthy`。
- `down`（不带 `-v`）再 `up` 后 RSA fingerprint、schema、账号和 media blob 不变。
- 覆盖在线 push/ACK、离线 difference、Edge 重连、Egress crash/reclaim 和 SFU UDP media。

## 10. 常见故障

- **Compose 在解析阶段报 required variable**：`.env` 缺值，或命令未指定正确的
  `--project-directory/--env-file`。
- **entrypoint 报 placeholder**：仍有 `CHANGEME` 或空内部 token。
- **File 不健康**：先看 PostgreSQL migration、backend kind、磁盘余量和 `file_data` 权限。
- **Core 不健康**：检查 FileData health/GetInfo、PostgreSQL、Redis，以及 CoreExec/group-control/
  public-link 三个 listener；SFU 是独立 owner，不是 Core 的本地 fallback。
- **Edge 不健康**：依次检查本地 2398 listener，以及 CoreExec/FileData/Egress ACK 的 bearer
  token 和 health。
- **群通话能加入但媒体失败**：确认 `TELESRV_ADVERTISE_IP` 与 `12399/udp` 的 NAT/firewall 转发。
- **私聊通话无法中继**：确认 Core 日志有 `turn listening`，并检查
  `TELESRV_TURN_ADVERTISE_IP`、`12400/udp` 和完整 `12500-12999/udp` 的 1:1 NAT/firewall 转发。
- **OBS 无法连接 RTMP**：确认 Core 日志有 `live stream rtmp ingest listening`，并检查
  `TELESRV_LIVESTREAM_RTMP_URL` 与 `2400/tcp` 转发；推流 key 由客户端直播设置页生成。
- **client RSA fingerprint 不匹配**：从当前 `edge_state` 导出 public key；旧卷可能保留了另一套
  身份。全新默认 test volume 应与 `deploy/docker/assets/test-server-rsa.pub` 完全一致。
