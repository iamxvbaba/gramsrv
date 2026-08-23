# telesrv configuration reference

Chinese version: [configuration.zh-CN.md](configuration.zh-CN.md)

This document describes settings loaded by `internal/config`. Service entrypoints now use role-specific YAML files:
`configs/edge.yaml`, `configs/core.yaml`, `configs/egress.yaml`, `configs/file.yaml`,
`configs/sfu.yaml`, `configs/admin.yaml`, and `configs/ton.yaml`. Defaults and validation behavior in the corresponding role config Go files under
`internal/config` are authoritative. All settings require a process restart; telesrv does not hot-reload configuration.

## 1. Loading, syntax, and precedence

- `cmd/telesrv-edge|core|egress|file|sfu|admin|ton` read `configs/<role>.yaml` by default. Use
  `--config <path>` or process environment `TELESRV_CONFIG=<path>` to point a process at its own YAML file.
- YAML is parsed strictly: unknown fields, invalid structure, and invalid duration values fail startup. Environment
  variables are no longer generic field overrides for service entrypoints; they are only expanded from YAML strings
  such as `${POSTGRES_DSN}` or `${CORE_EXEC_TOKEN}` for secret/platform injection.
- `TELESRV_CONFIG` only selects the current process config file. Do not put it inside YAML; systemd units,
  Docker/Kubernetes manifests, and local scripts should pass one config path per role.
- YAML booleans use `true`/`false`; lists use YAML sequences; duration fields use Go duration strings such as
  `200ms`, `30s`, `5m`, or `168h`.
- Never commit real passwords, tokens, private DSNs, or TURN secrets. Prefer a secret manager or protected service environment in production.

The Go configuration boundary is role-scoped too: `internal/config/edge_config.go`, `core_config.go`,
`egress_config.go`, `file_config.go`, `sfu_config.go`, `admin_config.go`, and `ton_config.go` define the matching YAML schema, `Load<Role>()`, and runtime
config type. Production entrypoints and `internal/node/<role>` should not accept the global `config.Config`; the repository no longer exposes the old env-file entrypoint.

