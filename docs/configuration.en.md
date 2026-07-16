# telesrv configuration reference

Chinese version: [configuration.zh-CN.md](configuration.zh-CN.md)

This document describes every setting loaded by `internal/config`. Defaults and validation behavior in `internal/config/config.go` are authoritative. All settings require a process restart; telesrv does not hot-reload configuration.

## 1. Loading, syntax, and precedence

- `TELESRV_CONFIG` is a **process environment variable** selecting the env-style configuration file. Default: `.env` in the process working directory. An explicit empty value disables file loading. Setting it inside the file has no effect because the file has already been selected.
- Precedence is: non-empty process environment value → non-empty file value → code default. The nullable listener settings (`TELESRV_DEBUG_ADDR`, `TELESRV_BOT_API_ADDR`, `TELESRV_ADMIN_API_ADDR`, and `TELESRV_PUBLIC_LINK_WEB_ADDR`) additionally allow an explicitly empty process value to disable a non-empty file value.
- The file accepts blank lines, full-line `#` comments, optional `export `, and `KEY=VALUE`. Single- and double-quoted values are supported. Inline comments are not stripped.
- File keys must start with `TELESRV_` and contain only uppercase ASCII letters, digits, and underscores. Unknown `TELESRV_*` keys are syntactically accepted but ignored by the current binary.
- Booleans accept `1/true/TRUE/True/yes/on` and `0/false/FALSE/False/no/off`. Lists are comma-separated. Durations use Go duration syntax such as `200ms`, `30s`, `5m`, or `168h`.
- Invalid integer, float, boolean, or duration text falls back to the code default. URL, app-scheme, app-name, and login-email dependency validation fails startup instead.
- Never commit real passwords, tokens, private DSNs, or TURN secrets. Prefer a secret manager or protected service environment in production.

## 2. MTProto listener, transport, and resource budgets

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_LISTEN` | string / `0.0.0.0:2398` | MTProto TCP listen address. Must match the address/port reachable by patched clients. |
| `TELESRV_ADVERTISE_IP` | string / `127.0.0.1` | Client-reachable server IP used by media/call fallbacks. The current static Desktop DC patch does not derive its MTProto endpoint from this value. |
| `TELESRV_RSA_KEY` | path / `data/server_rsa.pem` | MTProto RSA private key. Generated when missing. Treat the file as a secret and keep it stable across restarts. |
| `TELESRV_DC` | int / `2` | Server DC ID. Must match patched client expectations and stored media/DC metadata. |
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
| `TELESRV_MTPROTO_RPC_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | Process-wide queued/in-flight RPC request-body budget. |
| `TELESRV_MTPROTO_RPC_RESULT_CACHE_MAX_ENTRIES` | int / `262144` | Global ownership entries for pending owners, completed results, and tombstones during the in-process 331-second replay window. |
| `TELESRV_MTPROTO_RPC_RESULT_CACHE_MAX_BYTES` | int64 bytes / `67108864` | Global retained-byte budget. Owner admission reserves one byte; Put transfers it to a body or tombstone. Must be at least `16775168`. |
| `TELESRV_MTPROTO_RPC_RESULT_CACHE_AUTH_MAX_ENTRIES` | int / `32768` | Per raw-auth-key ownership entries; charged together with global and session scopes. |
| `TELESRV_MTPROTO_RPC_RESULT_CACHE_AUTH_MAX_BYTES` | int64 bytes / `33554432` | Per raw-auth-key retained bytes. Limits must satisfy `global >= auth >= session`. |
| `TELESRV_MTPROTO_RPC_RESULT_CACHE_SESSION_MAX_ENTRIES` | int / `16384` | Per `raw auth key + session_id` ownership entries. |
| `TELESRV_MTPROTO_RPC_RESULT_CACHE_SESSION_MAX_BYTES` | int64 bytes / `16777216` | Per `raw auth key + session_id` retained bytes; large enough for one legal outbound body. |
| `TELESRV_MTPROTO_RPC_RESULT_PENDING_PER_AUTH` | int / `2048` | Additional active-owner cap per raw auth key; no greater than global pending tasks or auth entries. |
| `TELESRV_MTPROTO_INBOUND_FRAME_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | Process-wide reservation for transport wire bytes plus maximum decrypted plaintext, acquired before payload allocation. |
| `TELESRV_MTPROTO_OUTBOUND_QUEUE_SIZE` | int / `128` | Per-connection normal outbound mailbox capacity. |
| `TELESRV_MTPROTO_OUTBOUND_CONTROL_QUEUE_SIZE` | int / `32` | Per-connection control-message mailbox capacity. |
| `TELESRV_MTPROTO_OUTBOUND_TRACKED_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | Global budget for tracked resend-pending message bodies. |
| `TELESRV_MTPROTO_OUTBOUND_WRITE_GLOBAL_MAX_BYTES` | int64 bytes / `536870912` | Global budget for concurrent encrypted wire/codec/obfuscation scratch. |

