# Docker Deployment

> Status: operation.
> Scope: complete single-host Docker Compose topology for `file/core/egress/sfu/edge`.
> Chinese version: [`docker-deployment.md`](docker-deployment.md).

This deployment is not a compatibility mode for the old monolith. By default it
starts PostgreSQL, Redis, a one-shot schema migrator, and five long-running
application roles:

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

The Edge readiness probe checks the local MTProto listener, Redis, and the
authenticated gRPC health of CoreExec, FileData, and Egress ACK. The probes for
Core, File, Egress, and SFU also authenticate their direct PostgreSQL, Redis, or
service dependencies. If a required dependency is unavailable, the affected
container becomes `unhealthy`, and business RPCs do not fall back to in-process
execution. Docker Compose does not restart a container solely because it is
`unhealthy`; a production deployment still needs alerting and an operator or
external watchdog to perform recovery. FileData is mandatory. Do not remove the
File service or restore a local file fallback in Core or Edge.

## 1. Prerequisites

- Docker Engine 24+ or Docker Desktop, with Compose v2.24+.
- At least 4 GiB of available memory. Use 8 GiB for local image builds and keep
  at least 2 GiB of free disk for the Core and File transcoding volumes.
- An IPv4 or IPv6 address reachable by the patched client. The embedded TURN
  listener is currently IPv4, so TURN deployments need a client-reachable IPv4.
- Network access to `ghcr.io` and Docker Hub for the default startup. Go module
  and Alpine package access is needed only for a local `-Build`.

The default `localfs` blob backend is suitable for a single-host deployment.
Multi-host operation, high availability, TLS/mTLS, external PostgreSQL/Redis,
S3, and load balancing are the next deployment tier. They cannot be achieved by
copying this Compose file or running `--scale`: fixed host ports, localfs, SFU
media addresses, and stable instance IDs all require orchestration-level design.

## 2. Generate the Deployment Environment

On Windows PowerShell 5.1+ or PowerShell 7+, run the generator. It uses the
system cryptographic random source, restricts the file ACL, and refuses to
overwrite an existing `.env`:

```powershell
.\scripts\new-docker-env.ps1 `
  -AdvertiseIP 192.0.2.10 `
  -PublicBaseURL https://chat.example.com `
  -PublicWebBaseURL https://web.chat.example.com
```

For local development, only `-AdvertiseIP 127.0.0.1` is required. The generator
also restricts published ports to loopback and creates a local HTTP public URL.
A non-loopback address requires explicit self-hosted public URLs; the generator
does not silently retain project-owned domains.

On Linux or macOS, copy the template, then use `openssl rand -hex 32` to create
independent database, Redis, and internal service secrets:

```bash
cp deploy/docker/.env.example deploy/docker/.env
chmod 600 deploy/docker/.env
${EDITOR:-vi} deploy/docker/.env
```

Check all of the following:

- No `.env` assignment may retain a `CHANGEME` placeholder. The container
  entrypoint fails fast when one remains.
- `TELESRV_ADVERTISE_IP` must be an IP address reachable by clients, not a
  Docker service name or DNS name.
- IPv4 deployments enable TURN by default. `TELESRV_TURN_ADVERTISE_IP` must be
  a client-reachable IPv4 and `TELESRV_TURN_SECRET` must be an independent
  random value. Forward the TURN listener and relay range without translating
  `12500-12999/udp` to a different external range. The generator explicitly
  disables embedded TURN for IPv6-only deployments.
- RTMP is enabled by default. `TELESRV_LIVESTREAM_RTMP_URL` must be an
  OBS/publisher-reachable `rtmp://<host>:2400/live` URL.
- Development login codes require
  `TELESRV_ALLOW_INSECURE_DEVELOPMENT_AUTH=true`. The generator enables it
  automatically only for loopback. For temporary LAN testing, pass
  `-AllowInsecureDevelopmentAuth` explicitly. An Internet-facing deployment
  must set `TELESRV_PHONE_CODE_DELIVERY_PROVIDER=webhook`,
  `TELESRV_OTP_WEBHOOK_URL`, and an independent
  `TELESRV_OTP_WEBHOOK_SECRET`; see [`otp-delivery.md`](otp-delivery.md).