## 2. MTProto listener, transport, and resource budgets

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_LISTEN` | string / `0.0.0.0:2398` | MTProto TCP listen address for `cmd/telesrv-edge`. Patched clients must be able to reach it. `cmd/telesrv-core` does not bind the MTProto listener; do not treat the Core-internal address as the client DC address. |
| `TELESRV_ADVERTISE_IP` | string / `127.0.0.1` | Client-reachable server IP written to `help.getConfig.DCOptions` and used by media/call fallbacks. The loopback default is only safe for pure local TDesktop validation; Android, LAN, and remote validation must set the host LAN/public IP explicitly. On Windows, prefer `scripts\restart-local-server.ps1 -Listen 0.0.0.0:2398 -AdvertiseIP <client-reachable-ip>` so a manual `Start-Process` launch cannot drop the environment. |
| `TELESRV_ADVERTISE_PORT` | TCP port / `TELESRV_LISTEN` port | Client-reachable port written to `help.getConfig.DCOptions`. `cmd/telesrv-edge` defaults to the `TELESRV_LISTEN` port. `cmd/telesrv-core` must explicitly set this to the public Edge port; never advertise a Core-internal address to clients. |
| `TELESRV_RSA_KEY` | path / `data/server_rsa.pem` | MTProto RSA private key. Generated when missing. Treat the file as a secret and keep it stable across restarts. |
| `TELESRV_DC` | positive TL int32 / `2` | Canonical server DC ID used in server-originated configuration, media/DC metadata, and `channelFull.stats_dc`. It must be within `1..2147483647` or startup fails. It does not partition key-exchange state on the current single backend. |
| `TELESRV_DEFAULT_COUNTRY_CODE` | ISO alpha-2 / `CN` | Country returned by `help.getNearestDc` for login-page preselection. Clients map `CN` to calling code `+86`, `US` to `+1`, and so on. Input is trimmed, uppercased, and validated as a country or autonomous area; malformed or unknown values fail startup. |
| `TELESRV_STRICT_DC_CHECK` | bool / `false` | Default `false` accepts every wire int32 DC label for permanent and temporary key exchange. `true` requires permanent `dc_id == TELESRV_DC` and temporary `abs(dc_id) == TELESRV_DC`; it is only a diagnostic and does not provide multi-DC isolation. |
| `TELESRV_INSTANCE_ID` | string / empty | Instance identifier for this process in Edge/Core control, Redis online-location indexes, and pub/sub source dedupe. Empty values are generated at startup from hostname/pid/time; multi-instance deployments should configure a stable unique value. |
| `TELESRV_EDGE_LOCATION_TTL` | duration / `90s` | TTL for Edge online-location and active raw-key lease records written to Redis. It must be positive and greater than the heartbeat interval; Core orphan-auth-key GC reads this distributed lease snapshot and skips deletion when the snapshot is unavailable. |
| `TELESRV_EDGE_LOCATION_HEARTBEAT_INTERVAL` | duration / `30s` | Interval at which this process refreshes its online-session location snapshot. It must be positive and less than `TELESRV_EDGE_LOCATION_TTL`. |
| `TELESRV_GROUPCALL_CONTROL_ADDR` | address / empty | Internal HTTP listen address used by Core to receive media-liveness reports from standalone SFU processes. `cmd/telesrv-core` requires it. |
| `TELESRV_GROUPCALL_CONTROL_URL` | URL / empty | Internal URL used by standalone `telesrv-sfu` to call Core's groupcall control API. |
| `TELESRV_GROUPCALL_CONTROL_TOKEN` | secret string / empty | Bearer token shared by Core and standalone SFU for the groupcall control API. `cmd/telesrv-core` and `cmd/telesrv-sfu` both require an explicit non-empty value. |
| `TELESRV_CORE_EXEC_GRPC_ADDR` | address / empty | Internal listen address for the production CoreExec gRPC API on `cmd/telesrv-core`. The Core entrypoint requires this setting. |
| `TELESRV_CORE_EXEC_GRPC_RESOLVER` | `static`/`dns` / `static` | Edge -> CoreExec gRPC service-discovery provider. `static` consumes a comma-separated endpoint list from `TELESRV_CORE_EXEC_GRPC_TARGETS`. `dns` consumes exactly one target: either a bare `host:port` service name, or an explicit gRPC DNS URI such as `dns:///telesrv-core.default.svc.cluster.local:2500` or `dns://127.0.0.1:5353/coreexec.test:2500` for canaries with a custom DNS authority. DNS mode lets the gRPC DNS resolver expand A/AAAA records, which fits Kubernetes Service/headless DNS without adding Consul/etcd/Nacos. Unsupported values fail startup. |
| `TELESRV_CORE_EXEC_GRPC_TARGETS` | resolver target(s) / empty | Required CoreExec gRPC endpoint input for `cmd/telesrv-edge`. With `static`, entries must be bare `host:port` values with numeric ports in `1..65535`; URL schemes are rejected, duplicates are removed, and comma-separated endpoints are allowed. IPv6 addresses must use bracket form, for example `[::1]:2440`. With `dns`, exactly one target is allowed; a bare `host:port` value is normalized to `dns:///host:port`, and explicit `dns:///name:port` / `dns://authority:port/name:port` URIs are accepted. Multiple Core addresses must come from DNS records, not comma-separated env values. The gRPC client uses bearer auth, startup unary health + `GetInfo` protocol-version/capability/instance-id handshake, `round_robin`, and gRPC client-side health checking through the service config; NOT_SERVING Core instances are kept out of the dispatch picker. gRPC automatic retries are explicitly disabled. Transport timeout/unavailable is classified in metrics/logs. Edge-side gRPC status error logs carry request_id, operation, and outcome, but request_id is not a metrics label. Edge keeps only a private TL admission shell for profile checks and raw-wire capture; it has no local business dispatch fallback when CoreExec is unavailable. Business replay must go through RPC execution/outbox semantics. A single hard-coded Core address is not enough for the production gate. |
| `TELESRV_CORE_EXEC_GRPC_REQUEST_TIMEOUT` | duration / `5s` | Default deadline for Edge -> CoreExec gRPC unary calls. It is added only when the upstream context has no deadline. Must be greater than `0` and at most `1m`. Covers dispatch, profile evidence, initConnection observer, and replay commit so a Core failure cannot hold Edge execution slots forever. |
| `TELESRV_CORE_EXEC_GRPC_TLS_CERT_FILE` / `TELESRV_CORE_EXEC_GRPC_TLS_KEY_FILE` | path / empty | CoreExec gRPC server TLS certificate and private key. They must be configured together. Empty keeps the local/development default plaintext gRPC channel. |
| `TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CA_FILE` | path / empty | CA bundle used by the CoreExec gRPC server to verify Edge client certificates. Setting it enables mTLS and requires the server TLS cert/key settings. |
| `TELESRV_CORE_EXEC_GRPC_TLS_CA_FILE` | path / empty | Root CA bundle trusted by the Edge gRPC client when verifying Core server certificates. Setting it enables TLS on the client. |
| `TELESRV_CORE_EXEC_GRPC_TLS_SERVER_NAME` | string / empty | Server name used by the Edge gRPC client when verifying the Core server certificate. Use it when static targets are IPs or LB addresses but the certificate is issued to an internal DNS name. A non-empty value also enables TLS. |
| `TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CERT_FILE` / `TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_KEY_FILE` | path / empty | Edge gRPC client certificate and private key. They must be configured together. Use them with the server `TLS_CLIENT_CA_FILE` setting for mTLS. |
| `TELESRV_CORE_EXEC_TOKEN` | secret string / empty | Bearer token for CoreExec gRPC APIs. `cmd/telesrv-core` and `cmd/telesrv-edge` both require an explicit non-empty value, and values must match on client and server and must not contain newlines. The current gRPC channel supports static multi-endpoint and DNS resolver providers; later providers may use Envoy/xDS or Consul. Do not introduce a standalone Consul/etcd/Nacos component just to pre-solve CoreExec discovery; discovery should stay behind the replaceable resolver abstraction. Redis still owns Edge location, push/control ACKs, short registries, and SFU owner records, not generic CoreExec service discovery. |
| `TELESRV_FILE_GRPC_ADDR` | address / empty | Internal listen address for the standalone `cmd/telesrv-file` FileData gRPC API. Required by the File entrypoint. |
| `TELESRV_FILE_GRPC_RESOLVER` | `static`/`dns` / `static` | Edge/Core -> FileData gRPC service-discovery provider. It follows the CoreExec resolver rules: `static` consumes comma-separated `host:port` endpoints, while `dns` consumes exactly one bare `host:port` or explicit `dns:///name:port` / `dns://authority:port/name:port` target. Unsupported values fail startup. |
| `TELESRV_FILE_GRPC_TARGETS` | resolver target(s) / empty | Required FileData gRPC endpoint input for `cmd/telesrv-edge` and `cmd/telesrv-core`. Edge probes FileData before opening the MTProto listener; Core probes it before exposing CoreExec. Unreachable FileData, token errors, protocol-version mismatch, or an unhealthy FileData backend fail fast. |
| `TELESRV_FILE_GRPC_REQUEST_TIMEOUT` | duration / `10s` | Default deadline for Edge/Core -> FileData gRPC calls. It is added only when the upstream context has no deadline. Must be greater than `0` and at most `1m`. Covers upload parts, static range downloads, blob put/get, and upload-part materialization. |
| `TELESRV_FILE_GRPC_TLS_CERT_FILE` / `TELESRV_FILE_GRPC_TLS_KEY_FILE` | path / empty | FileData gRPC server TLS certificate and private key. They must be configured together. Empty keeps the local/development default plaintext gRPC channel. |
| `TELESRV_FILE_GRPC_TLS_CLIENT_CA_FILE` | path / empty | CA bundle used by the FileData gRPC server to verify Edge/Core client certificates. Setting it enables mTLS and requires the server TLS cert/key settings. |
| `TELESRV_FILE_GRPC_TLS_CA_FILE` | path / empty | Root CA bundle trusted by Edge/Core gRPC clients when verifying FileData server certificates. Setting it enables TLS on the client. |
| `TELESRV_FILE_GRPC_TLS_SERVER_NAME` | string / empty | Server name used by Edge/Core gRPC clients when verifying the FileData server certificate. A non-empty value also enables TLS. |
| `TELESRV_FILE_GRPC_TLS_CLIENT_CERT_FILE` / `TELESRV_FILE_GRPC_TLS_CLIENT_KEY_FILE` | path / empty | Edge/Core gRPC client certificate and private key for FileData calls. They must be configured together. |
| `TELESRV_FILE_TOKEN` | secret string / empty | Bearer token for FileData gRPC APIs. `cmd/telesrv-file`, `cmd/telesrv-edge`, and `cmd/telesrv-core` all require an explicit non-empty value, and it must not contain newlines. FileData handles upload parts, static blob ranges, and materialization only; it is not the message/media permission authority. |
| `TELESRV_EGRESS_ACK_GRPC_ADDR` | address / empty | Internal listen address for the late client-ACK writeback API on `cmd/telesrv-egress`. The Egress entrypoint requires this setting. Service-level Edge fabric confirmation is enough to advance online outbox rows; this API is only a late signal for indeterminate writes and supports both PTS `dispatch_outbox` and non-PTS `edge_delivery_outbox` (the non-PTS lane uses `pts=0` in the fence). |
| `TELESRV_EGRESS_ACK_GRPC_RESOLVER` | `static`/`dns` / `static` | Edge -> Egress ACK gRPC service-discovery provider. It follows the CoreExec resolver rules: `static` consumes comma-separated `host:port` endpoints, while `dns` consumes exactly one bare `host:port` or explicit `dns:///name:port` / `dns://authority:port/name:port` target. Unsupported values fail startup. |
| `TELESRV_EGRESS_ACK_GRPC_TARGETS` | resolver target(s) / empty | Required Egress ACK gRPC endpoint input for `cmd/telesrv-edge`. Edge performs health + `GetInfo` before publishing the MTProto listener; unreachable Egress ACK, token errors, or protocol-version mismatch fail fast so clients cannot connect to an Edge that cannot advance PTS outbox or non-PTS delivery queue ACK state. |
| `TELESRV_EGRESS_ACK_GRPC_REQUEST_TIMEOUT` | duration / `5s` | Default deadline for Edge -> Egress ACK gRPC unary calls. It is added only when the upstream context has no deadline. Must be greater than `0` and at most `1m`. |
| `TELESRV_EGRESS_ACK_GRPC_TLS_CERT_FILE` / `TELESRV_EGRESS_ACK_GRPC_TLS_KEY_FILE` | path / empty | Egress ACK gRPC server TLS certificate and private key. They must be configured together. Empty keeps the local/development default plaintext gRPC channel. |
| `TELESRV_EGRESS_ACK_GRPC_TLS_CLIENT_CA_FILE` | path / empty | CA bundle used by the Egress ACK gRPC server to verify Edge client certificates. Setting it enables mTLS and requires the server TLS cert/key settings. |
| `TELESRV_EGRESS_ACK_GRPC_TLS_CA_FILE` | path / empty | Root CA bundle trusted by the Edge gRPC client when verifying Egress ACK server certificates. Setting it enables TLS on the client. |
| `TELESRV_EGRESS_ACK_GRPC_TLS_SERVER_NAME` | string / empty | Server name used by the Edge gRPC client when verifying the Egress ACK server certificate. A non-empty value also enables TLS. |
| `TELESRV_EGRESS_ACK_GRPC_TLS_CLIENT_CERT_FILE` / `TELESRV_EGRESS_ACK_GRPC_TLS_CLIENT_KEY_FILE` | path / empty | Edge gRPC client certificate and private key for Egress ACK calls. They must be configured together. |
| `TELESRV_EGRESS_ACK_TOKEN` | secret string / empty | Bearer token for Egress ACK gRPC APIs. `cmd/telesrv-edge` and `cmd/telesrv-egress` both require an explicit non-empty value, and it must not contain newlines. |
| `TELESRV_WEBSOCKET_ENABLE` | bool / `true` | Enables MTProto-over-WebSocket demultiplexing on the MTProto listener. |
| `TELESRV_WEBSOCKET_ALLOWED_ORIGINS` | list / `http://localhost:1234,http://127.0.0.1:1234` | Browser WebSocket origin allow-list. `*` is for temporary debugging only. |
| `TELESRV_MTPROTO_MAX_CONNECTIONS` | int / `200000` | Global physical connection admission limit. Negative disables this gate. |
| `TELESRV_MTPROTO_MAX_CONNECTIONS_PER_IP` | int / `4096` | Per-source-IP physical connection limit. Negative disables this gate. |
| `TELESRV_MTPROTO_MAX_CONCURRENT_HANDSHAKES` | int / `256` | Concurrent expensive RSA/DH handshakes. Negative disables this gate. |
| `TELESRV_MTPROTO_RPC_MAX_INFLIGHT` | int / `32` | Per-connection concurrent RPC budget; non-positive values are normalized by the edge to its safe default. |
| `TELESRV_MTPROTO_RPC_QUEUE_SIZE` | int / `64` | Per-connection queued RPC budget; non-positive values use the edge default. |
| `TELESRV_MTPROTO_RPC_TIMEOUT` | duration / `30s` | End-to-end handler timeout for scheduled RPC work. |
| `TELESRV_MTPROTO_RPC_GLOBAL_WORKERS` | int / `256` | Shared fair-scheduler worker count. |
| `TELESRV_MTPROTO_RPC_GLOBAL_MAX_TASKS` | int / `8192` | Process-wide scheduled/in-flight RPC task cap. |
| `TELESRV_MTPROTO_RPC_GLOBAL_MAX_BYTES` | int64 charge bytes / `536870912` | Process-wide reserved/queued/in-flight RPC memory charge. Exact admission reserves a conservative typed-materialization charge from wire size and grows it atomically before a nested-gzip-expanded graph is decoded; grow failure rejects the complete candidate batch. This is not an equal amount of concurrently receivable wire bytes. |
| `TELESRV_MTPROTO_RPC_EXECUTION_MAX_ENTRIES` | int / `262144` | Global cap for pending owners and compact unacknowledged execution receipts. Receipts retain request identity, execution outcome, and Layer admission metadata only—never TL bodies. `msgs_ack` removes them immediately; 331 seconds is only the no-ACK safety horizon. |
| `TELESRV_MTPROTO_RPC_EXECUTION_AUTH_MAX_ENTRIES` | int / `32768` | Per-raw-auth owner/receipt cap; limits satisfy `global >= auth >= session`. |
| `TELESRV_MTPROTO_RPC_EXECUTION_SESSION_MAX_ENTRIES` | int / `16384` | Per `raw auth key + session_id` owner/receipt cap. |
| `TELESRV_MTPROTO_RPC_EXECUTION_PENDING_PER_AUTH` | int / `2048` | Additional active-owner cap per raw auth key; no greater than global pending tasks or auth entries. |
| `TELESRV_MTPROTO_INBOUND_FRAME_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | Process-wide reservation for transport wire bytes, maximum decrypted plaintext, and every live outer/nested gzip expansion, acquired before the corresponding payload allocation. |
| `TELESRV_MTPROTO_OUTBOUND_QUEUE_SIZE` | int / `128` | Per-connection normal outbound mailbox capacity. |
| `TELESRV_MTPROTO_OUTBOUND_CONTROL_QUEUE_SIZE` | int / `32` | Per-connection control-message mailbox capacity. |
| `TELESRV_MTPROTO_OUTBOUND_TRACKED_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | Sole global budget for unacknowledged logical-session bodies. Reconnects reuse the same `msg_id/seq_no/body`; ACK, destroy, or six minutes offline releases it, with no second RPC cache/spool copy. |
| `TELESRV_MTPROTO_OUTBOUND_WRITE_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | Global budget for concurrent encrypted wire/codec/obfuscation scratch. |

Nested gzip admission adds no environment setting. Code-enforced ceilings are
10 MiB of output per `gzip_packed` envelope and 32 MiB of cumulative expansion
work per transport frame across outer, nested, sibling, failed, and
authoritative-profile re-decode attempts. Releasing a live expanded buffer
returns process memory but does not refund that frame's CPU/work counter.
Retained typed graphs remain charged to the RPC scheduler budget above.

## 3. HTTP endpoints, public links, and administration

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_DEBUG_ADDR` | nullable address / `127.0.0.1:6060` | pprof/debug listener. Empty disables it. Keep loopback-only; use an SSH tunnel for production profiling. |
| `TELESRV_BOT_API_ADDR` | nullable address / empty | Minimal HTTP Bot API listener. Empty disables it. It shares MTProto app/store facts. `setWebhook` accepts any valid `http://` or `https://` host/IP and port in `1..65535`. |
| `TELESRV_ADMIN_API_ADDR` | nullable address / empty | In-process Admin write API listener. Empty disables it; production should bind loopback. |
| `TELESRV_ADMIN_API_TOKEN` | secret string / empty | Admin API bearer token. Required when the Admin API is enabled and must match the Admin UI token configuration. |
| `TELESRV_ADMIN_UI_ADDR` | address / `127.0.0.1:2600` | Standalone `cmd/telesrv-admin` listen address. |
| `TELESRV_ADMIN_UI_PASSWORD` | secret string / empty | Admin UI login password. Configure this or `TELESRV_ADMIN_UI_TOKEN`. |
| `TELESRV_ADMIN_UI_TOKEN` | secret string / empty | Alternative Admin UI login credential. Admin write calls still use the separate `TELESRV_ADMIN_API_TOKEN`. |
| `TELESRV_ADMIN_SESSION_KEY` | secret string / empty | Encrypts/signs Admin UI session cookies. Production should use at least 32 random bytes; changing it invalidates sessions. |
| `TELESRV_ADMIN_UI_PERMISSIONS` | comma-separated list / `*` | Permissions granted to an Admin UI session authenticated with `TELESRV_ADMIN_UI_PASSWORD` / `_TOKEN`. `*` grants every permission and is the default. Reading a managed bot token requires the explicit `bots.token.read` permission; the token is returned only by the command response and is not persisted in the audit result. Names use letters, digits and `._:-`, at most 64 characters, and may end in `namespace.*` to grant a whole namespace. An empty list or an unparsable name fails startup. |
| `TELESRV_ADMIN_SCOPED_TOKENS` | `name:token:perm1,perm2` entries separated by `;` / empty | Additional Admin API bearer tokens carrying a bounded permission set each, so an integration gets exactly the rights it needs instead of the unrestricted `TELESRV_ADMIN_API_TOKEN`. A token may contain neither `:` nor whitespace, every entry must list at least one permission, names and tokens must be unique, and reusing `TELESRV_ADMIN_API_TOKEN` as a scoped token is refused because it would silently widen it to every permission. Any malformed entry fails startup rather than silently granting or dropping rights. |
| `TELESRV_PUBLIC_BASE_URL` | HTTP(S) URL / `https://telesrv.net` | Client-visible canonical public-link root. Paths are allowed; credentials, query, and fragment are rejected. Local example: `http://127.0.0.1:2401`. |
| `TELESRV_BRAND_PRODUCT_NAME` | string / `Telesrv` | Parent product display name shared by the system account, login notices, invites, WebAuthn, built-in bots, and upstream visible-copy rewriting. Trimmed, non-empty, no control characters, at most 64 Unicode characters. |
| `TELESRV_BRAND_PRODUCT_USERNAME` | username / `telesrv` | Public username of system account 777000. It is trimmed, loses an optional leading `@`, is normalized to lowercase, and must be a 5–32 character ASCII username starting with a letter. |
| `TELESRV_BRAND_DESKTOP_APP_NAME` | string / `Telesrv Desktop` | Desktop/Windows name returned by `account.getAuthorizations`; same validation as the product name. |
| `TELESRV_BRAND_ANDROID_APP_NAME` | string / `Telesrv Android` | Android name shown in authorization lists; same validation as the product name. |
| `TELESRV_BRAND_IOS_APP_NAME` | string / `Telesrv iOS` | iOS name shown in authorization lists; same validation as the product name. |
| `TELESRV_BRAND_MACOS_APP_NAME` | string / `Telesrv macOS` | macOS name shown in authorization lists; same validation as the product name. |
| `TELESRV_BRAND_WEB_A_APP_NAME` | string / `Telesrv Web A` | Authorization-list name for `telegram-tt` / `weba`; durable detection tokens remain unchanged. |
| `TELESRV_BRAND_WEB_K_APP_NAME` | string / `Telesrv Web K` | Authorization-list name for `tweb` / `webk`; durable detection tokens remain unchanged. |
| `TELESRV_BRAND_PREMIUM_NAME` | string / `Telesrv Premium` | Product name used by Premium status and related client-visible copy; same validation as the product name. |
| `TELESRV_BRAND_STARS_NAME` | string / `Telesrv Stars` | Product name used by Stars payment labels and related client-visible copy; same validation as the product name. |
| `TELESRV_UPDATE_PUBLIC_URL` | nullable HTTP(S) URL / empty | Client-visible root of `cmd/telesrv-update`; advertised through `help.getConfig.autoupdate_url_prefix`. Empty disables native desktop updates. See `docs/update-service.md`. |
| `TELESRV_UPDATE_SERVICE_URL` | nullable HTTP(S) URL / `TELESRV_UPDATE_PUBLIC_URL` | Internal update resolver used for `help.getAppUpdate`. It may be a loopback/private route; empty falls back to the public update URL. |
| `TELESRV_UPDATE_REQUEST_TIMEOUT` | duration / `2s` | Deadline for an application-update resolve call; must be greater than zero and at most `30s`. Resolver failure returns `help.noAppUpdate`, not an RPC 500. |
| `TELESRV_PUBLIC_APP_SCHEME` | URL scheme / `telesrv` | Automatic app-open scheme on landing pages. Must match patched client registration. `tg`, `http`, and `https` are rejected. |
| `TELESRV_PUBLIC_APP_LINK_BASE` | nullable custom URL base / empty | Optional host-based root for multi-server clients, for example `owpg://example.com`. When set, links use `owpg://example.com/oauth`, `owpg://example.com/<username>`, and equivalent route paths. Only exact `<custom-scheme>://<host>` values are accepted; ports, paths, queries, and fragments are rejected. `TELESRV_PUBLIC_APP_SCHEME` remains an accepted legacy input. |
| `TELESRV_PUBLIC_WEB_BASE_URL` | HTTP(S) URL / `https://weba.telesrv.net` | Web-client root used by public username pages. Same URL validation as `TELESRV_PUBLIC_BASE_URL`. |
| `TELESRV_PUBLIC_APP_NAME` | string / `TELESRV_BRAND_PRODUCT_NAME` | Public landing-page product name; trimmed, non-empty, no control characters, maximum 64 Unicode characters. |
| `TELESRV_PUBLIC_LINK_WEB_ADDR` | nullable address / empty | Username/avatar/sticker/emoji/chatlist/collectible-gift landing pages, the hash-only moderation appeal form, and short-lived unique-gift/channel-revenue confirmation pages. Empty disables the listener; moderation appeal issuance, local gift export, and channel revenue withdrawal then fail closed because no reachable confirmation URL exists. Public landing pages remain read-only; only exact bearer-token POST routes can commit their aggregate. Production should bind loopback behind exact nginx routes with token-route access logs disabled. Local development can enable `127.0.0.1:2401` in `configs/core.yaml`. |
| `TELESRV_TELEGRAM_LOGIN_ENABLE` | bool / `false` | Mount the self-hosted Telegram Login/OIDC provider on `TELESRV_PUBLIC_LINK_WEB_ADDR`. Enabling it requires that listener and all key files below. |
| `TELESRV_TELEGRAM_LOGIN_ISSUER` | absolute origin URL / `TELESRV_PUBLIC_BASE_URL` | Exact public issuer used in discovery and tokens. HTTPS is required by default; paths, credentials, query, and fragment are rejected. The next setting permits any HTTP host/IP. |
| `TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP` | bool / `false` | When enabled, permits any valid HTTP issuer, BotFather Web origin, redirect URI, and native HTTP callback, without loopback, subnet, or port restrictions. When disabled, those Web URLs still require HTTPS. |
| `TELESRV_TELEGRAM_LOGIN_SIGNING_KEYS_FILE` | path / `data/telegram-login/signing-keys.json` | JOSE private-key ring generated by `cmd/telegramloginkeygen`; active plus retiring public keys are published through JWKS. |
| `TELESRV_TELEGRAM_LOGIN_CODE_KEYS_FILE` | path / `data/telegram-login/code-keys.json` | AES-256-GCM envelope-key ring for recoverable, one-time authorization codes. |
| `TELESRV_TELEGRAM_LOGIN_SECRET_PEPPER_FILE` | path / `data/telegram-login/client-secret-pepper` | Deployment pepper for HMAC-SHA-256 client-secret hashes. The file must contain a base64 encoding of exactly 32 random bytes. |
| `TELESRV_TELEGRAM_LOGIN_REQUEST_TTL` | duration / `5m` | Pending authorization lifetime; bounded to `1m..15m`. |
| `TELESRV_TELEGRAM_LOGIN_CODE_TTL` | duration / `2m` | One-time code lifetime; bounded to `30s..10m`. |
| `TELESRV_TELEGRAM_LOGIN_ID_TOKEN_TTL` | duration / `1h` | Signed ID-token lifetime; bounded to `1m..24h`. Retiring signing keys must cover this window. |
| `TELESRV_TELEGRAM_LOGIN_TRUSTED_PROXY_CIDRS` | comma-separated CIDRs / empty | Only requests whose direct peer is in this list may supply `Forwarded`/`X-Forwarded-*` client metadata. The documented nginx deployment uses `127.0.0.1/32,::1/128`. |
| `TELESRV_TELEGRAM_LOGIN_RETENTION` | duration / `168h` | Retention after terminal request/code/revocation state; bounded to `1h..90d`. |
| `TELESRV_TELEGRAM_LOGIN_SWEEP_INTERVAL` | duration / `5m` | Retention worker interval; bounded to `10s..1h`. |
| `TELESRV_TELEGRAM_LOGIN_SWEEP_BATCH` | int / `500` | Maximum rows per retention pass; bounded to `1..1000`. |

