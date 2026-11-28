# 推送通知（FCM / Web push / Bark / ntfy）

状态：**FCM、Web push、Bark、ntfy 已实现并在生产环境验证**；APNs 已设计但暂缓
（Bark/ntfy 可在无需运营者 Apple 证书的情况下覆盖 iOS）。跟踪 issue：
[#98](https://github.com/iamxvbaba/gramsrv/issues/98)。本文档是 issue 作者要求的
设计方案，并与实现保持同步。

实现位置：

- token 存储：`internal/domain/push_token.go`、`internal/store/push_token.go`、
  `internal/store/memory/push_token.go`、`internal/store/postgres/push_token.go`，
  迁移 `deploy/migrations/*_device_push_tokens.up.sql`。
- RPC：`internal/rpc/account.go`（`onAccountRegisterDevice` /
  `onAccountUnregisterDevice`）、`internal/rpc/deps.go`（`Deps.PushTokens`）。
- 加密：`internal/push/cipher.go`（MTProto v2 推送加密）。
- FCM：`internal/push/fcm.go`（HTTP v1 + service account OAuth2）。
- Web push：`internal/push/webpush.go`（RFC 8291/8188 aes128gcm + RFC 8292 VAPID）。
- Bark：`internal/push/bark.go`；ntfy：`internal/push/ntfy.go`。
- payload/service：`internal/push/service.go`（按 token 类型分发）。
- 离线触发与静音/角标：`internal/rpc/offline_push.go`、
  `internal/rpc/offline_push_payload.go`，以及
  `internal/rpc/outbox_dispatcher.go` 中的 `WithOfflinePusher` 钩子。
- 接线/配置：`cmd/telesrv/main.go`、`internal/config/config.go`。

## 1. 概述

在开启推送之前，`account.registerDevice` 只是被接受并丢弃，后台客户端在重新连接
之前收不到任何通知。本功能为后台客户端提供**可选、默认关闭**的服务端推送：

- `account.registerDevice` / `unregisterDevice` 在接入 token 存储后会真正持久化
  设备 token 与客户端 `secret`。token 以 `(token_type, token)` 全局唯一，会重新
  归属到最新账号；空 token 返回 `TOKEN_EMPTY`。
- 持久化 outbox 派发器本就知道一条更新触达了几个在线会话；当数量为 **0** 时，
  `OfflinePushBridge` 会基于“观看者视角”的更新构建 `loc_key` 通知，按用户合并后
  由小型 worker 池发送，绝不阻塞 outbox。在线投递完全不变。
- 静音：会读取对端的 `mute_until`（peer 作用域加 users/chats/broadcasts 分类默认
  值）。静音会话改为发送**限频的 `MESSAGE_MUTED` 角标刷新**而非可见通知；token 为
  `no_muted` 的设备在该刷新中被跳过。
- 失效：provider 返回 `UNREGISTERED`/`404`/`410` 时会删除该 token。

## 2. 加密

**FCM（token_type 2）**：用客户端 `secret` 按官方客户端使用的 MTProto v2 方案封装
通知 JSON，作为 data-only FCM 消息的 `p` 字段发送：

```
blob       = SHA1(secret)[-8:] || msg_key(16) || AES-IGE(padded_plaintext)
msg_key    = SHA256(secret[96:128] || padded_plaintext)[8:24]
aes key/iv = MessageKeyData(secret, msg_key, incoming=true)  // x=8
padded     = int32LE(len(json)) || json || 随机填充到 16 字节
p          = base64url(blob)
```

该方案对齐了参考实现（DrKLO Android `PushListenerController` 与
`MessageKeyData`、Telegram Web K 的 `pushSingleManager`），并在测试中用独立的客户端
解密器做往返校验。

**Web push（token_type 10）**：订阅 token 是 JSON（`endpoint` + `p256dh`/`auth`）。
通知 JSON 以 **RFC 8291**（`aes128gcm`，**RFC 8188**）加密，并用 **RFC 8292** VAPID
的 ES256 放入 `Authorization` 头。已通过 RFC 8291 §5 测试向量以及一个会校验 VAPID
签名并解密 body 的端到端测试。

## 3. Bark 与 ntfy（token_type 14/15）

无需运营者 Apple 证书时，iOS 可由两个自建 HTTP 推送服务中继，二者都由各自的 App
弹出通知（其 APNs 权限已内置）：

- **Bark**（`internal/push/bark.go`，`TELESRV_PUSH_BARK_SERVER`）：`POST
  {server}/push`，字段 `{device_key,title,body,level,sound,group,url}`。token 为
  Bark 设备 key，或 JSON `{"endpoint","key","sound","group","url"}`。
- **ntfy**（`internal/push/ntfy.go`，`TELESRV_PUSH_NTFY_SERVER`）：`POST
  {server}/{topic}`，字段 `{title,message,priority,click,tags}`，可选
  `Authorization: Bearer`。token 为 topic，或 JSON
  `{"endpoint","topic","token","click"}`。

两者复用与 web push 相同的 `Title`/`Description`；`MESSAGE_MUTED` 刷新以
被动/低优先级发送，不会震动。两种 token 类型都是 telesrv 扩展：打过补丁的客户端
用对应类型注册 Bark key / ntfy topic。服务器地址为空即禁用该 provider。

## 4. 配置（全部可选）

| 设置 | 默认 | 含义 |
|---|---|---|
| `TELESRV_PUSH_ENABLED` | `false` | 总开关。关闭时 `registerDevice` 仍为空操作、不存储 token。 |
| `TELESRV_PUSH_ALWAYS` | `false` | 即使对方有在线会话也推送（在线客户端会同时收到应用内更新与推送）。 |
| `TELESRV_PUSH_FCM_SERVICE_ACCOUNT_FILE` | `` | Firebase service account JSON 路径（文件形式密钥）。 |
| `TELESRV_PUSH_FCM_PROJECT_ID` / `_TOKEN_URL` / `_SCOPE` | — | 覆盖项。 |
| `TELESRV_PUSH_WEBPUSH_VAPID_PUBLIC_KEY` / `_PRIVATE_KEY` | `` | Web push VAPID 密钥对（base64url）。 |
| `TELESRV_PUSH_WEBPUSH_SUBJECT` / `_TTL` | — / `86400` | VAPID `sub`、TTL。 |
| `TELESRV_PUSH_BARK_SERVER` | `` | Bark 服务器地址；为空禁用 Bark。 |
| `TELESRV_PUSH_BARK_SOUND` / `_GROUP` | `` | Bark 默认声音/分组。 |
| `TELESRV_PUSH_NTFY_SERVER` | `` | ntfy 地址；为空禁用 ntfy。 |
| `TELESRV_PUSH_NTFY_TOKEN` | `` | 受保护的 ntfy 可选 Bearer token。 |
| `TELESRV_PUSH_DEBOUNCE` / `_SEND_TIMEOUT` | `30s` / `5s` | 合并窗口 / HTTP 超时。 |

不引入新依赖：service account JWT 与 VAPID 仅用标准库（`crypto/ecdh`、
`crypto/ecdsa`、`crypto/aes`、基于 `crypto/hmac` 的 HKDF）。

## 5. 客户端要求（无法回避）

- **Android** 必须针对运营者自己的 Firebase 项目重新构建（替换
  `google-services.json`），FCM 才会投递到服务器的 service account。
- **Web** 必须用其私钥在服务端保存的 VAPID 公钥构建（`VITE_PUSH_SERVER_KEY`）；
  推送服务会拒绝密钥不匹配的请求。
- **iOS**：可用 Bark/ntfy App 接收（见 §3），无需 Apple 证书；原生 APNs 作为后续
  工作（`p8` 密钥、`api.push.apple.com` HTTP/2、`mutable-content`）。

## 6. 测试

- `internal/push`：FCM 针对 `httptest` 的测试（token 缓存、失效）、MTProto 加密
  往返、RFC 8291 测试向量、VAPID+解密的端到端测试，以及 Bark/ntfy 的发送与分发测试。
- `internal/rpc`：注册/存储、outbox「仅在 `sent == 0` 时推送」、静音→`MESSAGE_MUTED`、
  `no_muted` 跳过。
- `internal/store`：memory + postgres（`*_integration_test.go`，需 DSN）。

## 7. 暂未包含

- 原生 APNs（需要 Apple 密钥与 iOS 重新构建）。
- 除对端默认值之外的论坛话题（topic）作用域推送。