- The password inside `TELESRV_POSTGRES_DSN` must match `POSTGRES_PASSWORD`.
  URL-encode special characters when editing it manually.
- The five internal tokens and TURN secret must be independent and must not
  reuse a database, Redis, or administrator password.

The generator has no force-overwrite mode. Regenerating `.env` would make an
existing PostgreSQL password, Redis password, and container tokens inconsistent.
Rotate credentials one at a time with a coordinated restart; do not treat
initialization as a repeatable reset command.

Role YAML expands only `${NAME}` placeholders that actually appear in the file.
Ordinary process environment variables do not override absent YAML fields.
Docker configuration is therefore defined by `deploy/docker/config/*.yaml` and
the role-specific Compose `environment` allowlists, not by an unlimited `.env`.
Each role image contains only its own YAML, and the default Compose topology does
not bind-mount configuration from the host. This keeps image and configuration
revisions aligned. Rebuild all affected images after changing tracked YAML. If a
deployment-specific override is required, mount one role file through a separate
Compose override and include that override in encrypted backup and same-version
rollback procedures.

## 3. Validate, Pull, and Start

For the first Windows experience, run the following from the repository root.
When `.env` is absent, the script initializes it with loopback defaults,
validates the configuration, pulls the `v2` images from GHCR, starts the stack,
and waits for readiness:

```powershell
.\scripts\start-docker.ps1
```

When startup completes, the script prints the default development login code
`12345`, TURN/STUN endpoint, relay range, and RTMP ingest URL. Docker Desktop
may need several minutes to create the complete TURN UDP range on first startup;
wait until the script reports that the stack is ready. A temporary LAN deployment
can also be initialized with one command. Replace the example address with the
LAN address of the Docker host:

```powershell
.\scripts\start-docker.ps1 -AdvertiseIP 192.168.1.20 -PublicBaseURL http://192.168.1.20:2401 -PublicWebBaseURL http://192.168.1.20:2401 -AllowInsecureDevelopmentAuth
```

With a plain-HTTP LAN address, Core logs a warning and disables Star gift and
revenue withdrawal links (they are bearer URLs, so only HTTPS is accepted
outside loopback); everything else keeps working. Use an HTTPS public base URL
when withdrawals are needed.

For a real Internet-facing deployment, first run `new-docker-env.ps1` by itself,
configure the phone-code webhook described in Section 2, and then run
`start-docker.ps1` without address parameters. If `.env` already exists, the
startup script always reuses its credentials. New address or development-auth
parameters only produce a warning and never overwrite deployment identity. Edit
`.env` explicitly before restarting when an address must change.

The default image prefix is `ghcr.io/iamxvbaba/gramsrv`. It contains seven
packages: `migrate`, `file`, `core`, `egress`, `sfu`, `admin`, and `edge`. The convenient
channel tag is `v2`; every publish also produces an immutable `sha-<commit>`
tag. Automatic publication on `v2` pushes is currently paused; maintainers run
the `Publish v2 container images` workflow manually from GitHub Actions. A newly
created GHCR package is private by default, so maintainers must set each of the
seven packages to Public in GitHub Packages before anonymous one-click pulls
work. While they remain private, run `docker login ghcr.io` first.

To validate modified source with locally built images, opt in explicitly. The
Compose Edge build target is `edge-test`, matching the public v2 test channel:

```powershell
.\scripts\start-docker.ps1 -Build
```

To restart that locally built stack later, add `-NoPull` (`--no-pull` for
`start-docker.sh`): the script then never contacts the registry and fails on a
missing image instead of silently replacing the local tag with a published
one. Combined with `-Build`, base images are not refreshed.

To run the phases separately, or to start on Linux/macOS:

```bash
docker compose --project-directory deploy/docker --env-file deploy/docker/.env -f deploy/docker/compose.yaml config --quiet
docker compose --project-directory deploy/docker --env-file deploy/docker/.env -f deploy/docker/compose.yaml pull
docker compose --project-directory deploy/docker --env-file deploy/docker/.env -f deploy/docker/compose.yaml up -d --no-build --wait
```

Alternatively, enter `deploy/docker` first:

```powershell
Set-Location deploy\docker
docker compose config --quiet
docker compose pull
docker compose up -d --no-build --wait
```