### 3.1 Bot API webhook troubleshooting

Start by separating the three addresses below. Never use the webhook receiver domain as the Bot
API endpoint unless an explicit reverse-proxy route maps that domain to telesrv:

| Name | Setting/source | Direction and purpose |
|---|---|---|
| Bot API listener | telesrv `TELESRV_BOT_API_ADDR` | The telesrv bind address; empty disables the gateway. `0.0.0.0` is valid only for binding and is not a client request target. |
| Bot API base URL | the bot application's `TELEGRAM_API_URL` or equivalent | A client-reachable address for telesrv, for example `http://172.17.0.1:8081`. Method URLs are `<base>/bot<TOKEN>/<method>` and file URLs are `<base>/file/bot<TOKEN>/<file_path>`. |
| Webhook receiver URL | the bot application's `WEBHOOK_URL + WEBHOOK_PATH`, registered by `setWebhook` | The target to which telesrv actively POSTs updates, for example `https://bot.example.com/webhook`. It is not the Bot API base URL. |

The network direction is different too: polling is `bot application -> telesrv Bot API`, while
webhook delivery is `telesrv -> bot application webhook receiver`. Working polling proves only the
first path. It does not prove webhook DNS, outbound TCP, TLS, reverse proxy, or Docker hairpin
connectivity.

#### 1. Query the authoritative webhook state from the Bot API

Run this inside the bot application container with its actual Bot API base URL. Do not expand and
paste the token into chat, tickets, or screenshots:

```sh
curl -sS -X POST \
  "${TELEGRAM_API_URL%/}/bot${BOT_TOKEN}/getWebhookInfo" | jq
```

If the application uses a differently named variable, replace `TELEGRAM_API_URL` with the
**client-reachable address** corresponding to `TELESRV_BOT_API_ADDR`. For example, if telesrv binds
`0.0.0.0:8081`, a container on the same host might use `http://172.17.0.1:8081`; it must not request
`http://0.0.0.0:8081`.

Interpret the result as follows:

| Result | Conclusion and next step |
|---|---|
| Empty `url` | No webhook is registered on this telesrv instance. Verify that the application uses this Bot API base URL and that startup `setWebhook` succeeded. |
| Increasing `pending_update_count` | Updates reached the telesrv durable queue but are not being delivered successfully. Inspect `last_error_message`. |
| HTTP `401`/`403` in `last_error_message` | The receiver is reachable, but its webhook secret differs or an authentication layer rejected the request. |
| `dial tcp ... i/o timeout` | telesrv cannot connect to the target IP/port. Check outbound firewall rules, Docker networking, loopback/hairpin NAT, and security groups. |
| `connection refused` | The address is reachable, but nothing listens on that port or the port mapping/reverse-proxy upstream is wrong. |
| DNS/`no such host` | The webhook hostname cannot be resolved from the telesrv runtime environment. |
| TLS/`x509` error | The certificate chain, hostname, SNI, or container CA trust is wrong. HTTPS uses the system trust store. |
| Target type absent from `allowed_updates` | Newly produced updates of that type are not queued. A normal `/start` requires at least `message`. |
| Pending reaches zero but the app does not react | telesrv received a 2xx response. Inspect the receiver's internal queue, workers, dispatcher, and handlers. |

`getWebhookInfo` reports telesrv's persisted delivery facts. An application `/health` endpoint only
proves that its receiver route and workers started; it cannot replace this check.

#### 2. Validate the receiver with the correct header

The Telegram webhook secret is distinct from the Bot token, OIDC Client Secret, and other API
keys. The receiver validates `X-Telegram-Bot-Api-Secret-Token`, not `Authorization: Bearer`:

```sh
curl -i -X POST "${WEBHOOK_URL%/}${WEBHOOK_PATH}" \
  -H 'Content-Type: application/json' \
  -H "X-Telegram-Bot-Api-Secret-Token: ${WEBHOOK_SECRET_TOKEN}" \
  -d '{"update_id":2147483000}'
```

Expect an HTTP 2xx response. `401 invalid_secret_token` proves that the request reached the
application but the header was absent or did not match. Recreate/restart the application after
editing `.env`; changing the file alone neither updates the secret already registered in telesrv
nor the receiver process's startup-time secret.

#### 3. Test from the actual telesrv network namespace

A browser or official Telegram reaching the public webhook proves only public inbound
connectivity. Repeat the test from the host, container, or network namespace that actually runs
telesrv:

```sh
docker exec <telesrv-container> sh -lc \
  'getent hosts bot.example.com; curl -vk --connect-timeout 10 https://bot.example.com/health/unified'
```

If public clients work but this returns `dial tcp ...:443: i/o timeout`, a same-host public-IP
hairpin failure is a common cause. Prefer split DNS or a container host mapping so the public
hostname resolves to the reverse proxy's internal entry point inside the telesrv container while
preserving the hostname, HTTPS SNI, and certificate validation. If the reverse proxy publishes
443 on the Docker host, test first with:

```sh
curl -vk --resolve bot.example.com:443:172.17.0.1 \
  https://bot.example.com/health/unified
```

After that succeeds, a deployment may use a network-appropriate Compose entry such as:

```yaml
extra_hosts:
  - "bot.example.com:host-gateway"
```

Other fixes include attaching telesrv to the reverse proxy's Docker network, allowing the Docker
subnet to reach host port 443, or correcting cloud security-group/NAT hairpin rules. telesrv allows
an internal HTTP receiver, but use one only on a controlled shared network and only when the
application's `WEBHOOK_URL` is not also its public OIDC, payment, or media callback base. Do not
blindly replace a global public URL with an internal address to mask a routing problem.

#### 4. Close the loop after the fix

1. Restart the bot application so it calls `setWebhook` again with the current URL, secret, and
   `allowed_updates`.
2. Send a new `/start` or press a callback button.
3. Call `getWebhookInfo` again. `pending_update_count` should fall to `0`, with no new
   `last_error_date`.
4. Inspect telesrv Warning logs for `bot api webhook delivery failed`. The record contains
   `bot_user_id`, `retry_in`, and the failure reason, but must not contain the webhook URL, Bot
   token, or secret.
5. Confirm that the receiver recorded and processed the `update_id`. Delivery is at-least-once, so
   the application must safely handle duplicate updates caused by retries.

Immediately rotate any Bot token, webhook secret, OIDC Client Secret, API key, or database
password exposed in shell history, chat, or screenshots. Keep only redacted diagnostics in support
material.

### 3.2 Complete Telegram Login / OIDC setup

#### 1. Generate `data/telegram-login` once

Run this from the `telesrv` repository root:

```powershell
go run ./cmd/telegramloginkeygen -mode init -dir data/telegram-login
Get-ChildItem .\data\telegram-login
```

The same command works on Linux; restrict the generated directory afterward:

```bash
go run ./cmd/telegramloginkeygen -mode init -dir data/telegram-login
chmod 0700 data/telegram-login
chmod 0600 data/telegram-login/*
```

Initialization creates the following private files. It never prints key material and refuses to
overwrite an existing `signing-keys.json`, `code-keys.json`, or `client-secret-pepper`:

- `signing-keys.json` plus three `signing-*.pem` files: the manifest and private keys for RS256,
  ES256, and EdDSA ID-token signatures;
- `code-keys.json`: the AES-256-GCM envelope-key ring for one-time authorization codes;
- `client-secret-pepper`: a 32-byte deployment pepper used to store and verify OIDC Client Secret
  digests.

The repository ignores `data/*` by default. Never put this directory in Git, release archives,
logs, or ordinary backups. All instances must mount the same protected files and restart together
after rotation. Losing the pepper invalidates existing Client Secret verification. Losing a signing
key that is still in its publication window invalidates otherwise-live ID tokens against JWKS.

#### 2. Configure and start the Provider

This example exposes OIDC directly at `http://192.0.2.25:2401`; replace it with the server address
that clients can actually reach. Bind `0.0.0.0:2401` for direct LAN/public access, or keep
`127.0.0.1:2401` when an on-host reverse proxy is the only caller:

```env
TELESRV_PUBLIC_BASE_URL=http://192.0.2.25:2401
TELESRV_PUBLIC_LINK_WEB_ADDR=0.0.0.0:2401
TELESRV_PUBLIC_APP_SCHEME=telesrv
# Optional for multi-server clients: TELESRV_PUBLIC_APP_LINK_BASE=owpg://example.com

TELESRV_TELEGRAM_LOGIN_ENABLE=true
TELESRV_TELEGRAM_LOGIN_ISSUER=http://192.0.2.25:2401
TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP=true
TELESRV_TELEGRAM_LOGIN_SIGNING_KEYS_FILE=data/telegram-login/signing-keys.json
TELESRV_TELEGRAM_LOGIN_CODE_KEYS_FILE=data/telegram-login/code-keys.json
TELESRV_TELEGRAM_LOGIN_SECRET_PEPPER_FILE=data/telegram-login/client-secret-pepper
```

For HTTPS, set the issuer and public base to the exact HTTPS origin and leave
`TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP=false`. The issuer becomes the token `iss` and the root of all
discovery endpoints, so its scheme, host, and port must exactly match the address used by relying
parties. Start or restart `telesrv`, then verify the public endpoints:

```powershell
curl.exe http://192.0.2.25:2401/.well-known/openid-configuration
curl.exe http://192.0.2.25:2401/.well-known/jwks.json
curl.exe -I http://192.0.2.25:2401/js/telegram-login.js
curl.exe -I 'http://192.0.2.25:2401/js/telegram-widget.js?23'
```

The discovery `issuer` must equal the configured value, and its `authorization_endpoint`,
`token_endpoint`, and `jwks_uri` must be reachable by the relying party. A reverse proxy must pass
through `/.well-known/openid-configuration`, `/.well-known/jwks.json`, `/auth`, `/auth/status`,
`/token`, `/crossapp`, `/inapp`, `/telegram-login.js`, `/js/telegram-login.js`,
`/telegram-widget.js`, `/js/telegram-widget.js`, and `POST /telegram-widget/resolve` unchanged.

#### 3. Create an OIDC Client with the local `@BotFather`

Create a bot with `/newbot` or select an existing bot. In the local `@BotFather`, run `/setlogin`
and choose that bot. Initial setup returns:

- `Client ID`: the bot user ID as a decimal string;
- `Client Secret`: shown once, separate from the Bot API token, and meant to be saved immediately
  in a secret manager.

After selecting a bot once, BotFather keeps that configuration session active; there is no need to
repeat `/setlogin` and the bot username for every change. Send commands one at a time or paste them
as separate lines in one message (up to 32 lines per message). This example runs the relying party
at `http://192.0.2.30:3000`:

```text
add origin http://192.0.2.30:3000
add redirect http://192.0.2.30:3000/oauth/callback
algorithm RS256
enable
```

Send `/done` after the changes succeed. BotFather closes the session and returns the final
configuration summary. Every successful change takes effect immediately, so `/cancel` only closes
the session and does not roll back changes. If a multi-line message fails partway through,
BotFather identifies the applied lines, the failed line, and the later lines that were skipped,
then keeps the selected bot active for a corrected command.

An `origin` is an exact Web origin without a path, query, or fragment; it authorizes the JS SDK,
popup CORS, and legacy `login_url`. A `redirect` is the exact full URI that receives an
Authorization Code. Wildcards and prefix matching are not supported. Use `/logininfo` to inspect
status and registrations; use `/setlogin` to add/remove URLs, change the algorithm, or disable the
client; use `/resetloginsecret` to rotate the Client Secret. Available algorithms are RS256,
ES256, EdDSA, and ES256K only when its build/key ring is present. EdDSA and ES256K accept only the
`openid` scope.

#### 4. Integrate a relying party with standard OIDC

Start by loading:

```text
http://192.0.2.25:2401/.well-known/openid-configuration
```

The standard flow is Authorization Code with PKCE S256:

1. Generate random `state`, `nonce`, and PKCE `code_verifier`; derive the S256 `code_challenge`.
2. Open the discovery `authorization_endpoint` with `client_id`, the exact `redirect_uri`,
   `response_type=code`, a `scope` containing `openid`, `state`, `nonce`, `code_challenge`, and
   `code_challenge_method=S256`.
3. After the user approves in TDesktop/Android, verify `state` at the relying-party callback and
   read the one-time code.
4. Server-side, POST `grant_type=authorization_code`, the code, the same `redirect_uri`, and
   `code_verifier` to the discovery `token_endpoint`. Confidential clients authenticate with HTTP
   Basic or `client_secret_post`.
5. Verify the ID-token signature with the discovery `jwks_uri`, then strictly validate `iss`,
   `aud`, `exp`, `nonce`, and a non-empty `sub`. Decoding without signature verification is not
   sufficient.

Supported scopes are `openid`, `profile`, `phone`, and `telegram:bot_access`. The provider does not
currently expose UserInfo, refresh tokens, or an introspection endpoint. Browser applications may
load `<issuer>/js/telegram-login.js` for the local JS SDK. A Client Secret must remain server-side.

For an existing frontend that uses Telegram's attribute-based Login Widget, replace only the widget
script source; do not use `https://telegram.org/js/telegram-widget.js` for a bot that exists only in
telesrv:

```html
<script async src="https://<telesrv-issuer>/js/telegram-widget.js?23"
  data-telegram-login="BotName"
  data-size="large"
  data-radius="10"
  data-request-access="write"
  data-lang="en"
  data-onauth="onTelegramAuth(result)"></script>
<script>
  function onTelegramAuth(result) {
    // telesrv SDK result: { id_token, user }, or { error }
  }
</script>
```

`data-client-id="<numeric bot user id>"` is preferred when supplied. With only
`data-telegram-login`, the shim resolves the current local bot username only if its Login client is
enabled and the browser's exact Origin is registered for that bot. `data-size` and `data-radius`
style the simple local button, `data-lang` is forwarded to the Login SDK, and
`data-request-access="write"` requests `openid profile telegram:bot_access`; without it the shim
requests `openid profile`. The `data-onauth` callback receives the existing telesrv SDK result
`{id_token, user}` rather than a fabricated official-widget payload.

Mini Apps are a separate surface and must continue to load the official
`https://telegram.org/js/telegram-web-app.js`. The telesrv `telegram-widget.js` shim does not replace
the Mini App script or alter Mini App behavior.

#### 5. Verify the complete path with the Bedolaga demo

Install the demo dependencies:

```powershell
python -m venv "$env:TEMP\telesrv-bedolaga-demo-venv"
& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" -m pip install `
  -r .\cmd\bots\bedolagaformat\requirements.txt
```

Put the Client ID/Secret from step 3 and the same bot's Bot API token only in process environment:

```powershell
$env:TELESRV_BOT_TOKEN = "<bot_id>:<bot_api_secret>"
$env:TELESRV_BOT_API_SERVER = "http://192.0.2.25:8081"
$env:TELESRV_BOT_LOGIN_DEMO = "1"
$env:TELESRV_BOT_LOGIN_ISSUER = "http://192.0.2.25:2401"
$env:TELESRV_BOT_LOGIN_CLIENT_ID = "<Client ID returned by BotFather>"
$env:TELESRV_BOT_LOGIN_CLIENT_SECRET = "<one-time OIDC Client Secret>"
$env:TELESRV_BOT_LOGIN_PUBLIC_URL = "http://192.0.2.30:3000"
$env:TELESRV_BOT_LOGIN_LISTEN = "0.0.0.0:3000"

& "$env:TEMP\telesrv-bedolaga-demo-venv\Scripts\python.exe" `
  .\cmd\bots\bedolagaformat\demo.py --drop-pending --login-demo
```

The BotFather origin must equal `TELESRV_BOT_LOGIN_PUBLIC_URL`, and the redirect must equal
`<TELESRV_BOT_LOGIN_PUBLIC_URL>/oauth/callback`. Send `/logindemo` to the bot. The first button
tests Bot API `login_url` plus the HMAC callback; the second page tests the local JS SDK popup and
Authorization Code + PKCE/JWKS. Omitting the Client Secret leaves JS popup verification available
but explicitly disables the server-side code flow.

#### 6. Rotate keys

When rotating a signing key, retain the old public key for at least the configured ID-token TTL
plus ten minutes. Restart all instances together after the operation:

```powershell
go run ./cmd/telegramloginkeygen -mode rotate-signing -algorithm RS256 `
  -id-token-ttl 1h -publish-for 2h -dir data/telegram-login
go run ./cmd/telegramloginkeygen -mode rotate-code -dir data/telegram-login
```

Run `rotate-signing` separately for RS256, ES256, or EdDSA. `rotate-code` retains old code keys and
adds a new active key. Do not edit manifests or PEM files manually, and never generate divergent
key rings independently on different instances.

## 4. PostgreSQL, Redis, files, and seed data

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_POSTGRES_DSN` | secret DSN / `postgres://telesrv:telesrv@127.0.0.1:5432/telesrv_v2?sslmode=disable` | Primary durable business database. Local `main` and `v2` use separate `telesrv_main` / `telesrv_v2` databases because their migration histories differ. Production must replace the development credentials and TLS policy. |
| `TELESRV_POSTGRES_MAX_CONNS` | int / `50` | pgxpool maximum connections. `<=0` delegates to pgx defaults, which are usually too small for production outbox/RPC concurrency. |
| `TELESRV_POSTGRES_MIN_CONNS` | int / `16` | pgxpool pre-warmed minimum connections. |
| `TELESRV_REDIS_ADDR` | address / `127.0.0.1:6399` | Redis used for volatile codes, limits, and shared update/cache state. |
| `TELESRV_REDIS_PASSWORD` | secret string / empty | Redis password. |
| `TELESRV_REDIS_DB` | int / `0` | Redis logical database number. |
| `TELESRV_LANGPACK_SEED_DIR` | path / `data/langpack` | TDesktop `.strings` language-pack seed directory. |
| `TELESRV_OFFICIAL_GIFTS_DIR` | path / `data/official-gifts` | Read-only snapshot generated by `cmd/giftfetch`, used for verified explicit imports in the admin UI. |
| `TELESRV_BLOB_BACKEND` | enum / `localfs` | The only permanent media backend: `localfs` or `s3`. There is no cross-backend fallback, dual read, or dual write; startup rejects rows for another backend. |
| `TELESRV_BLOB_DIR` | path / `data/blobs` | Permanent media and transient upload parts for `localfs`; S3 mode never falls back to this directory. |
| `TELESRV_BLOB_STAGING_DIR` | path / `data/blob-staging` | Local transient upload parts and write spool in `s3` mode; not a permanent backend. |
| `TELESRV_S3_ENDPOINT` | host[:port] / empty | Required in `s3` mode, without an `http://` or `https://` scheme. |
| `TELESRV_S3_REGION` | string / `us-east-1` | S3 region. |
| `TELESRV_S3_BUCKET` | string / empty | Required permanent-media bucket in `s3` mode. |
| `TELESRV_S3_ACCESS_KEY_ID` | secret / empty | Required in `s3` mode; no weak shipped credential. |
| `TELESRV_S3_SECRET_ACCESS_KEY` | secret / empty | Required in `s3` mode and never logged. |
| `TELESRV_S3_USE_SSL` | bool / `true` | Use TLS for the S3 endpoint. |
| `TELESRV_S3_PATH_STYLE` | bool / `false` | Force path-style bucket lookup; commonly `true` for local MinIO. |
| `TELESRV_S3_CREATE_BUCKET` | bool / `false` | Allow startup to create a missing bucket only when explicitly enabled. |
| `TELESRV_STORAGE_LOW_SPACE_GUARD_ENABLE` | bool / `true` | Enable storage capacity admission. Startup requires a valid initial snapshot; refresh failures retain the last valid snapshot and log a warning. |
| `TELESRV_STORAGE_MIN_FREE_BYTES` | int64 / `1073741824` | Minimum local filesystem free bytes. It protects permanent files and upload parts for `localfs`, and local upload/spool space for `s3`; `0` disables this dimension. |
| `TELESRV_STORAGE_MAX_TOTAL_BYTES` | int64 / `0` | Optional S3 permanent-byte budget, counted over unique `object_key` values tracked by `file_blobs`; `0` disables it. This is not bucket billing usage. |
| `TELESRV_STORAGE_USAGE_REFRESH_INTERVAL` | duration / `1m` | Capacity snapshot refresh interval; must be positive while the guard is enabled. |
| `TELESRV_STICKER_SEED_DIR` | path / `data/sticker-seed` | Sticker/reaction seed packages imported into documents, sticker sets, and blobs. |
| `TELESRV_STICKER_SEED_MAX_SETS` | int / `300` | Maximum regular sticker sets imported at startup; `<=0` means unlimited. |
| `TELESRV_GIF_SEED_DIR` | path / `data/gifs` | Optional built-in `@gif` startup directory. It accepts only `.gif/.mp4`, with at most 50 total entries; a missing directory is skipped, while changed same-name content or invalid media fails startup. Outputs use only the explicitly selected blob backend. |
| `TELESRV_PREMIUM_PROMO_SEED_DIR` | path / `data/premium-promo` | Exported `help.getPremiumPromo` manifest, MP4 videos, and JPEG thumbnails. A missing directory keeps the no-video fallback; an existing invalid/incomplete directory fails startup. |

The language-pack file manifest is authoritative. To add a language, place `data/langpack/<pack>/<pack>_<lang>_v<version>.strings` and restart `telesrv`. The `pack` must match its first-level directory and may use the letters, digits, `-`, and `_` already used by Telegram (for example, `android_x`); `lang` is canonicalized to lowercase with hyphens (`pt_BR` becomes `pt-br`). Only the highest file version for each language is loaded. Effective content changes require a version bump; same-version effective mutations and version rollbacks stop startup. Removing a language file or an entire pack subdirectory atomically removes its database catalog and strings on the next restart. Startup streams a source-file SHA-256 first: unchanged files reuse the last atomic manifest without parsing strings or writing the database, while only new or changed files are parsed and replaced through PostgreSQL `COPY`.

Object storage is intentionally single-backend. Do not flip
`TELESRV_BLOB_BACKEND` until an explicit offline migration has copied the
objects, verified size/SHA-256, and updated `file_blobs.backend`.

## 5. Authentication, OTP providers, SMTP, and passkeys

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_DEV_AUTH_CODE` | sensitive string / `12345` | Fixed code used by `PHONE_CODE_DELIVERY_PROVIDER=development`; do not expose this default publicly. |
| `TELESRV_AUTH_CODE_TTL` | duration / `5m` | Login/registration/email verification code lifetime; must be positive. |
| `TELESRV_AUTH_CODE_MAX_ATTEMPTS` | int / `5` | Maximum wrong attempts for one code/hash; must be positive. |
| `TELESRV_PHONE_CODE_LENGTH` | int / `5` | Random SMS-code length for the `webhook` phone provider; allowed range `4..10`. |
| `TELESRV_AUTH_CODE_PHONE_RATE_LIMIT` | int / `5` | Code issuance limit per normalized phone digest per rate window; `<=0` disables this dimension. |
| `TELESRV_AUTH_CODE_AUTH_KEY_RATE_LIMIT` | int / `20` | Code issuance limit per raw auth key per rate window; `<=0` disables this dimension. |
| `TELESRV_AUTH_CODE_RATE_WINDOW` | duration / `10m` | Shared window for phone and auth-key issuance limits. |
| `TELESRV_PHONE_CODE_DELIVERY_PROVIDER` | enum / `development` | `development` uses fixed codes; `webhook` generates random SMS codes for login, registration, and phone changes. Both modes first commit the same code to the durable 777000 dialog for existing accounts; Webhook is additive. |
| `TELESRV_EMAIL_CODE_DELIVERY_PROVIDER` | enum / `smtp` | Delivery implementation for login-email and email setup/change codes: `smtp` or `webhook`. Existing-account login-email codes are first mirrored to 777000; setup/change remains provider-only. |
| `TELESRV_OTP_WEBHOOK_URL` | absolute URL / empty | Required when any provider selects `webhook`; see [otp-delivery.md](otp-delivery.md) for the fixed v1 contract. Any valid `http://` or `https://` host/IP and port is accepted; userinfo is rejected. |
| `TELESRV_OTP_WEBHOOK_SECRET` | secret string / empty | Optional HMAC-SHA256 signing secret; enables `X-Telesrv-Signature` when non-empty. |
| `TELESRV_OTP_WEBHOOK_TIMEOUT` | duration / `5s` | Webhook HTTP timeout; must be positive when Webhook delivery is enabled. |
| `TELESRV_LOGIN_EMAIL_ENABLE` | bool / `false` | Enables login-email verification. SMTP settings are required only when the email provider is `smtp`. |
| `TELESRV_LOGIN_EMAIL_REQUIRE_SETUP` | bool / `false` | Forces accounts without a login email to configure one. Requires `TELESRV_LOGIN_EMAIL_ENABLE=true`. |
| `TELESRV_LOGIN_EMAIL_CODE_LENGTH` | int / `6` | Email verification-code length; allowed range `4..10`. |
| `TELESRV_SMTP_HOST` | string / empty | SMTP server host; required when login email is enabled with the `smtp` provider. |
| `TELESRV_SMTP_PORT` | int / `587` | SMTP port; must be `1..65535` when the SMTP provider is used. |
| `TELESRV_SMTP_USERNAME` | sensitive string / empty | SMTP username. Also used as sender when `TELESRV_SMTP_FROM` is empty. |
| `TELESRV_SMTP_PASSWORD` | secret string / empty | SMTP password. |
| `TELESRV_SMTP_FROM` | email/string / empty | Envelope/header sender. Either this or SMTP username is required when login email is enabled. |
| `TELESRV_SMTP_FROM_NAME` | string / `TELESRV_BRAND_PRODUCT_NAME` | Display name for login-email messages; when unset, it inherits the parent product display name. |
| `TELESRV_SMTP_TLS` | enum / `starttls` | `starttls`, `tls`, or `none`; any other value fails startup. |
| `TELESRV_SMTP_TIMEOUT` | duration / `10s` | SMTP operation timeout; must be positive when the SMTP provider is used. |
| `TELESRV_PASSKEY_RP_ID` | hostname / `telesrv.net` | WebAuthn relying-party ID used for `rpIdHash`. Android Credential Manager requires alignment with hosted `assetlinks.json`. |
| `TELESRV_PASSKEY_ALLOWED_ORIGINS` | list / empty | Allowed WebAuthn origins. Empty disables explicit origin enforcement because Android APK-key-hash origins may not be known in advance. |