## 3. HTTP endpoints, public links, and administration

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_DEBUG_ADDR` | nullable address / `127.0.0.1:6060` | pprof/debug listener. Empty disables it. Keep loopback-only; use an SSH tunnel for production profiling. |
| `TELESRV_BOT_API_ADDR` | nullable address / empty | Minimal HTTP Bot API listener. Empty disables it. It shares MTProto app/store facts. |
| `TELESRV_ADMIN_API_ADDR` | nullable address / empty | In-process Admin write API listener. Empty disables it; production should bind loopback. |
| `TELESRV_ADMIN_API_TOKEN` | secret string / empty | Admin API bearer token. Required when the Admin API is enabled and must match the Admin UI token configuration. |
| `TELESRV_ADMIN_UI_ADDR` | address / `127.0.0.1:2600` | Standalone `cmd/telesrv-admin` listen address. |
| `TELESRV_ADMIN_UI_PASSWORD` | secret string / empty | Admin UI login password. Configure this or `TELESRV_ADMIN_UI_TOKEN`. |
| `TELESRV_ADMIN_UI_TOKEN` | secret string / empty | Alternative Admin UI login credential. Admin write calls still use the separate `TELESRV_ADMIN_API_TOKEN`. |
| `TELESRV_ADMIN_SESSION_KEY` | secret string / empty | Encrypts/signs Admin UI session cookies. Production should use at least 32 random bytes; changing it invalidates sessions. |
| `TELESRV_PUBLIC_BASE_URL` | HTTP(S) URL / `https://telesrv.net` | Client-visible canonical public-link root. Paths are allowed; credentials, query, and fragment are rejected. Local example: `http://127.0.0.1:2401`. |
| `TELESRV_PUBLIC_APP_SCHEME` | URL scheme / `telesrv` | Automatic app-open scheme on landing pages. Must match patched client registration. `tg`, `http`, and `https` are rejected. |
| `TELESRV_PUBLIC_WEB_BASE_URL` | HTTP(S) URL / `https://web.telesrv.net` | Web-client root used by public username pages. Same URL validation as `TELESRV_PUBLIC_BASE_URL`. |
| `TELESRV_PUBLIC_APP_NAME` | string / `telesrv` | Public landing-page product name; trimmed, non-empty, no control characters, maximum 64 Unicode characters. |
| `TELESRV_PUBLIC_LINK_WEB_ADDR` | nullable address / empty | Read-only username/avatar/sticker/emoji/chatlist landing-page listener. Empty disables it. Production should bind loopback behind exact nginx routes. `.env.example` enables `127.0.0.1:2401` for development. |

## 4. PostgreSQL, Redis, files, and seed data

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_POSTGRES_DSN` | secret DSN / `postgres://telesrv:telesrv@127.0.0.1:5432/telesrv?sslmode=disable` | Primary durable business database. Production must replace the development credentials and TLS policy. |
| `TELESRV_POSTGRES_MAX_CONNS` | int / `50` | pgxpool maximum connections. `<=0` delegates to pgx defaults, which are usually too small for production outbox/RPC concurrency. |
| `TELESRV_POSTGRES_MIN_CONNS` | int / `16` | pgxpool pre-warmed minimum connections. |
| `TELESRV_REDIS_ADDR` | address / `127.0.0.1:6399` | Redis used for volatile codes, limits, and shared update/cache state. |
| `TELESRV_REDIS_PASSWORD` | secret string / empty | Redis password. |
| `TELESRV_REDIS_DB` | int / `0` | Redis logical database number. |
| `TELESRV_LANGPACK_SEED_DIR` | path / `data/langpack` | TDesktop `.strings` language-pack seed directory. |
| `TELESRV_BLOB_DIR` | path / `data/blobs` | Local development blob-backend root for media bytes. |
| `TELESRV_STICKER_SEED_DIR` | path / `data/sticker-seed` | Sticker/reaction seed packages imported into documents, sticker sets, and blobs. |
| `TELESRV_STICKER_SEED_MAX_SETS` | int / `300` | Maximum regular sticker sets imported at startup; `<=0` means unlimited. |