For an empty database, PostgreSQL and Redis start in parallel. The one-shot
`migrate` job runs after PostgreSQL is healthy. After migration succeeds, File
starts, and Egress starts once Redis is also healthy. Core starts after File is
healthy; SFU and Edge can satisfy their own start conditions only after Core is
healthy. Edge waits for Redis, Core, File, and Egress, but intentionally does not
wait for SFU so that media availability does not block the messaging entrypoint.

This sequence prevents multiple long-running processes from racing migrations
that include `CREATE INDEX CONCURRENTLY`. File, Core, and Egress still confirm
the same migrations idempotently as a second gate. `up --wait` requires every
long-running service to be healthy and the migrator to finish successfully.
Each application role uses its own Dockerfile target. An image contains only its
role binary, the health probe, its YAML, selected seeds, and required runtime
libraries. Base images are pinned by digest. Application containers run as UID `10001` with a
read-only root filesystem, all capabilities dropped, a 512 PID limit, and
`no-new-privileges`.

Inspect status and ready logs:

```powershell
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml ps --all
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml logs --tail 100 file core egress sfu edge
```

Expected state: `migrate` is `Exited (0)`, all five long-running applications
are `healthy`, and the logs include:

- `telesrv-migrate: schema ready ...`
- `telesrv file ready`
- `telesrv core role ready`
- `turn listening`
- `live stream rtmp ingest listening`
- `telesrv egress ready`
- SFU standalone ready/heartbeat messages
- `telesrv edge ready`

## 4. Networks and Ports

| Host port | Default binding | Purpose |
|---|---|---|
| `2398/tcp` | `127.0.0.1` for zero-argument setup; `0.0.0.0`/`::` for non-loopback initialization | MTProto Edge client entrypoint |
| `12399/udp` | `127.0.0.1` for zero-argument setup; `0.0.0.0`/`::` for non-loopback initialization | SFU media |
| `12400/udp` | `127.0.0.1` for zero-argument setup; `0.0.0.0` for non-loopback IPv4 initialization | TURN/STUN listener |
| `12500-12999/udp` | Same binding as `12400/udp`; 1:1 host/container ports | TURN relay allocations |
| `2400/tcp` | `127.0.0.1` for zero-argument setup; `0.0.0.0`/`::` for non-loopback initialization | RTMP ingest for OBS or another publisher |
| `2401/tcp` | `127.0.0.1` by default; the advertised IP for a direct HTTP URL | Public-link HTTP or host nginx/Caddy reverse proxy |

PostgreSQL, Redis, CoreExec, FileData, Egress ACK, SFU control, and debug/pprof
are not published to the host. `data` and `control` are internal Docker
networks. Internal gRPC currently uses bearer tokens over plaintext, so the
Docker daemon, host root, and containers on the same control network are part of
the trust boundary. A cross-host deployment needs a TLS/mTLS overlay and cannot
reuse these probes unchanged.

IPv4 Docker deployments enable embedded TURN and RTMP by default. The router/NAT
and Windows/Linux firewall must both allow `12400/udp`, the complete
`12500-12999/udp` range, and `2400/tcp`. The TURN relay range requires 1:1
forwarding and `TELESRV_TURN_ADVERTISE_IP` must be the address clients can reach;
changing only `.env` or only the firewall is not a complete forwarding setup.
The v2 `compose.yaml` already configures the listeners and published ports from
`.env`; no manual Compose edit is needed. Run the startup script again after
changing these variables so Compose recreates Core when required.

On a host with multiple network interfaces, set `TELESRV_PUBLIC_BIND_IP` and
the IPv4 TURN `TELESRV_TURN_BIND_IP` to specific addresses. When the generator
sees a PublicBaseURL in the form
`http://<AdvertiseIP>:...`, it binds port 2401 directly to that address for a
simple LAN experience. For an Internet-facing reverse proxy, keep 2401 bound to
host loopback and terminate TLS in nginx or Caddy. Never publish pprof or
internal gRPC directly.

Admin is enabled by default. Its login password is stored in the owner-only
`deploy/docker/.env`; it mounts `file_data` read-only for local blob/spool disk
capacity and sends every administrative write through the Core Admin API. The
TON worker is not enabled by the default Compose topology; add explicit
configuration and validation when needed.

