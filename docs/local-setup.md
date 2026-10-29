# Local setup

This guide shows the shortest safe path for running gramsrv on a development
machine or a small test server.

## 1. Prepare local configuration

The repository intentionally tracks only `.env.example`. Your real `.env` is
ignored by Git and must not be committed.

Linux / macOS:

```bash
cp .env.example .env
${EDITOR:-nano} .env
```

Windows PowerShell:

```powershell
Copy-Item .env.example .env
notepad .env
```

If you prefer a different config filename, set `TELESRV_CONFIG` as a process
environment variable before starting the server.

## 2. Set the network values

Review at least these values in `.env`:

- `TELESRV_LISTEN` is the MTProto bind address. Use `0.0.0.0:2398` when
  external clients must connect to this host, or `127.0.0.1:2398` for
  same-machine testing only.
- `TELESRV_ADVERTISE_IP` must be a client-reachable IPv4 or IPv6 address, not a
  DNS name. Use `127.0.0.1` only when the patched client runs on the same
  machine. Use a LAN or public IP for phones, other computers, or remote tests.
- `TELESRV_PUBLIC_BASE_URL` and `TELESRV_PUBLIC_WEB_BASE_URL` are HTTP(S) URLs
  used in generated public links. Put hostnames here, not in
  `TELESRV_ADVERTISE_IP`.
- `TELESRV_DEV_AUTH_CODE=12345` is convenient for local development but must not
  be exposed as a production login code.

## 3. Start Postgres and Redis

The development compose file exposes Postgres on `127.0.0.1:5432` and Redis on
`127.0.0.1:6399`, matching the defaults in `.env.example`.

```bash
docker compose -f deploy/docker-compose.yml up -d
```

The legacy development stack defaults to the Postgres password `telesrv` and
an empty Redis password. These defaults match the server even without `.env`,
and an existing empty `TELESRV_REDIS_PASSWORD` remains supported. For a fresh
volume, set `TELESRV_POSTGRES_PASSWORD` and `TELESRV_REDIS_PASSWORD` in `.env`
before starting Compose. The server derives its local Postgres DSN from the
former, including URL encoding, unless `TELESRV_POSTGRES_DSN` is explicitly set.
Single-quote passwords containing `$` in `.env` so Compose reads them literally.
Use the same `.env` and process environment for Compose and the server.
Process password variables take precedence over `.env`, including empty
values: an empty Postgres password selects `telesrv`, and an empty Redis
password disables Redis authentication in this development stack.

Changing the Postgres environment variable does not change a role in an
existing volume. To rotate it, stop the server and other application processes
that use this database, leaving Postgres running. As a trusted Docker
administrator, run:

```bash
docker exec -it telesrv-postgres psql -U telesrv -d postgres -c '\password telesrv'
```

Enter the new password twice at the prompts. This administrative connection
uses the container's local socket; it is not a check of the old password.
After the command succeeds, set `TELESRV_POSTGRES_PASSWORD` in `.env` to the same
value. If an explicit `TELESRV_POSTGRES_DSN` is configured, update its password
as well, URL-encoding reserved characters. Then run
`docker compose -f deploy/docker-compose.yml up -d` and restart the application
processes. For a Redis password change, also stop its application clients,
update `TELESRV_REDIS_PASSWORD` in the shared configuration, recreate Redis with
Compose, and restart the clients.

The Windows `restart-local-server.ps1` helper supplies its own DSN; when using
a custom Postgres password, pass the matching URL-encoded `-PostgresDSN`
explicitly. Do not use `down -v` for a password rotation; that deletes the database.

If you use external Postgres or Redis, update `TELESRV_POSTGRES_DSN` and
`TELESRV_REDIS_ADDR` in `.env`.

## 4. Build and run the server

Linux / macOS:

```bash
go build -o bin/gramsrv ./cmd/telesrv
./bin/gramsrv
```

Windows PowerShell:

```powershell
go build -o bin/gramsrv.exe ./cmd/telesrv
.\bin\gramsrv.exe
```

## 5. First-start checklist

After startup, confirm:

- migrations completed successfully;
- `data/server_rsa.pem` was created if it did not already exist;
- MTProto is listening on `TELESRV_LISTEN`;
- Postgres and Redis connections are healthy;
- patched clients use the matching DC address, port, and server RSA key.

For the complete configuration reference, see
[`docs/configuration.en.md`](configuration.en.md).

## 6. Importing the official Star-gift catalog (optional)

The admin panel can import official gifts explicitly and hash-verified, but it
reads them from a snapshot directory rather than from Telegram directly —
pointed at by `TELESRV_OFFICIAL_GIFTS_DIR` (`data/official-gifts` by default).
An empty or absent directory just means the import screen has nothing to offer;
nothing else in the panel depends on it.

To fill one, run `cmd/giftfetch`. It needs an authorized **user** session, which
you get with a QR login — no phone number, no SMS code:

```sh
SESSION=/root/.giftfetch.session go run ./cmd/giftfetch -login-qr
# scan the code: Telegram -> Settings -> Devices -> Link Desktop Device

SESSION=/root/.giftfetch.session go run ./cmd/giftfetch \
    -out /opt/gramsrv/data/official-gifts
```

The session file is a live account credential; keep it outside the repository
and out of version control. Full flag reference, the snapshot layout and the
validation the fetcher performs are in [`docs/giftfetch.md`](giftfetch.md).

## 7. Sticker, emoji and reaction seeds (optional)

Without a seed directory (`TELESRV_STICKER_SEED_DIR`, `data/sticker-seed` by
default) the server has no reactions, custom emoji or sticker sets. Export them
from official Telegram with `cmd/stickerfetch`, using the same kind of session:

```sh
SESSION=/root/.giftfetch.session go run ./cmd/stickerfetch data/sticker-seed reactions
SESSION=/root/.giftfetch.session go run ./cmd/stickerfetch -timeout 3h \
    data/sticker-seed/telegram_emoji_export featured_emoji
```

The directory layout, all set specs and the thumbnail requirements are in
[`docs/stickerfetch.md`](stickerfetch.md).
