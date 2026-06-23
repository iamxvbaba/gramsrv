# gramsrv

`gramsrv` is a Go implementation of a Telegram-like MTProto server, focused on
real client compatibility, repeatable protocol research, self-hosted chat
experiments, and ongoing tracking of newer Telegram client behavior.

[Website](https://telesrv.net) · [Discussion group](https://t.me/telesrv_chat) · [Channel](https://t.me/telesrv) · [中文 README](README.zh-CN.md)

![gramsrv multi-device Desktop and Android preview](docs/assets/readme-hero.png)

`gramsrv` is independent and unofficial. It is not affiliated with, endorsed by,
or sponsored by Telegram or the official Telegram team.

## Highlights

- **One server program to run.** After PostgreSQL and Redis are ready, the Go
  server process wires together RSA key preparation, database migrations,
  language pack seeding, MTProto edge handling, RPC routing, updates,
  media/files, and reliable dispatch workers.
- **Multi-device is implemented.** Telegram Desktop and Android clients can use
  the same server state, with scoped sessions, online fan-out, current-session
  exclusion, and offline recovery through update difference APIs.
- **Maintained with Telegram version tracking.** The public baseline stays
  reproducible, while newer Telegram Desktop and Android behavior is tracked
  through real client traces, compatibility notes, and targeted follow-up work.
- **Optimized hot paths are part of the design.** Batched reliable outbox
  delivery, warmed PostgreSQL pools, scoped session lookups, bounded RPC inputs,
  and seek-style pagination reduce repeated work on chat, sync, and media paths.
- **Telegram Desktop is the primary compatibility target.** The public build
  tracks a pinned TDesktop baseline and keeps compatibility work documented.
- **Android compatibility is active.** The current public screenshots include a
  patched Android client connected to the same server path.
- **Core chat paths work today.** Login, users, contacts, dialogs, private
  messages, supergroups/channels, media/files, profile/channel photos,
  stickers, reactions, language packs, and presence are covered on the main
  path.
- **Production boundaries are explicit.** Large-scale public channels,
  multi-DC/file-DC/CDN, Bot API, payments, stories, Premium business logic,
  abuse controls, and production object storage are outside the current public
  scope.

For downloads, public information, and the current hosted experience entry,
visit [telesrv.net](https://telesrv.net). For questions, compatibility reports,
and development discussion, join [t.me/telesrv_chat](https://t.me/telesrv_chat).

## Screenshots

| Telegram Desktop | Android |
|---|---|
| <img src="docs/assets/tdesktop1.png" alt="Telegram Desktop connected to gramsrv" width="520"> | <img src="docs/assets/android1.png" alt="Android client connected to gramsrv" width="260"> |

## Maintenance and Version Tracking

`gramsrv` does not treat one pinned client build as the end of the work. The
pinned Telegram Desktop baseline keeps regressions reproducible, while newer
Telegram Desktop and Android releases are followed separately through real
client startup/sync traces, compatibility matrix updates, and focused adaptation
tasks.

When a new Telegram client path appears, the expected workflow is to record it,
bound the inputs, decide whether it should be implemented, stubbed, or marked as
out of scope, and then keep the repository documentation in sync with the actual
behavior.

## Repository Layout

```text
cmd/telesrv/              server entrypoint
deploy/                   docker-compose and PostgreSQL migrations
internal/mtprotoedge/     MTProto transport, auth key, session, ack/resend
internal/rpc/             TL router and Telegram Desktop compatibility handlers
internal/app/             domain services
internal/domain/          protocol-independent domain models
internal/store/           store interfaces and memory/postgres/redis backends
docs/                     compatibility notes and module design docs
```

## Quick Start

Requirements:

- Go 1.25 or newer
- Docker Desktop or Docker Engine with Compose
- OpenSSL, if you want to build a matching Telegram Desktop client

Start PostgreSQL and Redis:

```powershell
docker compose -f deploy/docker-compose.yml up -d
```

Build and run the server:

```powershell
go build -o bin/gramsrv.exe ./cmd/telesrv
.\bin\gramsrv.exe
```

On first start, `gramsrv` creates `data/server_rsa.pem`, applies all database
migrations, seeds bundled language packs, and listens on `0.0.0.0:2398`.

Useful development environment variables:

| Variable | Default | Meaning |
|---|---:|---|
| `TELESRV_LISTEN` | `0.0.0.0:2398` | MTProto listen address |
| `TELESRV_ADVERTISE_IP` | `127.0.0.1` | IP written into `help.getConfig` |
| `TELESRV_DC` | `2` | self-hosted DC id |
| `TELESRV_DEV_AUTH_CODE` | `12345` | fixed login code for local development |
| `TELESRV_POSTGRES_DSN` | local Compose DSN | PostgreSQL connection string |
| `TELESRV_REDIS_ADDR` | `localhost:6399` | Redis address |
| `TELESRV_STICKER_SEED_DIR` | `data/sticker-seed` | optional exported sticker/reaction seed directory |

The optional sticker seed directory is skipped when it does not exist.

## Client Compatibility

Stock Telegram clients will not connect to `gramsrv` because they trust
Telegram's production DC list and RSA keys. Use a patched experience client from
the [official website](https://telesrv.net), or build your own client with a
minimal protocol patch.

Current Telegram Desktop baseline:

- Telegram Desktop commit: `9caf32dffc90ddd9bb08ad5777b865f729fa167b`
- TL layer: 225
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

## Documentation

- [Compatibility matrix](docs/compatibility-matrix.md)
- [Telegram Desktop patch notes](docs/tdesktop-patch-notes.md)
- [Persistence layer](docs/persistence-layer.md)
- [Message module](docs/message-module.md)
- [Channel module](docs/channel-module.md)
- [Performance audit](docs/performance-audit.md)

## Contributing

Compatibility-driven contributions are welcome. Useful areas include Telegram
Desktop and Android reports, reproducible RPC traces, focused bug fixes,
new Telegram client version reports, multi-device update tests, performance
work on already implemented paths, and documentation that makes local setup
easier.

If a change affects visible client behavior, include the client version/commit,
the RPC path you tested, and the server log result.