## 6. Maps, external media, previews, and uploads

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_MAPBOX_TOKEN` | secret string / empty | Mapbox Static Images access token for `upload.getWebFile` map previews. Empty uses deterministic placeholders. |
| `TELESRV_MAPTILE_CACHE_DIR` | path / `data/maptiles` | Disk cache for fetched map thumbnails, preserving byte-stable chunk downloads and limiting quota use. |
| `TELESRV_EXTERNAL_MEDIA_ENABLE` | bool / `true` | Enables SSRF-protected fetching of external photo/document URLs. |
| `TELESRV_EXTERNAL_MEDIA_MAX_BYTES` | int bytes / `10485760` | Maximum response body per external-media fetch. Downstream treats `<=0` as the 10 MiB safe default. |
| `TELESRV_EXTERNAL_MEDIA_RATE_PER_MIN` | int / `60` | Global external-media fetches per minute. Downstream treats `<=0` as its default. |
| `TELESRV_WEBPAGE_PREVIEW_ENABLE` | bool / `true` | Enables SSRF-protected Web-page metadata/image fetching for link previews. |
| `TELESRV_WEBPAGE_PREVIEW_MAX_BYTES` | int bytes / `5242880` | Response cap shared by preview HTML and image fetching. Downstream treats `<=0` as the 5 MiB default. |
| `TELESRV_WEBPAGE_PREVIEW_RATE_PER_MIN` | int / `300` | Global preview upstream requests per minute; one preview may make at most two requests. |
| `TELESRV_UPLOAD_PART_TTL` | duration / `24h` | Retention for unassembled upload parts. |
| `TELESRV_UPLOAD_PART_GC_INTERVAL` | duration / `30m` | Upload-part GC polling interval. |
| `TELESRV_UPLOAD_PART_GC_BATCH` | int / `10000` | Maximum rows removed per upload-part GC batch. |
| `TELESRV_UPLOAD_INFLIGHT_MAX_BYTES` | int64 bytes / `4194304000` | Per-user unassembled upload-byte cap; `<=0` means unlimited. |
| `TELESRV_UPLOAD_INFLIGHT_MAX_PARTS` | int / `8000` | Per-user unassembled upload-part row cap; `<=0` means unlimited. |
| `TELESRV_UPLOAD_INFLIGHT_MAX_FILES` | int / `64` | Per-user concurrent unassembled `file_id` cap; `<=0` means unlimited. |

## 7. AI compose and business automation

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_BUSINESS_AI_PROVIDER` | string / `echo` | Business auto-reply generator. Allowed values are `echo`/empty (echo the triggering text), `template`/`quick_reply`/`quick-reply` (use quick-reply templates), or `ai`/`compose_ai`/`ai_compose`/`aicompose`/`kimi` (reuse the `TELESRV_AI_PROVIDERS` provider chain). This setting does not accept arbitrary provider names; for example, with Ollama set `TELESRV_BUSINESS_AI_PROVIDER=ai` and select the actual provider through `TELESRV_AI_PROVIDERS=ollama,local`. |
| `TELESRV_AI_ENABLED` | bool / `true` | Enables client compose rewrite/polish. False returns no tones and hides the entry. |
| `TELESRV_AI_PROVIDERS` | list / `local` | Ordered provider chain. Empty resolves to deterministic `local`, which makes no external request. |
| `TELESRV_AI_TIMEOUT` | duration / `15s` | Total timeout for one provider call. |
| `TELESRV_AI_RATE_LIMIT` | int / `20` | Per-account compose operations per window. |
| `TELESRV_AI_RATE_WINDOW` | duration / `1m` | Compose AI rate-limit window. |
| `TELESRV_AI_LOG_CONTENT` | bool / `false` | When false, logs contain lengths/provider/status only. Enabling may expose user prompts and generated text. |
| `TELESRV_TRANSLATION_ENABLED` | bool / `true` | Enables `messages.translateText`; at least one remote AI provider is still required, and the local echo provider is never treated as translation. |
| `TELESRV_TRANSLATION_PROVIDERS` | list / empty | Selects provider names from `TELESRV_AI_PROVIDERS`; empty uses every configured remote provider. |
| `TELESRV_TRANSLATION_TIMEOUT` | duration / `15s` | Total timeout for one batch; batches contain at most 20 texts and use fixed provider concurrency of 4. |
| `TELESRV_TRANSLATION_RATE_LIMIT` | int / `60` | Per-account translated text items per window; a 20-item batch costs 20 to prevent provider-call amplification. |
| `TELESRV_TRANSLATION_RATE_WINDOW` | duration / `1m` | Translation rate-limit window. |

Chat translation sends message bodies explicitly selected by the user to the configured external provider. Default logs omit content, but deployments should still disclose the upstream processor in their privacy policy. With only `local` configured, telesrv returns `TRANSLATIONS_DISABLED` instead of presenting source text as a translation.

For each name in `TELESRV_AI_PROVIDERS`, telesrv uppercases it, converts non-alphanumeric characters to `_`, and reads the following dynamic keys. Example: provider `openai-compatible` uses suffix `OPENAI_COMPATIBLE`.

| Dynamic setting | Type / default | Description |
|---|---|---|
| `TELESRV_AI_<NAME>_KIND` | string / derived from name | Adapter kind. Built-ins: `local`, `openai_responses`, `openai_chat`, `gemini`, `anthropic`. Names `openai`, `openai_chat`/`openai-compatible`/`openai_compat`, `gemini`, and `anthropic` map to their corresponding built-in kind. |
| `TELESRV_AI_<NAME>_BASE_URL` | URL string / empty | Optional provider endpoint override. Required by some compatible/self-hosted providers. |
| `TELESRV_AI_<NAME>_API_KEY` | secret string / provider fallback | Provider credential. For known providers it falls back to the process variables below. |
| `TELESRV_AI_<NAME>_MODEL` | string / empty | Provider model identifier. External providers generally require it. |
| `TELESRV_AI_<NAME>_MAX_OUTPUT_TOKENS` | int / `1024` | Requested output-token cap. |
| `TELESRV_AI_<NAME>_TEMPERATURE` | float / `0.2` | Sampling temperature. |
| `TELESRV_AI_<NAME>_OMIT_TEMPERATURE` | bool / `false` | Omits the temperature field for models/providers that reject it. |
| `TELESRV_AI_<NAME>_THINKING` | string / empty | Provider-specific thinking/reasoning mode, normalized to lowercase; for example `disabled`. |

The following fallback keys are accepted from the **process environment only**. The env file rejects them because they do not start with `TELESRV_`: `OPENAI_API_KEY`, `GEMINI_API_KEY`, and `ANTHROPIC_API_KEY`. A provider-specific `TELESRV_AI_<NAME>_API_KEY` takes precedence.

## 8. Read-model and auth-key caches

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_TEMP_KEY_CACHE_MAX_ENTRIES` | int / `262144` | Router temporary→permanent auth-key binding cache capacity. |
| `TELESRV_TEMP_KEY_CACHE_TTL` | duration / `30m` | Recheck period; exact bind/revoke invalidation handles normal writes, while TTL covers cross-process/exception paths. |
| `TELESRV_AUTH_USER_CACHE_TTL` | duration / `1m` | Positive auth-key→user authorization cache recheck period. Write-side Redis invalidation remains the primary path; TTL bounds stale authorization if cross-Core invalidation/control messages are missed. `<=0` disables the TTL fallback. |
| `TELESRV_AUTH_KEY_CACHE_MAX_ENTRIES` | int / `262144` | Edge raw auth-key hot-cache capacity. Edge does not connect to PostgreSQL; CoreExec/PG is authoritative, while Edge caches only key material, server salt, and expiry, not Layer/client metadata. Must be greater than `0`. |
| `TELESRV_AUTH_KEY_CACHE_TTL` | duration / `30m` | Resource TTL for the Edge auth-key hot cache. It only bounds memory use: `destroy_auth_key` publishes a Core Redis invalidation and closes raw-key sessions, so correctness does not depend on TTL. Must be `(0,24h]`. |
| `TELESRV_CHANNEL_ROW_CACHE_MAX` | int / `50000` | Shared channel-row cache capacity. `<=0` disables both cache and its LISTEN/NOTIFY listener. |
| `TELESRV_CHANNEL_MEMBER_CACHE_MAX` | int / `100000` | Channel member/access read-model cache capacity; `<=0` disables it. |
| `TELESRV_CHANNEL_DIALOG_CACHE_MAX` | int / `100000` | Viewer/channel dialog projection cache capacity; `<=0` disables it. |
| `TELESRV_CHANNEL_BOOST_CACHE_MAX` | int / `100000` | Channel boost read-model cache capacity; `<=0` disables it. |
| `TELESRV_CHANNEL_BOOST_CACHE_TTL` | duration / `10s` | Maximum stale window if a boost invalidation notification is missed. |

## 9. Outbox, push, limits, retention, and GC

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_OUTBOX_WORKERS` | int / `4` | Concurrent `cmd/telesrv-egress` outbox workers. Stable logical sharding preserves per-user head order for both PTS `dispatch_outbox` and non-PTS `edge_delivery_outbox`. |
| `TELESRV_OUTBOX_BATCH` | int / `100` | Maximum rows claimed per `cmd/telesrv-egress` batch. Larger batches improve throughput but increase DB/push bursts. |
| `TELESRV_OUTBOX_LEASE_TIMEOUT` | duration / `30s` | Time before a `dispatching` row can be reclaimed by another Egress instance. Must exceed worst-case batch delivery time. |
| `TELESRV_OUTBOX_POISON_RETENTION` | duration / `1m` | Diagnostic retention for terminal failed delivery heads; durable update events remain recoverable through difference. |
| `TELESRV_OUTBOX_POISON_CLEANUP_INTERVAL` | duration / `15s` | Cleanup interval for terminal failed heads, independent of large-table retention. |
| `TELESRV_OUTBOUND_PUSH_TIMEOUT` | duration / `200ms` | Maximum wait for Edge to confirm online update enqueue. Timeout is treated as unconfirmed delivery, leaving the durable outbox row for lease/reclaim. |
| `TELESRV_SEND_RATE_LIMIT` | int / `30` | Per-account messages per send window; `<=0` disables send limiting. |
| `TELESRV_SEND_RATE_WINDOW` | duration / `1m` | Send-rate window. |
| `TELESRV_CATCHUP_RATE_LIMIT` | int / `0` | Per-user difference/catch-up RPCs per window; `<=0` disables the gate. |
| `TELESRV_CATCHUP_RATE_WINDOW` | duration / `1m` | Catch-up rate-limit window. |
| `TELESRV_CHANNEL_NUDGE_MAX_TARGETS` | int / `0` | Maximum targets for one channel fan-out nudge; `<=0` uses the built-in default. |
| `TELESRV_UPDATE_EVENT_RETENTION` | duration / `168h` | Durable update-log retention. Cleanup only removes events covered by protocol-safe watermarks/state. |
| `TELESRV_BOT_API_UPDATE_RETENTION` | duration / `24h` | Maximum Bot API update queue retention; acknowledged rows also have a shorter fixed grace period. |
| `TELESRV_ORPHAN_AUTH_KEY_RETENTION` | duration / `24h` | Minimum retention for handshake-created keys with no authorization/temp binding/active connection. |
| `TELESRV_RETENTION_INTERVAL` | duration / `1h` | General retention worker interval. |
| `TELESRV_RETENTION_BATCH` | int / `10000` | Maximum rows deleted by one general retention batch. |

Moderation report/evidence/case/decision/action/appeal rows are durable audit facts and are not removed by the
general retention worker. The same worker deletes expired sponsored impressions and appeal links in bounded seek
batches, and deletes auth-delivery diagnostics and client telemetry after their fixed 30-day privacy retention.
The raw appeal token is never persisted. Production reverse proxies must expose only `/appeal/<token>` to the
public-link listener, preserve HTTPS in `TELESRV_PUBLIC_BASE_URL`, cap request bodies, and must not log the tokenized
path. `TELESRV_PUBLIC_BASE_URL` must resolve to that proxy for moderation freeze actions.

