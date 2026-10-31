# AGENTS.md

Telegram-compatible MTProto server written in Go. This file is a module map so you can find and fix things without re-learning the codebase.

## Identity and branches

- Go module is `telesrv` (Go 1.26), repo is `gramsrv`, binary is `telesrv`. First compatibility target: **Telegram Desktop**. Protocol comes from `github.com/iamxvbaba/td` (fork of gotd/td); do not vendor or fork `td` here.
- The TL schema layer lives in `td`, not here: `td/tg` holds the canonical Go model and `td/tlprofile` the exact per-layer profiles. Canonical Layer is **232** (`td` v1.7.0, plain module requirement — no local `replace`). `tg.Layer`/`tlprofile.ProfileCanonical` follow it; bumping a layer means a new `td` release first, then the layer-pinned tests in `internal/rpc/layer*_test.go`.
- `main` = **monolith** (this branch), `v2` = microservices (Edge/Core/Egress/File/SFU). CI runs on `main`, `grammy`, `admin`. Current checkout: `main`.
- Comments, logs and package docs are written in **English**; keep it that way for new code. Localized, user-facing text lives only in the locale files (`data/langpack/**`, the admin `i18n.tsx`, the grammystore `i18n.js`/`catalog.js` `*Ru` fields, `docs/*.zh-CN.md`, `docs/*.ru.md`). PowerShell artifacts may display `???` for non-ASCII in some terminals — the files are UTF-8. A guard test (`internal/translation`) fails if CJK/Cyrillic appears in source outside those paths.
- No Makefile, no linter config, no test framework beyond stdlib `testing`. Formatting = `gofmt`.

## Commands

```powershell
go build -o bin/gramsrv.exe ./cmd/telesrv        # main server
go vet ./...
go test ./... -count=1                            # CI gate, passes without external services
```

### Tests that need Postgres

Postgres integration tests are `*_integration_test.go` in `internal/store/postgres` and `cmd/telesrv-admin`. They **skip** unless `TELESRV_TEST_POSTGRES_DSN` is set, and the DSN's database name **must contain "test"** or the helper fails fast (see `testPool` in `internal/store/postgres/authkey_integration_test.go`). Run a focused subset like CI does:

```powershell
$env:TELESRV_TEST_POSTGRES_DSN="postgres://inventory:inventory-test@127.0.0.1:5432/gift_inventory_test?sslmode=disable"
go test ./internal/store/postgres -run '^(TestOfficialStarGiftInventoryPostgres|TestStarsLedgerPostgres)$' -count=1
go test ./cmd/telesrv-admin -run '^(TestOperatorCommand|TestAuditTrail)$' -count=1
```

- `deploy` Compose integration test (`deploy/docker-compose_test.go`) SKIPS unless `TELESRV_RUN_COMPOSE_INTEGRATION=1` — it actually boots a Docker stack. Don't run by accident.
- Most `internal/rpc` and `internal/app` tests run against in-memory stores (`internal/store/memory`, `rpc_testkit_*`) and are pure `go test`.
- Load testing: `cmd/telesrv-load` binary + `internal/loadtest`, `internal/loadharness`.

### Building the browser surfaces (Node, different repos)

- Admin web UI: `cmd/telesrv-admin/web` (`npm ci`, `npm run build`).
- Grammy store bot: `cmd/bots/grammystore` (TypeScript, own deployment: `npm run check`, `npm test`, own Dockerfile). Only target these if the task is about the admin UI or the store bot.

### Adding a page to the admin panel

The panel's navigation was a flat sidebar that every feature added a row to, and
the three things that render it -- the sidebar, the in-page tab bars, the route
table -- each kept their own list and their own idea of permissions. They
drifted: twelve sidebar entries rendered unconditionally while every endpoint
behind them was gated, so an operator without `accounts.read` saw Accounts and
got a bare 403. `src/nav.ts` replaced all three lists with one declaration.

The rules that keep it that way:

- **A sibling page is a tab, not a second sidebar row.** If the new page does the
  same job as an existing one over a different slice, add it to that section's
  `tabs` array. Commerce holds Prices and the Stars ledger; Media holds Storage;
  Accounts holds Account Rating and Shared devices. A new row is only for a
  genuinely new job. A section with a single visible tab draws no tab bar.
- **A tab that is a single button is not a tab.** The Stars giveaway was a page
  with one field and one button; it now lives at the bottom of the Stars &
  Premium page, in its own warned frame, and the old `/star-issue` URL is an
  alias of that tab (`aliases` in `nav.ts` keeps the sidebar row and the tab bar
  lit on it).
- **Declare the tab's real right.** It must be the right the route enforces
  server-side. A right that is not in the table has no route of its own -- it
  widens an action on a page already gated by its read right. If you need a new
  right, it goes in `permissions.tsx` (`permissionLabels` *and*
  `permissionGroups`) *and* `assignablePermissions()` in `security.go`; a right
  outside that list cannot be granted to a named operator.
