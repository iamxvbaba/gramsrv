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
| `TELESRV_BOT_API_ADDR` | nullable address / empty | Minimal HTTP Bot API listener. Empty disables it. It shares MTProto app/store facts. `setWebhook` accepts any valid `http://` or `https://` host/IP and port in `1..65535`. |
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
| `TELESRV_PUBLIC_LINK_WEB_ADDR` | nullable address / empty | Read-only username/avatar/sticker/emoji/chatlist/collectible-gift landing-page listener. Empty disables it. Production should bind loopback behind exact nginx routes. `.env.example` enables `127.0.0.1:2401` for development. |
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
| Bot API base URL | the bot application's `TELEGRAM_API_URL` or equivalent | A client-reachable address for telesrv, for example `http://172.17.0.1:8088`. Method URLs are `<base>/bot<TOKEN>/<method>` and file URLs are `<base>/file/bot<TOKEN>/<file_path>`. |
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
`0.0.0.0:8088`, a container on the same host might use `http://172.17.0.1:8088`; it must not request
`http://0.0.0.0:8088`.

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
```

The discovery `issuer` must equal the configured value, and its `authorization_endpoint`,
`token_endpoint`, and `jwks_uri` must be reachable by the relying party. A reverse proxy must pass
through `/.well-known/openid-configuration`, `/.well-known/jwks.json`, `/auth`, `/auth/status`,
`/token`, `/crossapp`, `/inapp`, `/telegram-login.js`, and `/js/telegram-login.js` unchanged.

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
| `TELESRV_POSTGRES_DSN` | secret DSN / `postgres://telesrv:telesrv@127.0.0.1:5432/telesrv?sslmode=disable` | Primary durable business database. Production must replace the development credentials and TLS policy. |
| `TELESRV_POSTGRES_MAX_CONNS` | int / `50` | pgxpool maximum connections. `<=0` delegates to pgx defaults, which are usually too small for production outbox/RPC concurrency. |
| `TELESRV_POSTGRES_MIN_CONNS` | int / `16` | pgxpool pre-warmed minimum connections. |
| `TELESRV_REDIS_ADDR` | address / `127.0.0.1:6399` | Redis used for volatile codes, limits, and shared update/cache state. |
| `TELESRV_REDIS_PASSWORD` | secret string / empty | Redis password. |
| `TELESRV_REDIS_DB` | int / `0` | Redis logical database number. |
| `TELESRV_LANGPACK_SEED_DIR` | path / `data/langpack` | TDesktop `.strings` language-pack seed directory. |
| `TELESRV_OFFICIAL_GIFTS_DIR` | path / `data/official-gifts` | Read-only snapshot generated by `cmd/giftfetch`, used for verified explicit imports in the admin UI. |
| `TELESRV_BLOB_DIR` | path / `data/blobs` | Local development blob-backend root for media bytes. |
| `TELESRV_STICKER_SEED_DIR` | path / `data/sticker-seed` | Sticker/reaction seed packages imported into documents, sticker sets, and blobs. |
| `TELESRV_STICKER_SEED_MAX_SETS` | int / `300` | Maximum regular sticker sets imported at startup; `<=0` means unlimited. |

The language-pack file manifest is authoritative. To add a language, place `data/langpack/<pack>/<pack>_<lang>_v<version>.strings` and restart `telesrv`. The `pack` must match its first-level directory and may use the letters, digits, `-`, and `_` already used by Telegram (for example, `android_x`); `lang` is canonicalized to lowercase with hyphens (`pt_BR` becomes `pt-br`). Only the highest file version for each language is loaded. Effective content changes require a version bump; same-version effective mutations and version rollbacks stop startup. Removing a language file or an entire pack subdirectory atomically removes its database catalog and strings on the next restart. Startup streams a source-file SHA-256 first: unchanged files reuse the last atomic manifest without parsing strings or writing the database, while only new or changed files are parsed and replaced through PostgreSQL `COPY`.

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
| `TELESRV_SMTP_FROM_NAME` | string / `telesrv` | Display name for login-email messages. |
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
| `TELESRV_STARGIFT_SWEEP_INTERVAL` | duration / `15s` | Local Star Gift offer/auction lifecycle sweep interval; no blockchain connection is made. |
| `TELESRV_STARGIFT_SWEEP_BATCH` | int / `1000` | Maximum offer/auction/outbox work claimed per lifecycle sweep. |
| `TELESRV_STARGIFT_TON_STARTING_GRANT` | int64 / `10000000000` | Nanoton granted idempotently on a user's first access to the internal telesrv TON ledger; `0` disables it. This is not an on-chain asset. |
| `TELESRV_STARGIFT_TRANSFER_STARS` | int64 / `25` | Stars charged for a collectible transfer; `0` enables the free-transfer RPC. |
| `TELESRV_STARGIFT_DROP_DETAILS_STARS` | int64 / `25` | Stars charged to remove a collectible's original sender/message details. |
| `TELESRV_STARGIFT_OFFER_MIN_STARS` | int / `1` | Minimum Stars offer snapshotted for user-owned collectibles; `0` disables the offer entry point. |
| `TELESRV_STARGIFT_STARS_PROCEEDS_PERMILLE` | int / `1000` | Seller share in Stars sales, in permille; the remainder is recorded as platform commission. |
| `TELESRV_STARGIFT_TON_PROCEEDS_PERMILLE` | int / `1000` | Seller share in internal-TON sales, in permille; this affects only the local ledger. |
| `TELESRV_STARGIFT_EXPORT_DELAY` | duration / `0s` | Delay snapshotted into `can_export_at` when a collectible is issued. |
| `TELESRV_STARGIFT_TRANSFER_DELAY` | duration / `0s` | Delay snapshotted into `can_transfer_at`. |
| `TELESRV_STARGIFT_RESELL_DELAY` | duration / `0s` | Delay snapshotted into `can_resell_at`. |
| `TELESRV_STARGIFT_CRAFT_DELAY` | duration / `0s` | Delay snapshotted into `can_craft_at`. |
| `TELESRV_STARGIFT_CRAFT_CHANCE_PERMILLE` | int / `250` | Per-input local craft success contribution, capped at 1000 permille. |

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

At minimum, production operators should explicitly review and override the development credentials/endpoints: PostgreSQL DSN and TLS, Redis password/network exposure, RSA key persistence, fixed development auth code exposure, Admin credentials/session key, OTP Webhook/SMTP secrets, AI/Mapbox API keys, TURN secret and firewall ports, public URLs/scheme alignment, and non-loopback SFU/TURN advertise addresses for real devices.