## 5. Authentication, login email, SMTP, and passkeys

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_DEV_AUTH_CODE` | sensitive string / `12345` | Fixed development login code. Production SMS/risk delivery is not implemented; do not expose this default publicly. |
| `TELESRV_AUTH_CODE_TTL` | duration / `5m` | Login/registration/email verification code lifetime; must be positive. |
| `TELESRV_AUTH_CODE_MAX_ATTEMPTS` | int / `5` | Maximum wrong attempts for one code/hash; must be positive. |
| `TELESRV_AUTH_CODE_PHONE_RATE_LIMIT` | int / `5` | Code issuance limit per normalized phone digest per rate window; `<=0` disables this dimension. |
| `TELESRV_AUTH_CODE_AUTH_KEY_RATE_LIMIT` | int / `20` | Code issuance limit per raw auth key per rate window; `<=0` disables this dimension. |
| `TELESRV_AUTH_CODE_RATE_WINDOW` | duration / `10m` | Shared window for phone and auth-key issuance limits. |
| `TELESRV_LOGIN_EMAIL_ENABLE` | bool / `false` | Enables login-email verification delivery. When true, SMTP settings below become mandatory. |
| `TELESRV_LOGIN_EMAIL_REQUIRE_SETUP` | bool / `false` | Forces accounts without a login email to configure one. Requires `TELESRV_LOGIN_EMAIL_ENABLE=true`. |
| `TELESRV_LOGIN_EMAIL_CODE_LENGTH` | int / `6` | Email verification-code length; allowed range `4..10`. |
| `TELESRV_SMTP_HOST` | string / empty | SMTP server host; required when login email is enabled. |
| `TELESRV_SMTP_PORT` | int / `587` | SMTP port; must be `1..65535` when login email is enabled. |
| `TELESRV_SMTP_USERNAME` | sensitive string / empty | SMTP username. Also used as sender when `TELESRV_SMTP_FROM` is empty. |
| `TELESRV_SMTP_PASSWORD` | secret string / empty | SMTP password. |
| `TELESRV_SMTP_FROM` | email/string / empty | Envelope/header sender. Either this or SMTP username is required when login email is enabled. |
| `TELESRV_SMTP_FROM_NAME` | string / `telesrv` | Display name for login-email messages. |
| `TELESRV_SMTP_TLS` | enum / `starttls` | `starttls`, `tls`, or `none`; any other value fails startup. |
| `TELESRV_SMTP_TIMEOUT` | duration / `10s` | SMTP operation timeout; must be positive when login email is enabled. |
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
| `TELESRV_CHANNEL_ROW_CACHE_MAX` | int / `50000` | Shared channel-row cache capacity. `<=0` disables both cache and its LISTEN/NOTIFY listener. |
| `TELESRV_CHANNEL_MEMBER_CACHE_MAX` | int / `100000` | Channel member/access read-model cache capacity; `<=0` disables it. |
| `TELESRV_CHANNEL_DIALOG_CACHE_MAX` | int / `100000` | Viewer/channel dialog projection cache capacity; `<=0` disables it. |
| `TELESRV_CHANNEL_BOOST_CACHE_MAX` | int / `100000` | Channel boost read-model cache capacity; `<=0` disables it. |
| `TELESRV_CHANNEL_BOOST_CACHE_TTL` | duration / `10s` | Maximum stale window if a boost invalidation notification is missed. |

## 9. Outbox, push, limits, retention, and GC

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_OUTBOX_WORKERS` | int / `4` | Concurrent outbox workers. Stable logical sharding preserves per-user pts order. |
| `TELESRV_OUTBOX_BATCH` | int / `100` | Maximum rows claimed per poll. Larger batches improve throughput but increase DB/push bursts. |
| `TELESRV_OUTBOX_INTERVAL` | duration / `200ms` | Delay between outbox claims. |
| `TELESRV_OUTBOX_LEASE_TIMEOUT` | duration / `30s` | Time before a `dispatching` row can be reclaimed. Must exceed worst-case batch delivery time. |
| `TELESRV_OUTBOX_POISON_RETENTION` | duration / `1m` | Diagnostic retention for terminal failed delivery heads; durable update events remain recoverable through difference. |
| `TELESRV_OUTBOX_POISON_CLEANUP_INTERVAL` | duration / `15s` | Cleanup interval for terminal failed heads, independent of large-table retention. |
| `TELESRV_OUTBOUND_PUSH_TIMEOUT` | duration / `200ms` | Maximum wait for best-effort online update enqueue. |
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