- **Do not hand-wrap a route in `PermissionGate`.** The gate already comes from
  `routePermission(path)`. Hand-wrapping is what let the gaps accumulate.
- **A section shows when the session holds *any* of its tabs' rights**, and links
  to the first visible tab -- don't add your own visibility logic.
- **Translate every string** via `useI18n()` into all three language blocks of
  `i18n.tsx`.
- **Rebuild `dist` and commit it.** `cmd/telesrv-admin/server.go` has
  `//go:embed web/dist`, so a non-Docker build ships the committed bundle: a
  `web/src` change without a rebuilt `dist` does not ship. Delete the old hashed
  assets rather than leaving them.
- **Regenerate the API docs:** `python3 scripts/gen_admin_panel_doc_tables.py --write`
  (both `docs/admin-panel-api.*.md`); `--check` fails if they are stale.

### SQL codegen

`internal/store/postgres/sqlcgen` (package `sqlcgen`, pgx/v5, `emit_pointers_for_null_types`) is generated by sqlc from `sqlc.yaml`. Schema is **only** `deploy/migrations/*.up.sql`; the `.down.sql` files are deliberately excluded so sqlc never sees DROPs. Changes to `internal/store/postgres/queries/*.sql` or `.up.sql` require regenerating: `sqlc generate` (install sqlc first; not in PATH by default). Generated code is committed — keep it in sync.

## Request flow (how the server is wired)

`cmd/telesrv/main.go` (one huge `run()` ~1900 lines) wires everything:

```
MTProto client
  → internal/mtprotoedge   TCP + WS transport, handshake, auth keys, sessions,
                           encryption, admission budget (conn limits, RPC inflight,
                           byte budgets)
  → internal/rpc           TL dispatch (Router), wrapper handling
                           (invokeWithLayer/initConnection/invokeAfter, max depth 8),
                           layer binding, temp-key resolution, panic isolation
  → internal/app/*         feature business logic (43 packages)
  → internal/store         persistence: postgres | memory | redisstore
  ← updates/state          outbox dispatchers, durable delivery, projections
```

Binaries in `cmd/`: `telesrv` (server), `telesrv-admin` (admin dashboard), `telesrv-load`, `telesrv-update` (standalone update CDN, defunct in monolith unless `TELESRV_UPDATE_PUBLIC_URL` set), `gramsrv-backup` (bundle capture/restore/verify; guide in `docs/backup-restore.md`, wrappers in `scripts/backup.{sh,ps1}`, `scripts/restore.{sh,ps1}`, `scripts/check-media.{sh,ps1}`), plus fetch/seed utilities (`catalogfetch`, `giftfetch` — guide in `docs/giftfetch.md`, including the `-login-qr` session flow — `langpackfetch`, `stickerseeddeploy`, `blobmigrate`, `telegramloginkeygen`, `walletminiapp`, `bots/*`).

## Module map (find-and-fix guide)

- **`internal/domain/`** — pure protocol-agnostic types (users, messages, media, star gifts, premium…). Shared structs; change here when a field must appear everywhere.
- **`internal/rpc/`** (460 files, the biggest package) — one file (or `_rpc_test.go`) per feature/namespace: `messages_send.go`, `channels_members.go`, `payments_star_gifts.go`, `presence_work.go`, `convert_*.go` (TL conversion), `errors.go` (error mapping to TL), `layer_*.go` (layer gating). **Most bug fixes land here or in `app/`.** `router.go` holds dispatch, `deps.go` the injected service set (`rpc.Deps`, validated by `deps_validation_test.go`).
- **`internal/app/*`** — feature business logic used by the RPC layer. Pattern: `service.go` + tests. Start from the `.go` file matching the feature name, e.g. `app/stargifts`, `app/messages`, `app/verification`.
- **`internal/store/`** — three parallel backends, same store interfaces:
  - `postgres/` — real persistence; hand-written SQL in `queries/*.sql`, sqlc output in `sqlcgen/`, store methods in `<feature>.go`, tests in `*_integration_test.go` + in-memory `storetest`.
  - `memory/` — in-memory implementations used by fast RPC tests (not production).
  - `redisstore/` — ephemeral state: rate limits, ephemeral keys, delivery.
