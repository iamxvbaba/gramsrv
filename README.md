# gramsrv - Open Source Telegram Server / MTProto Server in Go

`gramsrv` is an open-source Telegram server implementation and MTProto server
written in Go. It is a Telegram-like backend for real client compatibility,
self-hosted chat experiments, protocol research, and long-running work toward a
practical community server.

If you are looking for a **Telegram server**, **MTProto server**,
**Telegram backend**, **Telegram clone server**, or **self-hosted
Telegram-like chat server**, this repository is the server-side implementation
to study, run, and improve.

[Website](https://telesrv.net) · [Discussion group](https://t.me/telesrv_chat) · [Channel](https://t.me/telesrv) · [中文 README](README.zh-CN.md)

`gramsrv` is independent and unofficial. It is not affiliated with, endorsed by,
or sponsored by Telegram or the official Telegram team.

## Project Keywords

`telegram server` · `telegram server implementation` · `mtproto server` ·
`mtproto server in go` · `telegram backend` · `telegram-like server` ·
`self-hosted telegram` · `telegram desktop compatible server` ·
`android telegram compatible server` · `open source chat server`

## Demo Video

https://github.com/user-attachments/assets/25e651dc-a022-4d60-8b9b-ca3e8bfe216c

## Project Traits

| Status | Trait | What it means |
|---|---|---|
| ✅ | One program startup | One Go binary prepares RSA keys, runs migrations, seeds data, opens MTProto, serves RPC handlers, dispatches updates, and starts workers. |
| ✅ | Fully open server code | Protocol edge, domain services, storage, compatibility handlers, media, updates, admin surfaces, and experiments are all in this repository. |

## Feature Checklist

Everything below is an implemented server-side capability in the open-source
codebase.

| Status | Feature | What works today |
|---|---|---|
| ✅ | MTProto server edge | TCP transport, RSA key exchange, auth keys, encrypted sessions, salts, ack/resend, bad messages, RPC dispatch, and layer compatibility helpers. |
| ✅ | Login and accounts | Development login code, sign-in, sign-up, log-out, authorizations, account settings, SRP/password state, email/passkey-oriented paths. |
| ✅ | Users and contacts | User profiles, usernames, profile photos, contact import/search, blocked/privacy state, presence, and last-seen style status. |
| ✅ | Dialogs and sync | Dialog list, pinned dialogs, manual unread, folders/filters, drafts, read boundaries, durable updates, online fan-out, and offline difference recovery. |
| ✅ | Chatlists and public links | Chat folder sharing, exported chatlist invite links, join/import flows, revoked invite handling, public username landing pages, and shared public link landing pages. |
| ✅ | Private chats | Send, history, read receipts, edit, delete, forward, reply, rich entities, grouped/media messages, reactions, scheduled/TTL-oriented paths. |
| ✅ | Rich messages | Telegram Desktop rich text messages, rich content conversion, send/edit/scheduled flows, dialog/history projections, and memory/PostgreSQL persistence. |
| ✅ | AI compose and ChatBot | Input-box rewrite/polish, default and custom tones, addstyle previews, local and external provider chains, streamed `@ChatBot` draft replies, and Business AI reply hooks. |
| ✅ | Supergroups and channels | Create, join, leave, invite links, participants, admins, forum topics, linked discussion guests, history, send/edit/delete/read, reactions, public search, and previews. |
| ✅ | Media and files | Upload, download, local blob storage, photos, documents, thumbnails, canonical GIFv conversion, external media fetch, web page previews, map tile cache hooks, profile/channel photos. |
| ✅ | Stickers and reactions | Sticker/reaction catalog, seed support, saved GIFs, recent reactions, top reactions, default reactions, and moderation-oriented reaction paths. |
| ✅ | Gifts and stars | Star gifts and local stars ledger foundations for compatibility and future feature work. |
| ✅ | Bots and mini apps | Bot service foundations, callbacks, inline helpers, webview/mini-app paths, a minimal Bot API gateway for libraries such as `python-telegram-bot`, persistent `getUpdates` delivery, and demo tools. |
| ✅ | Calls and live streams | Private call signaling foundations, group call state, RTMP live streaming, scheduled video chats, channel `join_as`, SFU/TURN building blocks, liveness, and expiry workers. |
| ✅ | Admin and operations | Admin API/UI backend, PostgreSQL migrations, Redis volatile state, retention workers, pprof/debug hooks, and load-test helpers. |
| ✅ | Desktop, Android, and Web focus | Telegram Desktop is the primary target, with Android and Web compatibility paths actively covered by the same server. |

Some items are compatibility-first or experimental, but they are real open
server code, not hidden product-only features. The next step is making these
paths stronger together.

## Quick Start

Requirements:

- Go 1.25 or newer
- Docker Desktop or Docker Engine with Compose
- OpenSSL, if you want to build a matching Telegram Desktop client

Start PostgreSQL and Redis:

```powershell
docker compose -f deploy/docker-compose.yml up -d
```

Build and run the single server program:

Windows (PowerShell):

```powershell
go build -o bin/gramsrv.exe ./cmd/telesrv
.\bin\gramsrv.exe
```

Linux / macOS:

```bash
go build -o bin/gramsrv ./cmd/telesrv
./bin/gramsrv
```

On first start, `gramsrv` creates `data/server_rsa.pem`, applies database
migrations, seeds bundled language packs, prepares optional media resources,
starts MTProto on `0.0.0.0:2398`, and brings up the update/media/background
workers in the same process.

Useful local environment variables:

See the complete [English configuration reference](docs/configuration.en.md) or
the [Chinese configuration reference](docs/configuration.zh-CN.md). `.env.example`
is a copyable development template, not an exhaustive parameter dictionary.

| Variable | Default | Meaning |
|---|---:|---|
| `TELESRV_LISTEN` | `0.0.0.0:2398` | MTProto listen address |
| `TELESRV_ADVERTISE_IP` | `127.0.0.1` | client-reachable fallback IP for media and calls |
| `TELESRV_DC` | `2` | self-hosted DC id |
| `TELESRV_DEV_AUTH_CODE` | `12345` | fixed login code for local development |
| `TELESRV_AUTH_CODE_MAX_ATTEMPTS` | `5` | wrong-code attempts before the code hash is deleted |
| `TELESRV_LOGIN_EMAIL_ENABLE` | `false` | send login codes to confirmed login email addresses through SMTP |
| `TELESRV_LOGIN_EMAIL_REQUIRE_SETUP` | `false` | force phone login/registration to set a login email first |
| `TELESRV_SMTP_HOST` | empty | SMTP host used when login email verification is enabled |
| `TELESRV_PUBLIC_BASE_URL` | `https://telesrv.net` | canonical external base URL for username, sticker, emoji, and chatlist links |
| `TELESRV_PUBLIC_APP_SCHEME` | `telesrv` | custom URL scheme opened by public landing pages |
| `TELESRV_PUBLIC_WEB_BASE_URL` | `https://web.telesrv.net` | Web client base URL shown on public landing pages |
| `TELESRV_PUBLIC_APP_NAME` | `telesrv` | display product name for public landing pages |
| `TELESRV_POSTGRES_DSN` | local Compose DSN | PostgreSQL connection string |
| `TELESRV_REDIS_ADDR` | `127.0.0.1:6399` | Redis address |
| `TELESRV_LANGPACK_SEED_DIR` | `data/langpack` | bundled language pack seed directory |
| `TELESRV_BLOB_DIR` | `data/blobs` | local media blob directory |
| `TELESRV_STICKER_SEED_DIR` | `data/sticker-seed` | optional sticker/reaction seed directory |
| `TELESRV_PUBLIC_LINK_WEB_ADDR` | empty | optional public link landing listener, for example `127.0.0.1:2401` |
| `TELESRV_BOT_API_ADDR` | empty | optional HTTP Bot API gateway listen address, for example `127.0.0.1:8081` |
| `TELESRV_BOT_API_UPDATE_RETENTION` | `24h` | retention window for unconfirmed Bot API `getUpdates` queue entries |
| `TELESRV_AI_ENABLED` | `true` | enable AI compose entry points |
| `TELESRV_AI_PROVIDERS` | `local` | ordered AI provider chain, such as `local` or `kimi,local` |
| `TELESRV_AI_TIMEOUT` | `15s` | per AI provider call timeout |
| `TELESRV_AI_RATE_LIMIT` | `20` | per-account AI compose request budget |
| `TELESRV_AI_RATE_WINDOW` | `1m` | AI compose rate-limit window |
| `TELESRV_AI_LOG_CONTENT` | `false` | whether logs may include prompt/generated text |
| `TELESRV_BUSINESS_AI_PROVIDER` | `echo` | Business automation reply provider |

The optional sticker seed directory is skipped when it does not exist.
Optional OpenAI-compatible, Kimi/Moonshot, Gemini, and Anthropic provider
variables are documented in `.env.example`.

## Public Deployment Ports

When deploying `gramsrv` on a public server, open the following ports according
to the features you enable.

### Minimal public deployment (chat only)

| Port | Protocol | Purpose | Required |
|---|---|---|---|
| 2398 | TCP | MTProto main port; also handles WebSocket when `TELESRV_WEBSOCKET_ENABLE=true` | Yes |

### With Admin backend

| Port | Protocol | Purpose | Notes |
|---|---|---|---|
| 2399 | TCP | Admin REST API | Restrict to trusted IPs or put behind VPN |
| 2600 | TCP | Admin Web UI | Use Nginx/reverse proxy + HTTPS in production |

### Optional feature ports

| Port | Protocol | Purpose | When needed |
|---|---|---|---|
| 2400 | TCP | RTMP live stream ingest | Live streaming |
| 12399 | UDP | SFU/WebRTC conferencing | Voice/video group calls |
| 12400 | UDP | TURN/STUN server | P2P/call relay |
| 12500-12999 | UDP | TURN relay port range | TURN relay |
| configurable | TCP | Bot API | When `TELESRV_BOT_API_ADDR` is set |
| 2401 example | TCP | Public username/sticker/chatlist landing pages | When `TELESRV_PUBLIC_LINK_WEB_ADDR=127.0.0.1:2401` is set |

### Internal/debug ports (do not expose publicly)

| Port | Default bind | Purpose |
|---|---|---|
| 6060 | `127.0.0.1:6060` | pprof debugging endpoint |
| 5432 | `127.0.0.1:5432` | PostgreSQL |
| 6399 | `127.0.0.1:6399` | Redis |

Make sure `TELESRV_LISTEN=0.0.0.0:2398` is set, and `TELESRV_ADVERTISE_IP`
points to your public IP so clients can connect.

## Public Link Landing Pages

`gramsrv` can serve public landing pages for `/<username>`, profile avatars,
`/addstickers/<shortName>`, `/addemoji/<shortName>`, and `/addlist/<slug>`.

Use `TELESRV_PUBLIC_LINK_WEB_ADDR` as the local HTTP bind address:

```env
TELESRV_PUBLIC_LINK_WEB_ADDR=127.0.0.1:2401
```

Use `TELESRV_PUBLIC_BASE_URL` as the external canonical URL shown in generated
links:

```env
TELESRV_PUBLIC_BASE_URL=https://your-domain.example
TELESRV_PUBLIC_APP_SCHEME=yourapp
TELESRV_PUBLIC_WEB_BASE_URL=https://web.your-domain.example
TELESRV_PUBLIC_APP_NAME=YourApp
```

In production, keep `TELESRV_PUBLIC_LINK_WEB_ADDR` on loopback and reverse-proxy
the public routes to it with HTTPS.

## Client Compatibility

Stock Telegram clients will not connect to `gramsrv` because they trust
Telegram's production DC list and RSA keys. Use a patched experience client from
the [official website](https://telesrv.net), or build your own client with a
minimal protocol patch.

Current Telegram Desktop baseline:

- Telegram Desktop commit: `9caf32dffc90ddd9bb08ad5777b865f729fa167b`
- TL layer: 227
- Local DC: `127.0.0.1:2398`, DC id `2`

After `gramsrv` generates `data/server_rsa.pem`, export the matching public key:

```powershell
openssl rsa -in data/server_rsa.pem -RSAPublicKey_out -out data/server_rsa.pub
```

Patch `Telegram/SourceFiles/mtproto/mtproto_dc_options.cpp`:

1. Replace the built-in production/test DC lists with your `gramsrv` endpoint.
2. Replace both `kPublicRSAKeys` and `kTestPublicRSAKeys` with
   `data/server_rsa.pub`.
3. Add `Flag::f_tcpo_only` to the built-in DC flags.

Keep the client patch minimal: endpoint, RSA key, and TCP-only flags only.

## Multi-Device Smoke Test

Use separate client working directories so sessions do not share local `tdata`:

```powershell
$tdesktop = "C:\path\to\tdesktop\out\Debug\Telegram.exe"
Start-Process $tdesktop -ArgumentList @("-workdir", "$PWD\.tdata-alice")
Start-Process $tdesktop -ArgumentList @("-workdir", "$PWD\.tdata-bob")
```

Log in with different phone numbers. In local development, the login code is
`12345` unless you changed `TELESRV_DEV_AUTH_CODE`.

Recommended checks:

- Send private messages, stickers, media, replies, forwards, edits, deletes,
  and read receipts between two users.
- Keep one device online and restart another device to verify offline
  `updates.getDifference` recovery.
- Open the same account from multiple sessions and confirm current-session
  echoes are not duplicated while other online sessions receive updates.
- Check server logs for no new `NOT_IMPLEMENTED`, `Unhandled RPC`, `bad_msg`,
  panic, or internal errors.

## Contributors

- [ajarshia](https://github.com/ajarshia) - Android Persian (`fa`) language pack.

## Repository Layout

```text
cmd/telesrv/              server entrypoint
cmd/telesrv-admin/        admin backend and web UI
deploy/                   docker-compose, migrations, deploy helpers
data/                     bundled language packs and optional seed data
internal/mtprotoedge/     MTProto transport, auth key, session, ack/resend
internal/rpc/             TL router and client compatibility handlers
internal/app/             domain services
internal/domain/          protocol-independent domain models
internal/store/           memory/postgres/redis storage backends
internal/seed/            bundled seed catalog loaders
internal/sfu/             real-time SFU experiments
internal/turnsrv/         TURN/STUN building blocks
```

## TODO List

- Improve Bot compatibility for third-party libraries, such as `python-telegram-bot`.
- Fix known bugs and keep hardening existing compatibility paths.

## Help Improve It

`gramsrv` will get better fastest if more people run it, break it, profile it,
and send focused improvements. Helpful contributions include:

- Telegram Desktop and Android compatibility reports with reproducible steps.
- RPC traces for startup, sync, chat, media, calls, bots, or edge cases.
- Focused fixes for implemented paths instead of broad rewrites.
- Tests for online/offline updates, multi-device sessions, read state, media,
  and channel behavior.
- Performance work on hot paths such as fan-out, pagination, storage queries,
  media upload/download, and connection handling.
- Setup improvements that make the one-program local experience smoother.

If a change affects visible client behavior, please include the client
version/commit, the RPC path you tested, and whether server logs stayed free of
new `NOT_IMPLEMENTED`, `Unhandled RPC`, `bad_msg`, panic, or internal errors.

## License

`gramsrv` is released under the [Apache License 2.0](LICENSE). You may use,
modify, distribute, and use it commercially under the terms of Apache-2.0.

## Custom Development

For paid custom development, you can contact the author through the discussion
group or website. Custom work can cover server features, Telegram Desktop,
Android, Web, deployment, compatibility adaptation, or other client/server
paths around this project.