## 10. Premium and Stars development grants

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_PREMIUM_GRANT_MONTHS` | int / `3` | Premium months granted to newly registered users; `0` disables new grants. Existing migration backfills are unaffected. |
| `TELESRV_STARS_STARTING_GRANT` | int64 / `1000` | Idempotent lazy starting Stars balance for all accounts; `0` disables automatic grant. |
| `TELESRV_PREMIUM_SWEEP_INTERVAL` | duration / `1m` | Expired-premium cleanup/push interval. Read paths derive expiry independently. |
| `TELESRV_PREMIUM_SWEEP_BATCH` | int / `500` | Maximum expired premium rows processed per sweep. |

## 11. Private calls, group calls, TURN, SFU, and livestream

| Setting | Type / code default | Description and constraints |
|---|---|---|
| `TELESRV_CALL_RING_TIMEOUT` | duration / `90s` | Server fallback timeout for ringing/accepted private calls; should remain aligned with the client `callRingTimeoutMs`. |
| `TELESRV_CALL_TOMBSTONE_TTL` | duration / `60s` | Terminal-call tombstone window for idempotency and late RPC absorption. |
| `TELESRV_CALL_MAX_ACTIVE_PER_USER` | int / `4` | Maximum non-terminal private calls per user. Non-positive values are normalized by the phone service. |
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
| `TELESRV_SFU_ENABLE` | bool / `true` | Enables embedded group-call media forwarding. False leaves signaling-only M0 behavior. |
| `TELESRV_SFU_UDP_PORT` | int / `12399` | Pion ICE UDPMux port; allow it through the firewall. |
| `TELESRV_SFU_ADVERTISE_IP` | string / empty | Client-reachable ICE candidate IP. Empty falls back to `TELESRV_ADVERTISE_IP`; loopback silently breaks real-device media. |
| `TELESRV_LIVESTREAM_ENABLE` | bool / `true` | Enables embedded RTMP ingest plus ffmpeg segmentation for channel livestreams. |
| `TELESRV_LIVESTREAM_RTMP_ADDR` | address / `:2400` | RTMP ingest TCP listen address. |
| `TELESRV_LIVESTREAM_RTMP_URL` | URL string / empty | OBS-facing server URL. Empty derives `rtmp://<AdvertiseIP>:2400/live`. |
| `TELESRV_LIVESTREAM_FFMPEG_PATH` | path/command / `ffmpeg` | ffmpeg executable path; the default resolves through `PATH`. |
| `TELESRV_LIVESTREAM_WORK_DIR` | path / empty | Segment working directory. Empty uses the system temporary directory. |
| `TELESRV_LIVESTREAM_SEGMENT_KEEP` | int seconds / `32` | Per-stream segment duration/window retained in memory; non-positive values are normalized by the livestream service. |

## 12. Production minimum checklist

At minimum, production operators should explicitly review and override the development credentials/endpoints: PostgreSQL DSN and TLS, Redis password/network exposure, RSA key persistence, fixed development auth code exposure, Admin credentials/session key, SMTP secrets when enabled, AI/Mapbox API keys, TURN secret and firewall ports, public URLs/scheme alignment, and non-loopback SFU/TURN advertise addresses for real devices.