## 10. Premium and Stars development grants

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_PREMIUM_BOT_USERNAME` | username / `premiumbot` | Reserved built-in Premium storefront username. A leading `@` is normalized away; invalid usernames fail startup. |
| `TELESRV_PREMIUM_BOT_USER_ID` | int64 / `1250000015` | Stable built-in Premium bot user ID. It must be positive and must not collide with another system account. |
| `TELESRV_PREMIUM_PLANS` | `months:days:stars` CSV / `3:90:750,6:180:1300,12:365:2400` | Seeds and synchronizes config-owned Premium plans. Months must be unique; all values are positive and bounded. Saving a row in the admin UI makes it admin-owned, so later restarts do not overwrite it. A catalog change bumps the durable plan version and invalidates outstanding old-price forms. |
| `TELESRV_PREMIUM_GRANT_MONTHS` | int / `3` | Premium months granted to newly registered users; `0` disables new grants. Existing migration backfills are unaffected. |
| `TELESRV_STARS_STARTING_GRANT` | int64 / `1000` | Idempotent lazy starting Stars balance for all accounts; `0` disables automatic grant. |
| `TELESRV_PREMIUM_SWEEP_INTERVAL` | duration / `1m` | Expired-premium cleanup/push interval. Read paths derive expiry independently. |
| `TELESRV_PREMIUM_SWEEP_BATCH` | int / `500` | Maximum expired premium rows processed per sweep. |
| `star_gifts.sweep_interval` | duration / `15s` | Core's local Star Gift offer/auction lifecycle sweep interval. |
| `star_gifts.sweep_batch` | int / `1000` | Maximum offer/auction/outbox work claimed per lifecycle sweep. |
| `star_gifts.ton_starting_grant` | int64 / `10000000000` | Nanoton granted idempotently on a user's first access to the internal telesrv TON ledger; `0` disables it. This is not an on-chain asset. |
| `star_gifts.transfer_stars` | int64 / `25` | Stars charged for a collectible transfer; `0` enables the free-transfer RPC. |
| `star_gifts.drop_original_details_stars` | int64 / `25` | Stars charged to remove a collectible's original sender/message details. |
| `star_gifts.offer_min_stars` | int / `1` | Minimum Stars offer snapshotted for user-owned collectibles; `0` disables the offer entry point. |
| `star_gifts.stars_proceeds_permille` | int / `1000` | Seller share in Stars sales, in permille; the remainder is recorded as platform commission. |
| `star_gifts.ton_proceeds_permille` | int / `1000` | Seller share in internal-TON sales, in permille; this affects only the local ledger. |
| `star_gifts.export_delay` | duration / `0s` | Delay snapshotted into `can_export_at` when a collectible is issued. |
| `star_gifts.transfer_delay` | duration / `0s` | Delay snapshotted into `can_transfer_at`. |
| `star_gifts.resell_delay` | duration / `0s` | Delay snapshotted into `can_resell_at`. |
| `star_gifts.craft_delay` | duration / `0s` | Delay snapshotted into `can_craft_at`. |
| `star_gifts.craft_chance_permille` | int / `250` | Per-input local craft success contribution, capped at 1000 permille. |
| `star_gifts.export.mode` | `local` / `ton` / `disabled`; default `local` | `local` preserves the current same-origin gift export, `disabled` disables only gift export, and `ton` delegates to the isolated TON worker. None of these modes disables channel Stars/internal-TON revenue claims. A TON worker that fails admission must fail closed instead of falling back to local export. |

Core reads the following `star_gifts.export.ton` keys only when `mode: ton` activates the production chain flow:

| YAML setting | Type / code default | Description and constraints |
|---|---|---|
| `network` | `mainnet` / `testnet`; default `mainnet` | Core intents, TON Proof, and the worker must use the same network. Set it explicitly in production. |
| `collection_address` | string / required | Fixed TEP-62 collection address; it must match the worker. |
| `collection_code_hash` | 32-byte hex/Base64 / required | Collection code hash. Core compares it with the durable admission written after the worker's chain preflight. |
| `mint_abi` | `basic-collection-v1` / required | Single-item mint ABI for the TON reference collection (opcode `0x00000001`). TEP-62 does not define a mint opcode; other values fail startup. |
| `initial_item_index` | uint64 decimal string / required | First telesrv-managed item index. Never move it backward or reuse it after rollout. |
| `proof_domain` | hostname / required | TON Connect proof domain; it must match the public Export/Claim Web host. |
| `capability_secret_file` | path / required | File contents are Base64 and must decode to 32--64 bytes. Never reuse the claim bot token or relayer mnemonic. |
| `allow_user_ids` | positive int64 sequence / empty | Empty admits all otherwise eligible users; non-empty is the mainnet canary allowlist for new intents. Duplicates and non-positive IDs fail startup. Existing intent confirmation/finalization/owner sync remains active. |
| `ttl` | duration / `30m` | Export capability lifetime, constrained to `5m..24h`. |
| `challenge_ttl` | duration / `5m` | Export/Claim TON Proof challenge lifetime, constrained to `1m..10m`. |
| `finalizer.batch` | int / `20` | Core finalize/sync-owner jobs claimed per cycle, constrained to `1..100`. |
| `finalizer.poll_interval` | duration / `2s` | Positive Core finalizer polling interval. |
| `finalizer.lease_timeout` | duration / `30s` | Core job lease, constrained to `10s..1h`. |
| `finalizer.request_timeout` | duration / `10s` | Per-job timeout, at least `1s` and shorter than the lease. |
| `finalizer.retry_delay` | duration / `5s` | Positive retry backoff. |
| `claim.enabled` | bool / `false` | Enables the `/ton-gift/claim` Mini App. |
| `claim.bot_token_file` | path / required when claim is enabled | The file must contain exactly one current active local bot token. Core verifies the bot ID/secret and idempotently configures its main/side/attach menu app; mismatch fails startup. |
| `claim.init_data_ttl` | duration / `15m` | Maximum Telegram Mini App `tgWebAppData` age, constrained to `1m..24h`. |

The isolated worker runs as `cmd/telesrv-ton --config configs/ton.yaml`:

| YAML setting | Type / code default | Description and constraints |
|---|---|---|
| `ton.network` | `mainnet` / `testnet`; required | Must match the Core intent network. |
| `ton.lite_servers[]` | address + public_key; at least one | Fixed lite endpoints and public keys; duplicates and empty values fail startup. |
| `ton.trusted_block` | masterchain block ID | Requires `workchain: -1`, non-zero shard/seqno, root hash, and file hash. |
| `ton.collection.address` / `code_hash` | string / required | Startup preflight verifies active collection code; every job rechecks the export collection. |
| `ton.collection.mint_abi` | `basic-collection-v1` / required | Fixed mint payload ABI that must match Core, every export, and durable admission. |
| `ton.collection.initial_item_index` | uint64 decimal string / required | Must equal the on-chain `next_item_index` at first rollout and remains the immutable admission baseline. Allocation occurs only after wallet proof succeeds. |
| `ton.relayer.mnemonic_file` | path / required when mint is enabled | Exactly 24 words for a v4r2 relayer. A read-only worker may omit it. Never commit this file. |
| `ton.relayer.min_balance` | TON decimal / `1` | Mint preflight reserve, greater than zero. |
| `ton.relayer.message_ttl` | duration / `15m` | Signed external-message lifetime, at least twice request timeout and no more than `1h`. |
| `ton.mint.enabled` | bool / `false` | `false` withdraws durable admission for new intents/proofs and stops mint jobs. Already allocated resolve-item, confirmation, and owner reconciliation can continue. This is the incident write-stop. |
| `ton.mint.send_amount` / `forward_amount` | TON decimal / `0.05`, `0.01` | Collection send and item forward values with at most 9 decimals; send amount must be positive. |
| `ton.mint.finality_depth` | int / `12` | Masterchain finality depth, constrained to `1..1000`. |
| `ton.worker.batch` | int / `10` | Jobs claimed per cycle, constrained to `1..100`. |
| `ton.worker.poll_interval` | duration / `2s` | Constrained to `100ms..1m`. |
| `ton.worker.lease_timeout` | duration / `2m` | Constrained to `10s..1h`. |
| `ton.worker.request_timeout` | duration / `90s` | At least `1s` and shorter than the lease. |

Secret-free full examples are in `configs/examples/core.ton-mainnet.yaml` and `configs/examples/ton.mainnet.yaml`.
Before production, complete the pinned checkpoint, code-hash, testnet, write-stop, single-user canary, reconciliation,
and rollback gates described by these settings; do not enable minting from the examples unchanged.

### Composite account rating and collectible usernames

The account rating is gramsrv's own local score combining Stars received and spent, bounded account activity, and
moderation penalties; it does not claim to reproduce Telegram's private algorithm 1:1. The stored level is projected
through `userFull.stars_rating`, while `stars_my_pending_rating` and its activation date are exposed only to the
account itself so official clients can render the result without a patch. Profile reads only fetch a projection
already persisted by the background worker and reuse the existing 30-minute `userFull` projection cache; they never
recompute or write a rating synchronously. Every component remains separately explainable. Collectible (NFT)
usernames are minted by the operator; no external marketplace, wallet or chain node is configured or contacted.

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_RATING_ENABLED` | bool / `true` | Enables the local composite rating and client level projection. Disabled refuses rating writes and leaves client rating flags unset. |
| `TELESRV_RATING_PENDING_DELAY` | duration / `24h` | How long a local rating increase stays pending before it becomes the visible admin level. A decrease is always applied immediately, so a penalty is never delayed. `0` applies every change at once; must be `0..720h`. |
| `TELESRV_RATING_RECOMPUTE_INTERVAL` | duration / `15m` | Background recompute worker interval; must be positive. |
| `TELESRV_RATING_RECOMPUTE_BATCH` | int / `500` | Stale projections recomputed per cycle; must be `1..10000`. |
| `TELESRV_RATING_STALE_AFTER` | duration / `6h` | Projection age after which the worker recomputes a user; must be positive. |
| `TELESRV_RATING_WEIGHT_STARS_RECEIVED_PERMILLE` | int64 / `1000` | Weight of Stars credited to the account (gifts, reactions, paid messages received), in permille of the raw amount. |
| `TELESRV_RATING_WEIGHT_STARS_SPENT_PERMILLE` | int64 / `250` | Weight of Stars the account spent, in permille. Spending is a weaker signal than receiving. |
| `TELESRV_RATING_WEIGHT_MESSAGE_SENT` | int64 / `1` | Score per sent message. |
| `TELESRV_RATING_WEIGHT_ACCOUNT_AGE_DAY` | int64 / `2` | Score per day of account age. |
| `TELESRV_RATING_WEIGHT_GIFT_RECEIVED` | int64 / `25` | Score per collectible gift held. |
| `TELESRV_RATING_WEIGHT_MODERATION_CASE` | int64 / `150` | Penalty magnitude per upheld moderation case; the formula subtracts it. |
| `TELESRV_RATING_WEIGHT_SCAM_PENALTY` | int64 / `5000` | Flat penalty magnitude for the scam flag. |
| `TELESRV_RATING_WEIGHT_FAKE_PENALTY` | int64 / `5000` | Flat penalty magnitude for the fake flag. |
| `TELESRV_RATING_ACTIVITY_CAP` | int64 / `5000` | Upper bound of the activity component so activity alone cannot outweigh Stars and moderation; `0` leaves it uncapped. |
| `TELESRV_COLLECTIBLE_USERNAME_URL_TEMPLATE` | URL template / empty | Landing URL recorded on a minted collectible username when the mint command carries no explicit URL. Empty derives `<TELESRV_PUBLIC_BASE_URL>/nft/username/<username>`. A configured template must be an absolute http(s) URL without userinfo; it may carry the `{username}` placeholder, and without it the name is appended as the last path segment. |

All rating weights are non-negative magnitudes and are validated even when the feature is disabled, so enabling it
later is not the moment a typo is discovered. The defaults above are exactly the shipped domain formula, so behaviour
is identical whether or not these keys are set. The final score is clamped at zero: penalties can erase a rating but
never invert it.

**Collectible username prices are stored in the smallest units of their currency**, because that is what
`fragment.collectibleInfo` carries: `amount` is "the total price in the smallest units of the currency (integer, not
float/double)" and `crypto_amount` likewise. So `USD 1000` is ten dollars, `TON 900` is 900 nanotons, and `XTR 1000` is
a thousand Stars, since Stars have no subunit. Clients divide by that exponent before drawing the price. The admin panel
is the conversion boundary: prices are typed and displayed there in whole currency units and converted on the way to the
API, so an operator never has to count zeros. An integration writing to `/api/actions/mint-collectible-username`
directly is talking to the API, not the panel, and must send smallest units itself.

### Official platform verification

Official verification is the platform badge (`user.verified` / `channel.verified`): an application is filed through the
built-in `@verifybot`, decided in the admin panel, and an approval flips that one flag on that one peer record. It is
deliberately not the third-party `botVerification` icon, where an outside organisation attaches its own mark. The
application row is the durable audit subject and is never deleted, only moved through its status machine.

Every eligibility check runs twice: once when the application is filed and again, against a freshly loaded snapshot, at
the moment of approval. A target must exist, carry a public username, be controlled by the applicant (bot owner, or
channel creator/administrator), not already be verified, not be deleted/frozen/scam/fake, and not be a built-in system
entity. A target that changed between submission and review is refused at the second gate, so the review queue cannot
be used to grant a state the submission path forbids. The flag itself is written inside the decision transaction, so
"approved" and "target verified" commit together.