The HTTP Bot API gateway is off by default. Set
`TELESRV_BOT_API_ADDR=0.0.0.0:8088` in `.env` to enable it (the container port
stays 8088); the one-click scripts then also load `compose.bot-api.yaml`, which
publishes it on `TELESRV_BOT_API_BIND_IP:TELESRV_BOT_API_PORT` (default
`127.0.0.1:8088`). When running Compose by hand, append
`-f deploy/docker/compose.bot-api.yaml` after `compose.yaml`. The Bot API is
plain HTTP: for Internet access keep it on loopback behind a TLS reverse proxy.

## 5. Data and RSA Identity

| Volume | Contents | Consequence if lost |
|---|---|---|
| `postgres_data` | All durable business, authorization, update facts, and schema | Service data is lost |
| `redis_data` | Location/control/cache state and AOF | Online state is rebuilt; active sessions are affected |
| `file_data` | localfs blobs, upload parts, and map cache | Media cannot be recovered |
| `file_tmp` | File GIF/video transcoding temporary files | Safe to recreate; do not back up |
| `core_state` | Optional seeds, map cache, and OIDC key files | Enabled features fail or identity changes |
| `core_tmp` | Temporary files for ffmpeg/ffprobe and RTMP segments | Safe to recreate; do not back up |
| `edge_state` | MTProto RSA private and public keys | Patched clients can no longer complete the handshake |

### 5.1 Where One-Click Startup Stores Data

A fresh deployment does not require the user to create or populate a host
`data/` directory. Compose automatically creates the named volumes above and
writes the database, media, and RSA identity into them. With the default
`COMPOSE_PROJECT_NAME=telesrv`, the actual volume names look like
`telesrv_postgres_data`, `telesrv_file_data`, and `telesrv_edge_state`. Changing
the project name also changes the prefix.

List the current deployment volumes and inspect their Docker-managed host
locations:

```powershell
docker volume ls --filter label=com.docker.compose.project=telesrv
docker volume inspect telesrv_postgres_data
docker volume inspect telesrv_file_data
docker volume inspect telesrv_edge_state
```

Docker Engine on Linux usually stores named volumes under
`/var/lib/docker/volumes/`. Docker Desktop on Windows and macOS stores them in
the Docker Desktop virtual disk. Do not edit those internal directories
directly. Use PostgreSQL tools, the File container, `docker compose cp`, or the
backup and restore procedure in Section 6.

The repository-root `data/` directory serves host-native development and image
build inputs:

- `internal/seed/catalog` and `internal/seed/appearance` are compiled into Core
  through `go:embed`; `data/langpack/` is copied to
  `/usr/share/telesrv/langpack/`. They already ship in the GHCR Core image, so
  users do not prepare or mount `data/`. The image manifest is
  `/usr/share/telesrv/seed-manifest.json`.
- The hundreds of megabytes of sticker/reaction exports, official gifts, and
  Premium promo media are not in the default image. They remain optional,
  feature-specific imports so the one-click download stays reasonable.
- The current test pair is fixed at
  `deploy/docker/assets/test-server-rsa.pem.b64` and
  `deploy/docker/assets/test-server-rsa.pub`. It is copied only into the
  `edge-test` target and the GHCR `edge:v2` image.
- Database, media blobs, and Redis state are not mapped into one host `data/`
  directory. They are separated into role-owned named volumes. This retains data
  across container recreation and avoids Windows/Linux bind-mount permission
  differences during one-click startup.

Do not copy empty directories or sample files into the volumes for a fresh
deployment. An existing installation also cannot be migrated by overwriting the
whole `data/` directory. Restore the database through dump/restore, media into
`file_data`, and the RSA private key into `edge_state`, as described in Section 6.

### 5.2 RSA Key Pair After One-Click Startup

The default `.env` uses `TELESRV_RSA_IDENTITY_MODE=test`. On an empty
`edge_state` volume, the Edge entrypoint decodes the image's published test
private-key fixture once and exports the matching PKCS#1 public key. This fixed
pair is the current test-stage identity, and its private key is deliberately
public test data.