- **`internal/mtprotoedge/`** — protocol edge: transport, auth key lifecycle, session management, frame budgets. Concurrency/budget bugs (10k sessions, reconnects) are here.
- **`internal/compat/{android,ios,tdesktop}/`** — per-client quirks. If a fix targets one specific client, check here first (e.g. `compat/tdesktop` for TDesktop wire behavior).
- **`internal/botapi/`** — Telegram Bot API on port 8081; `botapi_*` Python scripts in `scripts/` selftest it.
- **`internal/admin/`**, **`internal/adminapi/`** — admin panel state/ops + admin HTTP API (port 2599) with RBAC (`adminapi/rbac.go`, scoped tokens).
- **`cmd/telesrv-admin/web/`** — the panel's React SPA. **`src/nav.ts` is the only place navigation is declared**: every sidebar section, the paths belonging to it, and the right each path enforces server-side. `Layout.tsx` renders the sidebar from it, `SectionTabs.tsx` reads each section's tab list from it, and `Routes.tsx` gates the whole surface from it through `routePermission(path)`. **Read this before adding a page** — the rules are below.
- **`internal/web/`** — public-link web server, port 2401, `/healthz` returns `ok`.
- **`internal/sfu/`** — embedded WebRTC SFU (pion); **`internal/turnsrv/`** — embedded TURN/STUN. Both optional, log `sfu listening`/`turn listening` on readiness.
- **`internal/otpdelivery/`** — phone-code delivery providers (`smtp/`, `webhook/`, `development`). `internal/telegramloginhttp/` + `internal/app/telegramlogin/` — Telegram Login/OIDC provider (needs `cmd/telegramloginkeygen`).
- **`internal/branding/`**, **`internal/config/`**, **`internal/observability/{metrics,dbtrace}/`**, **`internal/readmodelcache/`**, **`internal/links/`** — cross-cutting.
- **`deploy/`** — `migrations/` (embedded via `deploy/deploy.go` → `embed.FS`), `docker-compose.yml` (dev deps: Postgres 17 + Redis 7, same branch's DBs), `docker/` (production compose, entrypoint, assets, bridge-network override), `postgres-init/`.

## Migrations

- `deploy/migrations/NNNN_name.up.sql` + matching `.down.sql`; single source of truth for both golang-migrate (embedded, run on startup via `store/postgres.Migrate`) and sqlc. **Migration 0123 is deliberately skipped** — the sequence jumps 0122 → 0124, don't reuse 0123.
- DBs are branch-isolated: `telesrv_main` and `telesrv_v2` in one container (`deploy/postgres-init/010_branch_databases.sql`). Legacy `telesrv` DB is kept for compat/migration only — never touch it.
- Windows dev scripts derive the Postgres DB from the current branch name (`telesrv_main` for `main`, `telesrv_v2` for `v2`).

## Config and dev loop

- Config loads from `.env` in the working dir (or process env; `TELESRV_CONFIG` overrides the file path). Only `.env.example` is tracked — never commit `.env`.
- Dev login code is `12345`. RSA key auto-generated at `data/server_rsa.pem` if missing.
- Windows helpers (repo-root PowerShell, the normal dev loop):
  - `scripts/restart-local-server.ps1` — build `cmd/telesrv`, kill old process on port, restart, wait for listen; resolves the branch-implied Postgres DSN via `ensure-local-databases.ps1`.
  - `scripts/check-local-runtime.ps1` — read-only health check (listener, migrations, log readiness line/errors, adb).
  - `scripts/start-docker.ps1` — one-command Docker launch; generates `deploy/docker/.env` on first run.
- Ports: MTProto **2398**, RTMP/livestream **2400**, public links **2401**, Bot API **8081** (host-facing side), admin API **2599**, admin UI **2600**, SFU UDP **12399**, TURN UDP **12400**, TURN relay 12500–12999 (bridge mode max 12563), Postgres host **15432** (dev compose: 5432), Redis host **16379** (dev compose: 6399).

## Docker

- Production compose: `deploy/docker/compose.yaml` (host networking by default; `compose.bridge-network.yaml` for hosts without it). Images: `ghcr.io/iamxvbaba/gramsrv/{server,admin,grammystore}`.
- Hardened containers: `read_only` rootfs, `cap_drop: [ALL]`, `pids_limit: 1024`, user 10001:10001, entrypoint rejects empty/`changeme` secrets. CI (`docker-smoke`) asserts all of this; keep it true.
- Image build targets: `server` (generated RSA identity) vs `server-test` (embedded public test key, default). CI publishes `server-test`.

## Gotchas

- `go test ./...` takes a while (many packages, chromedp-free but heavy). Prefix `-run` when iterating.
- Some tests assert on log lines like `sfu listening` / `telesrv server ready` — don't remove those readiness messages.
- `scripts/install.sh` is git-ignored on purpose (internal deployment script, not part of the public repo). Don't add it.
- `telesrv-admin.exe` at the repo root is a force-added artifact; ignore it.
- `.session` and `*.session.qr.png` are **Telegram account credentials**, produced by `giftfetch -login-qr` / `appearancefetch` / `scripts/giftfetch_telethon.py` (whose `--session` defaults to a bare relative name, so running it from the repo root drops one in the tree). They are git-ignored; keep it that way and never quote a session path into a committed example.
- `docs/admin-panel-api.{en,ru}.md` carry **generated** route and permission tables (`scripts/gen_admin_panel_doc_tables.py`). Edit `nav.ts` / `permissions.tsx` and rerun with `--write`; `--check` is the form worth running in CI. They drifted badly once because nothing checked them.