Submitted links (website, social, press) are validated as plain http(s) URLs to public hosts: credentials, non-web
schemes, non-standard ports, loopback, link-local, private and other reserved address space are all refused. **The
server never fetches a submitted link** — not at submission, not during review, not from the admin panel. That is a
deliberate anti-SSRF decision, and validation is the only thing ever done to an applicant-controlled URL.

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_VERIFICATION_ENABLED` | bool / `true` | Enables official verification. Disabled refuses every verification use case explicitly; peers already carrying the badge keep it, because the flag lives on the peer record. |
| `TELESRV_VERIFICATION_ALLOW_USER_TARGETS` | bool / `false` | Accepts plain user accounts as verification subjects. Off by default: the official process verifies a public presence (bot, public channel, public supergroup), and a private account has nothing to check. |
| `TELESRV_VERIFICATION_REJECT_COOLDOWN` | duration / `720h` | Wait imposed on an applicant/target pair after a rejection, measured from the decision so a slow review never shortens it. `0` disables it; must be `0..8760h`. |
| `TELESRV_VERIFICATION_APPLY_RATE_LIMIT` | int / `3` | Applications one applicant may create per window. `0` disables the budget; must be non-negative. |
| `TELESRV_VERIFICATION_APPLY_RATE_WINDOW` | duration / `24h` | Window for the creation budget. Must be positive whenever the limit is set: a positive limit with a zero window is a limiter that never refills. |
| `TELESRV_VERIFICATION_BOT_RATE_LIMIT` | int / `30` | `@verifybot` dialog rate per applicant, independent of how many applications are actually created. `0` disables it. |
| `TELESRV_VERIFICATION_BOT_RATE_WINDOW` | duration / `1m` | Window for the bot dialog rate; must be positive whenever that limit is set. |
| `TELESRV_VERIFICATION_NOTIFY_INTERVAL` | duration / `15s` | Applicant-notification worker interval; must be positive. A decision commits with its outbox row, never with a message send, so delivery is a separate retrying cycle over durable rows. |
| `TELESRV_VERIFICATION_NOTIFY_BATCH` | int / `50` | Outbox rows delivered per cycle; must be `1..500`. |
| `TELESRV_BROADCAST_WORKER_INTERVAL` | duration / `3s` | System-broadcast worker interval; must be positive. |
| `TELESRV_BROADCAST_WORKER_LEASE` | duration / `30s` | Recipient claim lease used for crash recovery; must be positive and no more than `1h`. |
| `TELESRV_BROADCAST_MATERIALIZE_BATCH` | int / `200` | Maximum all-user recipient IDs materialized by one database keyset step; must be `1..1000`. IDs stay inside PostgreSQL. |
| `TELESRV_BROADCAST_DELIVERY_BATCH` | int / `50` | Maximum leased recipients delivered per cycle; must be `1..500`. Each message commits independently. |
| `TELESRV_VERIFICATION_MAX_ACTIVE_PER_USER` | int / `3` | Applications one applicant may keep open (draft, submitted, in review) at once. `0` disables the cap; must be `0..50`. |

The defaults ship the feature on with the official bar in place, so no existing deployment changes behaviour: user
accounts are not accepted, a rejection costs a month, and an applicant can neither flood the queue nor keep an
unbounded number of applications open. Every value is validated even when the feature is disabled, so enabling it
later is not the moment a typo is discovered.

### Third-party bot verification

Third-party verification is the other badge (`botVerification`, projected onto `user.bot_verification_icon`,
`channel.bot_verification_icon`, `userFull.bot_verification`, `channelFull.bot_verification`,
`chatInvite.bot_verification`, and advertised by `botInfo.verifier_settings`): an outside organisation running a
**verifier bot** marks peers with its own icon and description. The operator grants verifier status to a bot; the bot
then applies its mark through `bots.setCustomVerification`, or through the application queue its own dialog drives. It is
never a second route to the platform checkmark above, and the two mechanisms never read or write each other's state.

The icon is a custom emoji **document id**, and clients resolve it through `messages.getCustomEmojiDocuments`. An id
that names no fetchable custom emoji document therefore renders as *nothing at all*: the badge is invisible, the peer
looks unverified, and the only place the mark exists is the database. That is why the icon catalogue is operator-curated
and validated against real documents before anything is written — both when an entry is added and when a verifier is
granted an icon.

Every mutation re-derives "may this bot verify right now?" from the stored verifier row, so the per-verifier kill switch
takes effect immediately on the RPC path *and* on the review path: an application approved after the switch was flipped
is refused rather than granted. A verifier bot may only be driven by itself or by its owner, and an applicant may only
file for a peer it controls (bot owner, channel creator, or an administrator carrying `change_info`). Approvals and
revocations move the mark inside the store's decision transaction, so an approved application can never exist without
its mark.

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_BOT_VERIFICATION_ENABLED` | bool / `true` | Enables third-party bot verification. Disabled refuses every mutation explicitly (grants, revocations, applications, catalogue edits) while marks already granted keep projecting: blanking one verifier's badges is what its per-verifier kill switch is for. A deployment that wants the pre-feature wire shape leaves the service unwired instead. |
| `TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER` | int / `10000` | Peers one verifier bot may mark. Verifier status is granted per deployment rather than earned per peer, so an unbounded verifier would be an unbounded badge printer. Spent only on a *new* mark — an existing one stays re-describable at the bound. `0` disables the service bound and leaves only the storage bound, which is also the maximum accepted (`10000`). |
| `TELESRV_BOT_VERIFICATION_REQUEST_RATE_LIMIT` | int / `5` | Verification applications one applicant may file per window, across all verifier bots. Spent last among the creation checks, so a refused application costs no budget. `0` disables it; must be non-negative. |
| `TELESRV_BOT_VERIFICATION_REQUEST_RATE_WINDOW` | duration / `24h` | Window for the application budget. Must be positive whenever the limit is set: a positive limit with a zero window is a limiter that never refills. |

The application budget is deliberately looser than the official one (`TELESRV_VERIFICATION_APPLY_RATE_LIMIT=3`): a
deployment can run several verifier companies, and filing with a second one is not a retry of the first. Both keys are
validated even when the feature is disabled, and the per-verifier bound is checked against the storage bound, so a key
that cannot do what it says fails startup instead of being silently unreachable.

## 11. Private calls, group calls, TURN, SFU, and livestream

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_CALL_RING_TIMEOUT` | duration / `90s` | Server fallback timeout for ringing/accepted private calls; should remain aligned with the client `callRingTimeoutMs`. |
| `TELESRV_CALL_TOMBSTONE_TTL` | duration / `60s` | Terminal-call tombstone window for idempotency and late RPC absorption. |
| `TELESRV_CALL_MAX_ACTIVE_PER_USER` | int / `4` | Maximum non-terminal private calls per user. Non-positive values are normalized by the phone service. |
| `TELESRV_CALL_REGISTRY_MAX_ENTRIES` | int / `10000` | Process-wide private-call registry hard limit. At capacity, new calls fail with `CALL_OCCUPY_FAILED`; established calls are never evicted by age. |
| `TELESRV_CALL_SIGNALING_MAX_BYTES` | int bytes / `65536` | Maximum payload for one `phone.sendSignalingData`. |
| `TELESRV_CALL_SIGNALING_RATE` | int / `50` | Signaling forwards per call per second; excess is silently dropped. |
| `TELESRV_CALL_EXPIRY_INTERVAL` | duration / `1s` | Call-expiry dispatcher polling interval. |
| `TELESRV_GROUPCALL_CHECK_TTL` | duration / `45s` | Participant liveness watermark expiry. Clients and the SFU reporter refresh it. |
| `TELESRV_GROUPCALL_SWEEP_INTERVAL` | duration / `10s` | Ghost-participant sweep interval. |
| `TELESRV_GROUPCALL_MAX_PARTICIPANTS` | int / `32` | Per-room participant cap for the current small-scale implementation. |
| `TELESRV_TURN_ENABLE` | bool / `true` | Enables embedded TURN/STUN relay data in private calls. False falls back to LAN/P2P-only behavior. |
| `TELESRV_TURN_UDP_PORT` | int / `12400` | Embedded TURN/STUN UDP listen port; must differ from the SFU port and be allowed through the firewall. |
| `TELESRV_TURN_ADVERTISE_IP` | string / empty | Client-reachable relay address. Empty falls back to SFU advertise IP, then general advertise IP. |
| `TELESRV_TURN_SECRET` | secret string / empty | HMAC secret for TURN REST credentials. Empty creates a process-random secret; multi-instance/external coturn deployments must configure one stable shared secret. |
| `TELESRV_TURN_RELAY_MIN_PORT` | int / `12500` | Inclusive relay allocation port minimum. |
| `TELESRV_TURN_RELAY_MAX_PORT` | int / `12999` | Inclusive relay allocation port maximum; must not be below the minimum. Open the whole range in the firewall. |
| `TELESRV_CALL_TURN_CREDENTIAL_TTL` | duration / `6h` | Per-call TURN credential lifetime. |
| `TELESRV_CALL_FORCE_RELAY` | bool / `false` | Forces `p2p_allowed=false` to test TURN relay paths. |
| `TELESRV_SFU_UDP_PORT` | int / `12399` | Pion ICE UDPMux port; allow it through the firewall. |
| `TELESRV_SFU_ADVERTISE_IP` | string / empty | Client-reachable ICE candidate IP. Empty falls back to `TELESRV_ADVERTISE_IP`; loopback silently breaks real-device media. |
| `TELESRV_SFU_OWNER_TTL` | duration / `2m` | TTL for the `callID -> sfu instance` owner lease; one call must stick to one owner in multi-SFU mode. |
| `TELESRV_SFU_OWNER_HEARTBEAT_INTERVAL` | duration / `30s` | Refresh interval for active standalone SFU owner leases; must be lower than the owner TTL. |
| `TELESRV_SFU_INSTANCE_TTL` | duration / `90s` | TTL for live SFU media instances registered in Redis. Core only selects unexpired instances and probes candidate control through gRPC health plus `GetInfo.instance_id` before assignment. |
| `TELESRV_SFU_INSTANCE_HEARTBEAT_INTERVAL` | duration / `30s` | Heartbeat interval used by `telesrv-sfu` to refresh instance discovery; must be lower than the instance TTL. |
| `TELESRV_SFU_INSTANCE_HEALTH_TIMEOUT` | duration / `1s` | Per-candidate timeout for Core remote owner selection probing SFU gRPC control health. A timeout marks only that candidate unavailable and the selector continues with other non-full instances, so a half-failed SFU inside the Redis TTL window cannot stall join. |
| `TELESRV_SFU_INSTANCE_MAX_ACTIVE_CALLS` | int / `0` | Maximum active calls per SFU instance; `0` means unlimited. Heartbeats publish the current active call count, and the Core remote owner selector skips full or health-check-failing instances and prefers lower-load candidates. |
| `TELESRV_SFU_CONTROL_GRPC_ADDR` | address / empty | Internal listen address for the standalone `cmd/telesrv-sfu` owner-control gRPC API. Required by `cmd/telesrv-sfu`. |
| `TELESRV_SFU_CONTROL_GRPC_URL` | gRPC target / empty | Advertised gRPC control address written to SFU instance/owner records. Empty falls back to `TELESRV_SFU_CONTROL_GRPC_ADDR`. Use a bare `host:port` target or `grpc://host:port`. |
| `TELESRV_SFU_CONTROL_GRPC_REQUEST_TIMEOUT` | duration / `5s` | Default deadline for Core -> SFU gRPC control unary calls. Must be greater than `0` and at most `1m`. |
| `TELESRV_SFU_CONTROL_GRPC_TLS_CERT_FILE` / `TELESRV_SFU_CONTROL_GRPC_TLS_KEY_FILE` | path / empty | SFU gRPC control server TLS certificate and private key. They must be configured together. |
| `TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CA_FILE` | path / empty | Client CA trusted by the SFU gRPC control server. When set, Core/probe clients must present a verified certificate; server cert/key must also be configured. |
| `TELESRV_SFU_CONTROL_GRPC_TLS_CA_FILE` | path / empty | Root CA bundle trusted by Core/probe clients when calling SFU gRPC control. |
| `TELESRV_SFU_CONTROL_GRPC_TLS_SERVER_NAME` | string / empty | Server name used by Core/probe gRPC clients when validating the SFU control server certificate. |
| `TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CERT_FILE` / `TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_KEY_FILE` | path / empty | Client certificate and private key used by Core/probe callers of SFU gRPC control. They must be configured together and enable mTLS with `*_CLIENT_CA_FILE`. |
| `TELESRV_SFU_CONTROL_TOKEN` | secret string / empty | Bearer token for the SFU control API; non-empty values must match on remote clients. Standalone `cmd/telesrv-sfu` requires an explicit non-empty value so the production media control plane cannot start unauthenticated. |
| `TELESRV_LIVESTREAM_ENABLE` | bool / `true` | Enables embedded RTMP ingest plus ffmpeg segmentation for channel livestreams. |
| `TELESRV_LIVESTREAM_RTMP_ADDR` | address / `:2400` | RTMP ingest TCP listen address. |
| `TELESRV_LIVESTREAM_RTMP_URL` | URL string / empty | OBS-facing server URL. Empty derives `rtmp://<AdvertiseIP>:2400/live`. |
| `TELESRV_LIVESTREAM_FFMPEG_PATH` | path/command / `ffmpeg` | ffmpeg executable path; the default resolves through `PATH`. |
| `TELESRV_LIVESTREAM_WORK_DIR` | path / empty | Segment working directory. Empty uses the system temporary directory. |
| `TELESRV_LIVESTREAM_SEGMENT_KEEP` | int seconds / `32` | Per-stream segment duration/window retained in memory; non-positive values are normalized by the livestream service. |

## 12. Container-specific configuration reference

### PostgreSQL Container

The `postgres` service provides the primary durable business database.

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `POSTGRES_PORT` | int / `5432` | Host port mapping for PostgreSQL (Docker: container port always `5432`). |
| `POSTGRES_USER` | string / `telesrv` | PostgreSQL superuser name for database initialization. |
| `POSTGRES_PASSWORD` | secret string / empty | PostgreSQL password; must be set before startup. Production should use a strong random value. |
| `POSTGRES_DB` | string / `telesrv` | Initial database name created by the PostgreSQL container. |
| `TELESRV_POSTGRES_DSN` | secret DSN / `postgres://telesrv:telesrv@postgres:5432/telesrv?sslmode=disable` | Connection string for telesrv services. Must use service name `postgres` (not container name) for Docker Compose internal DNS. Production must enable `sslmode=require` and provide verified certificates. |
| `TELESRV_POSTGRES_MAX_CONNS` | int / `50` | pgxpool maximum connections. Recommended `50-100` for production. |
| `TELESRV_POSTGRES_MIN_CONNS` | int / `16` | pgxpool pre-warmed minimum connections. Recommended `10-20` for production. |

### Redis Container

The `redis` service provides volatile storage for codes, rate limits, and shared session state.

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `REDIS_PORT` | int / `6399` | Host port mapping for Redis (Docker: container port always `6379`). |
| `TELESRV_REDIS_ADDR` | address / `redis:6379` | Redis connection address. Must use service name `redis` (not container name) for Docker Compose internal DNS. |
| `TELESRV_REDIS_PASSWORD` | secret string / empty | Redis password. Leave empty for local development; production should enforce a strong password and `requirepass` configuration. |
| `TELESRV_REDIS_DB` | int / `0` | Redis logical database number (0-15). Multiple deployments on the same Redis instance should use different numbers. |

### telesrv-file Service

