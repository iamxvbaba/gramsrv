# Backup and restore

`cmd/gramsrv-backup` captures a gramsrv deployment and puts it back on another
host: the PostgreSQL databases, the media and key tree, and the configuration
needed to bring the same instance up again.

Use it for host migrations, for moving between the monolith and the Compose
deployment, and for scheduled snapshots you can restore from if a disk dies.

- [What a bundle contains](#what-a-bundle-contains)
- [Capturing a bundle](#capturing-a-bundle)
- [Verifying a bundle](#verifying-a-bundle)
- [Restoring onto a new host](#restoring-onto-a-new-host)
- [Checklist after a restore](#checklist-after-a-restore)
- [Updating before you migrate](#updating-before-you-migrate)
- [Why the dumps look the way they do](#why-the-dumps-look-the-way-they-do)
- [Troubleshooting](#troubleshooting)

## What a bundle contains

A bundle is a directory. Copy it with `rsync`, `scp` or a zip; nothing in it
depends on the host it came from.

```
<bundle>/
  MANIFEST.json          what was captured, from which commit, with which row counts
  SHA256SUMS             sha256sum(1) compatible digests for every artifact
  server/
    schema.sql.zst       plain SQL dump of the main database
    globals.sql.zst      roles and their password hashes
    redis.rdb.zst        Redis snapshot, when captured
    data.tar.zst         the data/ tree: media, server RSA key, login signing keys
    config.tar.zst       .env, systemd units, Compose files
  grammystore/           the same layout for the store bot, when selected
```

`MANIFEST.json` records the git commit, the schema version and the exact row
counts of the tables that matter for a migration. Compare those numbers after a
restore to prove the copy is complete.

**A bundle contains production secrets.** It includes `.env`, the server RSA key
and the Telegram Login signing keys. Keep it out of version control, transfer it
over a channel you trust, and delete it once the new host is verified.

Two things are deliberately *not* in a bundle:

- **Compiled binaries.** Rebuild from the commit in the manifest.
- **Reverse proxy configuration.** TLS and routing live outside gramsrv.

## Capturing a bundle

The scripts pick sensible defaults and build the tool if needed:

```bash
./scripts/backup.sh --component server
```

For a capture that is consistent down to the media files, stop the writers. They
are always restarted, including when the capture fails:

```bash
./scripts/backup.sh --component all \
  --pg-container-server telesrv-postgres \
  --pg-container-grammystore grammystore_postgres_dev \
  --freeze-units gramsrv.service,gramsrv-admin.service \
  --freeze-containers grammystore_app_dev
```

Or call the tool directly:

```bash
go build -o bin/gramsrv-backup ./cmd/gramsrv-backup
./bin/gramsrv-backup backup --out /srv/backups/20261004 --component all \
  --include-data --pg-tools docker --pg-container-server telesrv-postgres
```

Notes that save time on a real host:

- **A database dump needs no downtime.** `pg_dump` takes a consistent snapshot
  while the server keeps running. Freezing only matters for `data.tar.zst`:
  without it a tar can catch an upload mid-file and store a truncated object.
- **`--pg-tools docker`** uses the `pg_dump` inside your database container, which
  is always the matching major version. Use `--pg-tools local` when the host has
  client tools installed; add `--pg-bin-dir` if they are not on `PATH`.
- **Each component has its own database**, so pass `--pg-container-server` and
  `--pg-container-grammystore` when they run in different containers.
- **`--skip-redis`-style choices exist**: `-include-redis=false` omits the Redis
  snapshot. Redis holds rate limits and delivery bookkeeping, so a fresh Redis is
  usually acceptable.
- **Budget roughly the size of `data/` plus a compressed database.** A small
  instance produces tens of megabytes; an instance with a large media store
  produces gigabytes.

## Verifying a bundle

Always verify before you rely on a bundle, and again after transferring it:

```bash
./bin/gramsrv-backup verify --from /srv/backups/20261004
```

This re-hashes every artifact, confirms each one still decompresses, and prints
the recorded row counts.

To check the media tree of a *running* instance against its database, which
catches a truncated copy:

```bash
./scripts/check-media.sh --pg-container telesrv-postgres
```

```
media: referenced=9794 on_disk=9940 bytes=1126781238 missing=0 orphan=146 corrupt=0
```

`missing` and `corrupt` must both be zero. `orphan` counts files no row
references, which is harmless leftover weight.

## Restoring onto a new host

The bundle restores databases, files and configuration. It does not install
Docker, start containers, or move DNS. Plan for that part yourself.

1. **Prepare the host.** Install Docker with Compose, and make sure the ports the
   deployment needs are reachable: the MTProto listener, the public link web
   endpoint, the admin API and UI, RTMP ingest, and the media ports (SFU UDP and
   the TURN UDP port plus its relay range). Take the exact values from your
   `.env`; the defaults are documented in `docs/configuration.en.md`.

2. **Put the code in place.** Check out the commit recorded in `MANIFEST.json`:

   ```bash
   git clone https://github.com/iamxvbaba/gramsrv.git
   cd gramsrv && git checkout <commit from MANIFEST.json>
   ```

3. **Start the databases.** Bring up Postgres and Redis with your Compose files
   before restoring, otherwise there is nothing to restore into. Use the same
   passwords as the source host: they come back with `globals.sql.zst`.

4. **Restore the bundle:**

   ```bash
   ./scripts/restore.sh --from /srv/backups/20261004 --dry-run   # read the plan first
   ./scripts/restore.sh --from /srv/backups/20261004 --force
   ```

   `--force` is required to overwrite files that already exist. The schema
   restore runs in a single transaction, so a failure leaves the database as it
   was rather than half migrated.

5. **Adjust the host-specific settings** in the restored `.env`:
   - `TELESRV_ADVERTISE_IP` must be the new address. This is the one setting that
     is always required: the TURN and SFU advertise addresses fall back to it, so
     a wrong value breaks calls even though text messaging works.
   - `TELESRV_PUBLIC_BASE_URL` and the related public URL settings if your domain
     changes.
   - Any password or token you deliberately rotated.

6. **Start gramsrv and the admin panel**, then run the checklist below.

## Checklist after a restore

Work through this in order; each step catches a different class of mistake.

- [ ] `/healthz` on the public link web endpoint returns `ok`.
- [ ] The admin panel loads and the operator list is intact.
- [ ] **Open Telegram Desktop with an account that was already logged in on the
      old host.** Staying signed in proves the RSA key and the auth keys were
      restored. Being asked for a code again means the key is not the one the
      client pinned.
- [ ] Download a photo and a video that were uploaded before the migration.
- [ ] Place a voice or video call. It exercises the advertised media addresses.
- [ ] The store bot answers, if you run one.
- [ ] `check-media.sh` reports `missing=0 corrupt=0`.
- [ ] Row counts in `MANIFEST.json` match what the restored database reports.
- [ ] Only then move DNS, and keep the old host reachable until the new one has
      been up long enough to be confident.

## Updating before you migrate

Migrate the code and the data together. If you update gramsrv first and back up
afterwards, the bundle matches the version you are deploying, which is what you
want.

```bash
git fetch origin && git merge --ff-only origin/main
gofmt -l ./cmd ./internal        # expect no output
go vet ./...
go test ./... -count=1
```

Then reconcile configuration before restarting, because a new release can add
settings:

```bash
diff <(grep -oE '^[A-Z_]+=' .env.example | sort -u) \
     <(grep -oE '^[A-Z_]+=' .env | sort -u)
```

Anything listed by `.env.example` and missing from your `.env` needs a value.
Keys your deployment carries that upstream dropped are ignored and can stay.

Build and swap binaries in one restart, and keep the previous build so you can
roll back:

```bash
cp bin/gramsrv bin/gramsrv.previous
go build -o bin/gramsrv ./cmd/telesrv
go build -o bin/gramsrv-admin ./cmd/telesrv-admin
systemctl restart gramsrv gramsrv-admin
journalctl -u gramsrv -f | grep 'server ready'
```

Start the server only through its service manager. Running the binary by hand
binds the same ports as the running instance and applies pending migrations,
which is not what you want during an update.

## Why the dumps look the way they do

The artifacts are tuned for one goal: replaying them on a different host, possibly
on a different PostgreSQL major version, with the least surprise.

- **Plain SQL, not the custom format.** Any `psql` can replay plain SQL, and it
  does not have to match the `pg_dump` that produced it. The custom format ties a
  dump to a client version.
- **`--clean --if-exists`.** The dump can be replayed over a populated database,
  which makes re-restoring a single component possible.
- **`--no-owner --no-privileges`.** Ownership comes from the role that replays
  the dump, so a differently named role on the target is not a problem.
- **No `--create`.** The database name is never baked into the artifact, so the
  same dump restores into a scratch database for testing.
- **Restored with `ON_ERROR_STOP=1 --single-transaction`.** Either the whole
  schema lands or nothing does.
- **Two constructs are stripped on the way in.** `\restrict` and `\unrestrict`
  are psql meta-commands added in PostgreSQL 17.5 as a mitigation for
  CVE-2025-8714; older clients reject them. `SET transaction_timeout` only exists
  on PostgreSQL 17 and newer. Removing them makes a dump from a newer server
  replayable on an older one. The cost is that the `\restrict` guard against
  malicious schema objects during restore is gone, so restore into a host you
  control.
- **Passwords are passed through the environment, never on a command line**, so
  they do not appear in `ps` output.
- **File modes and timestamps are preserved**, including `0600` on the key files
  and `0700` on the directory holding the login signing keys. Setuid, setgid and
  sticky bits are dropped on restore.

A dump from PostgreSQL *N* restores into *N* or newer only. The tool checks the
target version before it starts and refuses to restore into an older server rather
than leaving a half-applied schema.

## Troubleshooting

**`relation "..." does not exist` while restoring.** The dump belongs to a
different database, or `--dbname` pointed somewhere else. Check the database name
in `MANIFEST.json` against your DSN.

**`role "..." already exists` while restoring globals.** Expected on a host that
already has the role. Restore with `-restore-globals=false` when the target roles
are already correct.

**Restore fails with `unrecognized configuration parameter`.** The dump came from
a newer major version than the target. Restore into the same version or newer.

**`missing > 0` in the media check.** The database references objects that are not
on disk. Restore `data.tar.zst` again, and if it persists, the source host was
already missing those objects.

**`corrupt > 0`.** A stored object's content does not match its name, which means
a truncated or corrupted copy. Copy the bundle again and re-verify.

**The server starts but clients ask for a login code again.** The RSA key at
`data/server_rsa.pem` is not the one the clients pinned. Restore the original key;
a new key means every client has to re-scan the configuration.

**Nothing listens on the MTProto port.** `TELESRV_ADVERTISE_IP` is the value
clients are told to connect to, and it must match the address the new host is
reachable on. Check it first.