| File | Repository/image path | Runtime volume path | Purpose |
|---|---|---|---|
| RSA private key | `deploy/docker/assets/test-server-rsa.pem.b64` / `/usr/share/telesrv/keys/test-server-rsa.pem.b64` | `/var/lib/telesrv-edge/server_rsa.pem` | Base64 is only for publishing; runtime restores the original PKCS#1 key; do not put it in a client |
| RSA public key | `deploy/docker/assets/test-server-rsa.pub` / `/usr/share/telesrv/keys/test-server-rsa.pub` | `/var/lib/telesrv-edge/server_rsa.pub` | Embed in patched TDesktop or Android clients |

Clients using the default `edge:v2` can therefore read
`deploy/docker/assets/test-server-rsa.pub` directly without waiting for the
server to start. To confirm the identity active in the volume, export it to
`data/docker-server_rsa.pub`, which is excluded from Git and the Docker build
context, and compare the files:

```powershell
if (Test-Path data\docker-server_rsa.pub) { throw "data/docker-server_rsa.pub already exists; compare it before replacing" }
docker compose --project-directory deploy/docker --env-file deploy/docker/.env -f deploy/docker/compose.yaml cp edge:/var/lib/telesrv-edge/server_rsa.pub data/docker-server_rsa.pub
```

Linux and macOS can use the same `docker compose ... cp` command. The entrypoint
never overwrites an existing `edge_state/server_rsa.pem`; recreating the
container therefore preserves its identity. If an old volume already contains
a different key, that identity stays active and Edge logs the difference. In
the default test mode, deleting the volume installs the same published test pair
again, so the test-client fingerprint stays stable.

For a production deployment, set `TELESRV_RSA_IDENTITY_MODE=generated` and
`TELESRV_EDGE_BUILD_TARGET=edge` in a new deployment, then run
`.\scripts\start-docker.ps1 -Build` to create a private-key-free Edge image; or
provision a custom PKCS#1 key into `edge_state` before startup. Back up that production key
and never commit it. Multiple Edge instances use the same production identity.
The published test private key in this section must not be treated as a secret.

`file_tmp` and `core_tmp` use host disk through named volumes instead of the
64 MiB `/tmp` tmpfs. Normal requests remove temporary files. A process crash can
leave `telesrv-*` files behind; monitor volume capacity and remove orphaned files
only while the corresponding service is stopped.

## 6. Backup and Restore

Stop business writers before taking a consistent backup. The following
PowerShell workflow writes binary archives directly instead of passing them
through Windows PowerShell 5.1 text redirection:

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

Also back up Docker YAML overrides, explicitly enabled OIDC/seeds in
`core_state`, and the deployed image tags or digests. The backup directory is
excluded from Git and the build context, but it still needs owner-only ACLs,
off-host encryption, and checksums. With the S3 backend, do not treat
`file_data` as the blob source of truth. Use object-store versioning or
replication aligned with the database snapshot. Run `up -d --wait` after backup.

Always rehearse recovery in an isolated project with empty volumes. Restore the
backed-up `.env`, then run:

```powershell
$backupDir = (Resolve-Path deploy\docker\backups\manual).Path
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml up -d --wait postgres redis
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml cp "$backupDir/telesrv.dump" postgres:/tmp/telesrv.dump
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml exec -T postgres sh -ec 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists --no-owner /tmp/telesrv.dump'
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml run --rm --no-deps --user 0:0 --volume "${backupDir}:/backup:ro" file tar -C /var/lib/telesrv-file -xzf /backup/file_data.tar.gz
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml run --rm --no-deps --user 0:0 --volume "${backupDir}:/backup:ro" edge sh -ec 'install -m 0600 -o 10001 -g 10001 /backup/server_rsa.pem /var/lib/telesrv-edge/server_rsa.pem'
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml up -d --wait
```

Restore separate backups for any explicitly enabled login keys or seeds in
`core_state`. Redis online state can be rebuilt from an empty volume, but clients
will reconnect. Finally verify the RSA fingerprint, schema, media range reads,
and real-client login. A backup that restores the database but omits blobs or
the RSA identity is not usable.

## 7. Upgrade and Rollback

All five roles must come from the same source revision. Back up first, stop old
writers, then build or pull every role:

```powershell
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml stop edge sfu egress core file
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml pull
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml run --rm --no-deps migrate
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml up -d --wait file
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml up -d --wait core egress
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml up -d --wait sfu edge
```

The standalone migrator applies the schema for the current revision first. File,
Core, and Egress then confirm the same migrations during startup. This does not
mean that every old binary is backward-compatible with the new schema. If a
migration is incompatible, rollback must restore the pre-upgrade database and
matching file/RSA state; changing only the image tag is insufficient.

Use `docker compose stop` or `docker compose down` for ordinary shutdown.
`docker compose down -v` deletes the database, media, and RSA identity and is
allowed only when intentionally resetting a test environment.

## 8. localfs and S3

The default is `TELESRV_BLOB_BACKEND=localfs`, with File as the sole localfs
data-plane owner. To use S3:

1. Fill in endpoint, region, bucket, access key, secret, TLS, and path-style
   settings in `.env`.
2. Set `TELESRV_BLOB_BACKEND=s3`.
3. In an isolated environment, validate bucket permissions, range reads,
   multipart/upload-part garbage collection, and capacity alerts.
4. Follow the offline migration constraints in the
   [configuration reference](configuration.en.md). Existing data is not moved
   by changing an environment variable, and reads or writes do not fall back to
   the other backend.

Compose does not bundle MinIO by default, so a development object store is not
mistaken for production-grade highly available S3. Use a separate override for
a self-contained MinIO test environment, including bucket initialization, a
persistent volume, and health checks.

## 9. Minimum Acceptance Matrix

Before handing off a deployment, run at least:

```powershell
docker compose --project-directory deploy/docker --env-file deploy/docker/.env -f deploy/docker/compose.yaml config --quiet
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml up -d --no-build --wait
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml exec edge id -u
docker compose --project-directory deploy/docker -f deploy/docker/compose.yaml ps
```

- `id -u` is `10001`; writes to the root filesystem fail, while declared data
  volumes remain writable.
- The host exposes only the expected `2398/tcp`, `12399/udp`, `12400/udp`,
  `12500-12999/udp`, `2400/tcp`, and the `2401/tcp` binding selected in `.env`.
- Stopping Core, File, or Egress makes Edge `unhealthy`; affected business
  requests fail closed and do not execute in-process. Health recovers when the
  backend returns.
- After `down` without `-v` followed by `up`, the RSA fingerprint, schema,
  accounts, and media blobs are unchanged.
- Cover online push/ACK, offline difference, Edge reconnect, Egress
  crash/reclaim, and SFU UDP media.

## 10. Troubleshooting

- **Compose reports a required variable while parsing:** `.env` has a missing
  value, or the command did not use the correct `--project-directory` and
  `--env-file`.
- **The entrypoint reports a placeholder:** a `CHANGEME` value or empty internal
  token remains.
- **File is unhealthy:** inspect PostgreSQL migration state, backend kind, free
  disk space, and `file_data` permissions.
- **Core is unhealthy:** inspect FileData health/GetInfo, PostgreSQL, Redis, and
  the CoreExec, group-control, and public-link listeners. SFU is an independent
  owner, not a local Core fallback.
- **Edge is unhealthy:** inspect the local 2398 listener, then authenticated
  health for CoreExec, FileData, and Egress ACK.
- **A group call joins but media fails:** verify `TELESRV_ADVERTISE_IP` and
  NAT/firewall forwarding for `12399/udp`.
- **A private call cannot use relay:** confirm Core logs `turn listening`, then
  verify `TELESRV_TURN_ADVERTISE_IP` and 1:1 NAT/firewall forwarding for
  `12400/udp` and the complete `12500-12999/udp` range.
- **OBS cannot connect to RTMP:** confirm Core logs
  `live stream rtmp ingest listening`, then verify
  `TELESRV_LIVESTREAM_RTMP_URL` and `2400/tcp` forwarding. The client live-stream
  settings page generates the stream key.
- **The client RSA fingerprint does not match:** export the public key from the
  current `edge_state`; an old volume may preserve a different identity. A fresh
  default test volume exactly matches `deploy/docker/assets/test-server-rsa.pub`.