Handles file upload/download, blob storage, and media persistence.

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_FILE_GRPC_ADDR` | address / `0.0.0.0:2520` | Internal listen address for FileData gRPC API. |
| `TELESRV_FILE_GRPC_TARGETS` | resolver target(s) / `file:2520` | Endpoint used by Edge/Core to call FileData gRPC. Docker Compose: use `file:2520`. |
| `TELESRV_FILE_TOKEN` | secret string / empty | Bearer token for FileData gRPC. Must match across `telesrv-file`, `telesrv-core`, and `telesrv-edge`. Generate with `openssl rand -hex 24`. |
| `TELESRV_BLOB_BACKEND` | enum / `localfs` | Storage backend: `localfs` (local filesystem) or `s3` (S3-compatible object storage). |
| `TELESRV_BLOB_DIR` | path / `data/blobs` | Local blob storage directory (for `localfs` backend). |
| `TELESRV_BLOB_STAGING_DIR` | path / `data/blob-staging` | Transient upload staging directory. |
| `TELESRV_STORAGE_LOW_SPACE_GUARD_ENABLE` | bool / `true` | Enable storage capacity checks. |
| `TELESRV_STORAGE_MIN_FREE_BYTES` | int64 / `1073741824` | Minimum free disk space before upload rejection (1 GiB default). |
| `TELESRV_STORAGE_MAX_TOTAL_BYTES` | int64 / `0` | Max total blob bytes (S3 mode only); `0` = unlimited. |
| `TELESRV_STORAGE_USAGE_REFRESH_INTERVAL` | duration / `1m` | How often to refresh storage usage snapshot. |
| `TELESRV_UPLOAD_PART_TTL` | duration / `24h` | Retention period for incomplete upload parts. |
| `TELESRV_UPLOAD_PART_GC_INTERVAL` | duration / `30m` | Upload garbage collection interval. |
| `TELESRV_UPLOAD_PART_GC_BATCH` | int / `10000` | Maximum rows cleaned per GC batch. |

S3 configuration (when `TELESRV_BLOB_BACKEND=s3`):

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_S3_ENDPOINT` | host[:port] / empty | S3 endpoint without scheme (e.g., `s3.amazonaws.com` or `minio.example.com:9000`). |
| `TELESRV_S3_REGION` | string / `us-east-1` | AWS region or S3-compatible region code. |
| `TELESRV_S3_BUCKET` | string / empty | S3 bucket name for blob storage. |
| `TELESRV_S3_ACCESS_KEY_ID` | secret string / empty | AWS access key ID or equivalent. Never commit. |
| `TELESRV_S3_SECRET_ACCESS_KEY` | secret string / empty | AWS secret access key or equivalent. Never commit. |
| `TELESRV_S3_USE_SSL` | bool / `true` | Use TLS for S3 connection. Set `false` for local MinIO. |
| `TELESRV_S3_PATH_STYLE` | bool / `false` | Force path-style bucket URLs (required for MinIO, some private S3). |
| `TELESRV_S3_CREATE_BUCKET` | bool / `false` | Auto-create bucket on startup if missing. Requires write permissions. |

### telesrv-core Service

The main RPC dispatcher and business logic handler. Requires PostgreSQL and Redis connectivity.

Core-specific settings are detailed in sections 2-11 above. Critical ones:

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_CORE_EXEC_GRPC_ADDR` | address / `0.0.0.0:2440` | Internal listen address for CoreExec gRPC. Required. |
| `TELESRV_CORE_EXEC_TOKEN` | secret string / empty | Bearer token for CoreExec gRPC. Generate with `openssl rand -hex 24`. Must match `telesrv-edge`. |
| `TELESRV_GROUPCALL_CONTROL_ADDR` | address / `0.0.0.0:2420` | Internal listen address for group call control API. Required for SFU. |
| `TELESRV_GROUPCALL_CONTROL_URL` | URL / `http://telesrv-core:2420` | SFU-accessible URL for group call control. Docker Compose: use service name. |
| `TELESRV_GROUPCALL_CONTROL_TOKEN` | secret string / empty | Bearer token for group call API. Must match `telesrv-sfu`. |
| `TELESRV_SFU_CONTROL_GRPC_TARGETS` | resolver target(s) / empty | SFU gRPC control endpoint(s). Docker Compose: `sfu:2450`. |
| `TELESRV_SFU_CONTROL_TOKEN` | secret string / empty | Bearer token for SFU control. Must match `telesrv-sfu`. |

### telesrv-edge Service

Client-facing MTProto listener. Routes to Core via gRPC, manages connection state, and coordinates with Egress.

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_EDGE_PORT` | int / `2398` | Host port mapping for Edge (Docker: container `2398`). Clients connect here. |
| `TELESRV_LISTEN_IP` | string / `0.0.0.0` | Edge bind address. Use `0.0.0.0` for production unless behind a reverse proxy. |
| `TELESRV_ADVERTISE_IP` | string / `127.0.0.1` | Public IP advertised to clients. Production: set to LAN/public IP. |
| `TELESRV_ADVERTISE_PORT` | int / `2398` | Public port advertised to clients. Must match client-reachable address. |
| `TELESRV_CORE_EXEC_GRPC_TARGETS` | resolver target(s) / empty | Core gRPC endpoint(s). Docker Compose: `core:2440`. |
| `TELESRV_CORE_EXEC_TOKEN` | secret string / empty | Bearer token for Core RPC. Must match `telesrv-core`. |
| `TELESRV_FILE_GRPC_TARGETS` | resolver target(s) / empty | File gRPC endpoint(s). Docker Compose: `file:2520`. |
| `TELESRV_FILE_TOKEN` | secret string / empty | Bearer token for File API. Must match `telesrv-file` and `telesrv-core`. |
| `TELESRV_EGRESS_ACK_GRPC_TARGETS` | resolver target(s) / empty | Egress ACK gRPC endpoint(s). Docker Compose: `egress:2510`. |
| `TELESRV_EGRESS_ACK_TOKEN` | secret string / empty | Bearer token for Egress ACK. Must match `telesrv-egress`. |
| `TELESRV_WEBSOCKET_ENABLE` | bool / `true` | Enable MTProto-over-WebSocket. Recommended `true`. |
| `TELESRV_WEBSOCKET_ALLOWED_ORIGINS` | list / `http://localhost:1234,http://127.0.0.1:1234` | Comma-separated browser origins. Production: list actual origins; use `*` only for debugging. |

### telesrv-egress Service

Handles outbox delivery, message push, and PTS state management. Requires PostgreSQL and Redis.

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_EGRESS_ACK_GRPC_ADDR` | address / `0.0.0.0:2510` | Internal listen address for Egress ACK API. Required. |
| `TELESRV_EGRESS_ACK_TOKEN` | secret string / empty | Bearer token for Egress ACK. Must match `telesrv-edge`. |
| `TELESRV_OUTBOX_WORKERS` | int / `4` | Concurrent outbox delivery workers. Increase for higher throughput; 4-8 is typical. |
| `TELESRV_OUTBOX_BATCH` | int / `100` | Max rows claimed per batch. Larger = higher throughput; tune based on DB performance. |
| `TELESRV_OUTBOX_LEASE_TIMEOUT` | duration / `30s` | Time before a dispatching row can be reclaimed. Must exceed worst-case delivery time. |
| `TELESRV_OUTBOX_POISON_RETENTION` | duration / `1m` | Retention for terminal failed heads (diagnostic). |
| `TELESRV_OUTBOUND_PUSH_TIMEOUT` | duration / `200ms` | Timeout for Edge confirmation of update enqueue. |

### telesrv-sfu Service

Handles group call media (audio/video) via WebRTC. Uses Redis for instance discovery.

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_SFU_UDP_PORT` | int / `12399` | Pion ICE UDPMux port for media. Open in firewall. |
| `TELESRV_SFU_ADVERTISE_IP` | string / empty | Client-reachable SFU IP. Empty = use `TELESRV_ADVERTISE_IP`. Production: must be reachable by remote clients. |
| `TELESRV_SFU_CONTROL_GRPC_ADDR` | address / `0.0.0.0:2450` | Internal listen address for SFU control. Required. |
| `TELESRV_SFU_CONTROL_TOKEN` | secret string / empty | Bearer token for SFU control. Generate with `openssl rand -hex 24`. Must match `telesrv-core`. |
| `TELESRV_GROUPCALL_CONTROL_URL` | URL / `http://telesrv-core:2420` | Core group call control endpoint. |
| `TELESRV_GROUPCALL_CONTROL_TOKEN` | secret string / empty | Bearer token for group call API. Must match `telesrv-core`. |
| `TELESRV_SFU_OWNER_TTL` | duration / `2m` | Owner lease TTL; one call sticks to one SFU instance. |
| `TELESRV_SFU_OWNER_HEARTBEAT_INTERVAL` | duration / `30s` | Owner lease refresh interval. Must be `< TELESRV_SFU_OWNER_TTL`. |
| `TELESRV_SFU_INSTANCE_TTL` | duration / `90s` | SFU media instance discovery TTL in Redis. |
| `TELESRV_SFU_INSTANCE_HEARTBEAT_INTERVAL` | duration / `30s` | Instance discovery heartbeat. Must be `< TELESRV_SFU_INSTANCE_TTL`. |

### telesrv-admin Service

Web-based administrative UI for managing users, channels, moderation, and configuration.

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_ADMIN_UI_PORT` | int / `2600` | Host port mapping (Docker: container `2600`). Local development only; production uses reverse proxy on loopback. |
| `TELESRV_LOCAL_LISTEN_IP` | string / `127.0.0.1` | Admin UI bind address. Keep as `127.0.0.1` for loopback-only. |
| `TELESRV_ADMIN_API_ADDR` | address / `127.0.0.1:2601` | Internal Admin API listener on main `telesrv-core`. Empty = disabled. |
| `TELESRV_ADMIN_API_TOKEN` | secret string / empty | Admin API authentication token. Must match `TELESRV_ADMIN_UI_TOKEN` (same value). |
| `TELESRV_ADMIN_UI_TOKEN` | secret string / empty | Admin UI login credential (alternative to password). At least one of `TELESRV_ADMIN_UI_PASSWORD` or `TELESRV_ADMIN_UI_TOKEN` must be set. |
| `TELESRV_ADMIN_UI_PASSWORD` | secret string / empty | Admin UI login password. At least one of password or token must be set. |
| `TELESRV_ADMIN_SESSION_KEY` | secret string / empty | Session cookie encryption key. Production: 32+ random bytes. Change invalidates all sessions. |
| `TELESRV_ADMIN_UI_PERMISSIONS` | comma-separated list / `*` | Permissions for password/token login. `*` = all permissions. Examples: `users.list`, `channels.manage`, `moderation.*`. |
| `TELESRV_ADMIN_SCOPED_TOKENS` | semicolon-separated / empty | Additional API tokens with bounded permissions. Format: `name:token:perm1,perm2;name2:token2:perm3`. |

### telesrv-ton Service

Optional isolated worker for TON blockchain interactions (Star Gift export/minting). Only deployed if TON features are enabled.

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_TON_NETWORK` | enum / `testnet` | TON network: `mainnet` or `testnet`. Core and TON worker must use the same network. |
| `TELESRV_TON_LITESERVER_ADDR` | address / `127.0.0.1:19949` | TON liteserver endpoint (IP:port format). |
| `TELESRV_TON_LITESERVER_PUBLIC_KEY` | hex string / empty | Base64-encoded ED25519 public key of the liteserver for verification. |
| `TELESRV_TON_TRUSTED_BLOCK_SEQNO` | int / empty | Masterchain block sequence number for initial trust anchor. |
| `TELESRV_TON_TRUSTED_BLOCK_ROOT_HASH` | hex string / empty | Root hash of the trusted masterchain block. |
| `TELESRV_TON_TRUSTED_BLOCK_FILE_HASH` | hex string / empty | File hash of the trusted masterchain block. |
| `TELESRV_TON_COLLECTION_ADDRESS` | string / empty | TEP-62 NFT collection address (must match Core config). |
| `TELESRV_TON_COLLECTION_CODE_HASH` | hex string / empty | Collection code hash for verification. |
| `TELESRV_TON_INITIAL_ITEM_INDEX` | int / `0` | First telesrv-managed NFT index. Never decrease or reuse after rollout. |
| `TELESRV_TON_MNEMONIC_FILE` | path / empty | Relayer wallet mnemonic (24 words). Required for minting. Never commit. |
| `TELESRV_TON_MINT_ENABLE` | bool / `false` | Enable minting. Keep `false` until full testing/canary complete. |

### telesrv-update Service

Standalone HTTP server for native client auto-updates (optional, separate process).

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_UPDATE_PORT` | int / `2402` | Host port mapping (Docker: container `2402`). |
| `TELESRV_UPDATE_PUBLIC_URL` | HTTP(S) URL / empty | Public update endpoint advertised in `help.getConfig.autoupdate_url_prefix`. Empty = updates disabled. |
| `TELESRV_UPDATE_SERVICE_URL` | HTTP(S) URL / `TELESRV_UPDATE_PUBLIC_URL` | Internal update resolver (may differ from public; useful for loopback in dev). Empty = use public URL. |
| `TELESRV_UPDATE_REQUEST_TIMEOUT` | duration / `2s` | Update check timeout. Must be `0s < timeout <= 30s`. |

## 13. Production minimum checklist

At minimum, production operators should explicitly review and override the development credentials/endpoints: PostgreSQL DSN and TLS, Redis password/network exposure, RSA key persistence, fixed development auth code exposure, Admin credentials/session key, OTP Webhook/SMTP secrets, AI/Mapbox API keys, CoreExec/FileData/Egress ACK/SFU gRPC control TLS/mTLS, TURN secret and firewall ports, public URLs/scheme alignment, and non-loopback SFU/TURN advertise addresses for real devices. TON Star Gift rollout additionally requires pinned lite servers/checkpoint/collection code hash, separate capability/bot/relayer secrets, a one-user allowlist, a `mint.enabled: false` preflight, and complete testnet/canary/rollback gates.
