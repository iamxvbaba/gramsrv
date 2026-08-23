# Production Deployment Guide

This guide covers deploying `telesrv` in production using the prebuilt multi-arch images (`ghcr.io/iamxvbaba/gramsrv:latest`), Docker Compose, and automated secret generation.

---

## 1. Quickstart

You do **not** need to clone the full repository or install Go on your production server. Every
per-service YAML config (`configs/docker/*.yaml`) is baked into the published image
(`ghcr.io/iamxvbaba/gramsrv:latest`), so the only two files you need are `docker-compose.yml` and `.env`.

### Step 1: Download the compose file and env template

```bash
mkdir -p telesrv && cd telesrv
curl -sSL -o docker-compose.yml https://raw.githubusercontent.com/iamxvbaba/gramsrv/v2/docker-compose.yml
curl -sSL -o .env.example https://raw.githubusercontent.com/iamxvbaba/gramsrv/v2/.env.example
cp .env.example .env
```

That's it for files — no `configs/docker/*.yaml`, no `postgres-init/`, no update manifest to fetch.
(If you want to customize a service's YAML config without rebuilding the image, or you want the
optional `main`+`v2` shared-Postgres init script, see [Advanced: overriding baked-in
configs](#advanced-overriding-baked-in-configs) below — neither is required for a normal deploy.)

### Step 2: Fill in `.env`

`.env.example` ships every required secret as `CHANGEME`. **`docker-entrypoint.sh` refuses to start
any service that still has a secret set to `CHANGEME` or left empty** — you'll get a clear
`[FATAL] Required configuration secret '...' is missing or unconfigured` message naming exactly which
variable to fix, instead of a service silently running with a blank password.

Generate real values for every `CHANGEME` (Linux/macOS):
```bash
sed -i \
  -e "s|^POSTGRES_PASSWORD=CHANGEME|POSTGRES_PASSWORD=$(openssl rand -hex 16)|" \
  -e "s|^TELESRV_CORE_EXEC_TOKEN=CHANGEME|TELESRV_CORE_EXEC_TOKEN=$(openssl rand -hex 24)|" \
  -e "s|^TELESRV_FILE_TOKEN=CHANGEME|TELESRV_FILE_TOKEN=$(openssl rand -hex 24)|" \
  -e "s|^TELESRV_EGRESS_ACK_TOKEN=CHANGEME|TELESRV_EGRESS_ACK_TOKEN=$(openssl rand -hex 24)|" \
  -e "s|^TELESRV_GROUPCALL_CONTROL_TOKEN=CHANGEME|TELESRV_GROUPCALL_CONTROL_TOKEN=$(openssl rand -hex 24)|" \
  -e "s|^TELESRV_SFU_CONTROL_TOKEN=CHANGEME|TELESRV_SFU_CONTROL_TOKEN=$(openssl rand -hex 24)|" \
  -e "s|^TELESRV_ADMIN_API_TOKEN=CHANGEME|TELESRV_ADMIN_API_TOKEN=$(openssl rand -hex 24)|" \
  -e "s|^TELESRV_ADMIN_UI_PASSWORD=CHANGEME|TELESRV_ADMIN_UI_PASSWORD=$(openssl rand -base64 16)|" \
  -e "s|^TELESRV_ADMIN_SESSION_KEY=CHANGEME|TELESRV_ADMIN_SESSION_KEY=$(openssl rand -hex 32)|" \
  .env
grep -c CHANGEME .env   # should print 0
```

**Windows PowerShell:**
```powershell
(Get-Content .env) `
  -replace '^POSTGRES_PASSWORD=CHANGEME', "POSTGRES_PASSWORD=$(-join ((1..32) | ForEach-Object { '{0:x}' -f (Get-Random -Max 16) }))" `
  -replace '^TELESRV_CORE_EXEC_TOKEN=CHANGEME', "TELESRV_CORE_EXEC_TOKEN=$(-join ((1..48) | ForEach-Object { '{0:x}' -f (Get-Random -Max 16) }))" `
  -replace '^TELESRV_FILE_TOKEN=CHANGEME', "TELESRV_FILE_TOKEN=$(-join ((1..48) | ForEach-Object { '{0:x}' -f (Get-Random -Max 16) }))" `
  -replace '^TELESRV_EGRESS_ACK_TOKEN=CHANGEME', "TELESRV_EGRESS_ACK_TOKEN=$(-join ((1..48) | ForEach-Object { '{0:x}' -f (Get-Random -Max 16) }))" `
  -replace '^TELESRV_GROUPCALL_CONTROL_TOKEN=CHANGEME', "TELESRV_GROUPCALL_CONTROL_TOKEN=$(-join ((1..48) | ForEach-Object { '{0:x}' -f (Get-Random -Max 16) }))" `
  -replace '^TELESRV_SFU_CONTROL_TOKEN=CHANGEME', "TELESRV_SFU_CONTROL_TOKEN=$(-join ((1..48) | ForEach-Object { '{0:x}' -f (Get-Random -Max 16) }))" `
  -replace '^TELESRV_ADMIN_API_TOKEN=CHANGEME', "TELESRV_ADMIN_API_TOKEN=$(-join ((1..48) | ForEach-Object { '{0:x}' -f (Get-Random -Max 16) }))" `
  -replace '^TELESRV_ADMIN_UI_PASSWORD=CHANGEME', "TELESRV_ADMIN_UI_PASSWORD=$(-join ((1..24) | ForEach-Object { [char](Get-Random -Min 65 -Max 122) }))" `
  -replace '^TELESRV_ADMIN_SESSION_KEY=CHANGEME', "TELESRV_ADMIN_SESSION_KEY=$(-join ((1..64) | ForEach-Object { '{0:x}' -f (Get-Random -Max 16) }))" |
  Set-Content .env -Encoding UTF8
```

Values only need to be high-entropy and unique per deployment — the exact generator doesn't matter, and
nothing you set here is ever overwritten by `docker-entrypoint.sh` on restart (it only ever *validates*
that a real value is present; it never regenerates or replaces a secret you already filled in).

Also set the network settings while you're in `.env`:
- `TELESRV_ADVERTISE_IP`: the public IP or reachable LAN IP of your server.
- `TELESRV_PUBLIC_BASE_URL`: public HTTPS/HTTP root (e.g. `https://telesrv.example.com`).

See [configuration.en.md](configuration.en.md) (Section 1) for the complete network settings reference.

### Step 3: Start the stack

```bash
docker compose up -d && docker compose logs -f
```

Everything else — RSA key generation, Postgres schema migration, per-service startup ordering — happens
automatically inside the containers. `Ctrl-C` stops following logs without stopping the containers.

### Step 4: Verify

```bash
docker compose ps -a
```

Every service should read `Up ... (healthy)` once it settles (a fresh Postgres volume, first-run schema
migration, and language-pack seeding can take a minute or two — `telesrv-core` in particular). `depends_on`
conditions in `docker-compose.yml` mean services start in the right order automatically: `postgres`/`redis`
→ `telesrv-file` → `telesrv-core` → `telesrv-egress`/`telesrv-sfu`/`telesrv-admin` → `telesrv-edge`.

Expected output once settled:
```
NAME                    STATUS              PORTS
telesrv-postgres        Up (healthy)        127.0.0.1:5432->5432/tcp
telesrv-redis           Up (healthy)        127.0.0.1:6399->6379/tcp
telesrv-file            Up (healthy)        (no public ports)
telesrv-core            Up (healthy)        0.0.0.0:2401->2401/tcp, 0.0.0.0:8081->8081/tcp
telesrv-edge            Up (healthy)        0.0.0.0:2398->2398/tcp
telesrv-egress          Up (healthy)        (no public ports)
telesrv-sfu             Up (healthy)        0.0.0.0:12399->12399/udp
telesrv-admin           Up (healthy)        127.0.0.1:2600->2600/tcp
telesrv-update          Up (healthy)        0.0.0.0:2402->2402/tcp
```

If any service isn't healthy, see [Section 5: Troubleshooting](#5-troubleshooting-deployment-issues) and
[monitoring](#monitoring-logs--health) below.

### Monitoring: logs & health

```bash
docker compose ps -a                       # one-shot status + health for every container
docker compose logs -f                     # follow all services interleaved
docker compose logs -f telesrv-edge        # just the client connection gateway
docker compose logs -f telesrv-core        # just the RPC dispatcher
docker compose logs -f telesrv-admin       # just the admin UI server
docker compose logs --since 10m telesrv-core   # only the last 10 minutes
```

`docker compose ps -a` reports each container's health straight from its `healthcheck:` block in
`docker-compose.yml` — every service that listens on a port has one, so `Up` without `(healthy)` (or
`Restarting`) reliably means something is actually wrong, not just slow to log a ready message.

---

## 2. Configuration Files & Environment Variables

### Directory Structure
After following Step 1, your deployment directory should look like:

```
telesrv/
├── docker-compose.yml                     # Service definitions
├── .env.example                           # Template (reference only)
├── .env                                   # Your production configuration
└── data/                                  # Persistent state (created on first run)
    ├── server_rsa.pem                     # MTProto RSA private key (auto-generated)
    ├── server_rsa.pub                     # Public key for clients
    ├── blobs/                             # Uploaded media files
    ├── langpack/                          # Language packs
    ├── official-gifts/                    # Star Gift catalogs
    └── ...
```

`configs/docker/*.yaml` and the optional Postgres init script are **not** part of this directory —
they're baked into the image and only need to exist on disk if you're deliberately overriding them (see
[Advanced: overriding baked-in configs](#advanced-overriding-baked-in-configs)).

### Environment Variables (`.env`)

All service configuration is **environment-driven** via the `.env` file and Docker `env_file` directive in `docker-compose.yml`. The containers read `.env` and substitute placeholders in the YAML configs at startup.

**Key sections:**

- **Database & Cache**: `POSTGRES_PASSWORD`, `TELESRV_REDIS_ADDR`, etc.
- **Security Tokens**: `TELESRV_CORE_EXEC_TOKEN`, `TELESRV_FILE_TOKEN`, etc. (must be identical across services that communicate).
- **Network**: `TELESRV_ADVERTISE_IP`, `TELESRV_PUBLIC_BASE_URL`, port mappings.
- **Feature Flags**: `TELESRV_AI_ENABLED`, `TELESRV_LIVESTREAM_ENABLE`, etc.

For all available options, see [configuration.en.md](configuration.en.md).

### Microservice YAML Configs

Each YAML config file (e.g., `core.yaml`, `edge.yaml`) is a **service-specific blueprint** that gets substituted with environment variables and parsed by the service at startup.

**Example: `core.yaml` excerpt**
```yaml
app:
  id: core
  coreExec:
    addr: ${TELESRV_CORE_EXEC_GRPC_ADDR:-0.0.0.0:2440}
    token: ${TELESRV_CORE_EXEC_TOKEN}
  groupCall:
    addr: ${TELESRV_GROUPCALL_CONTROL_ADDR:-0.0.0.0:2420}
```

The image bakes its own copy of `configs/docker/*.yaml` in at `/app/configs/docker/` (see the
`Dockerfile`), so a plain `docker compose up -d` from Step 1 works with no YAML files on your host at
all. The service reads whichever copy is present with all `${VAR}` placeholders replaced by the
corresponding environment variable from `.env`.

**When to modify YAML configs:**

- **Rarely.** Most production customization is done via `.env`.
- **Advanced use cases**: Custom logging formats, extended health check intervals, performance tuning, or adding experimental features.
- **After modification**: Restart the affected container: `docker compose restart telesrv-core`.

#### Advanced: overriding baked-in configs

`docker-compose-development.yml` (used when you have the full repo checked out, see [Section 7](#7-development-workflow))
bind-mounts `./configs/docker:/app/configs/docker:ro` so local edits apply on restart without rebuilding
the image. The production `docker-compose.yml` intentionally does **not** mount that path, so the
image's baked-in defaults are what actually ship — mounting your own `./configs/docker` over a bare
`docker-compose.yml` deployment (no repo checkout) would need you to first populate that directory with
complete YAML files yourself (e.g. copied out of the image with `docker run --rm --entrypoint cat
ghcr.io/iamxvbaba/gramsrv:latest /app/configs/docker/core.yaml > configs/docker/core.yaml`, repeated per
service), then add the same `volumes:` line back to a `docker-compose.override.yml`.

Similarly, the optional `postgres-init/010_branch_databases.sql` script only matters if you're sharing
one Postgres container between the `main` and `v2` branches (it just pre-creates the `telesrv_main`/
`telesrv_v2` databases those branches use instead of the default `telesrv` database this branch actually
connects to via `TELESRV_POSTGRES_DSN`). A normal single-branch `v2` deployment doesn't need it: fetch it
only if that scenario applies to you —
```bash
mkdir -p postgres-init
curl -sSL -o postgres-init/010_branch_databases.sql https://raw.githubusercontent.com/iamxvbaba/gramsrv/v2/postgres-init/010_branch_databases.sql
```
and mount it the same way `docker-compose-development.yml` does (`./postgres-init:/docker-entrypoint-initdb.d:ro`
on the `postgres` service) before the *first* `docker compose up` (Postgres only runs
`docker-entrypoint-initdb.d` scripts against a brand-new, empty data volume).

### Identity migration

The normal service startup validates identity files but does not perform
destructive migrations. To convert an older PKCS#8 RSA key explicitly, run:

```bash
docker run --rm \
   -v "$PWD/data:/data" \
   telesrv:latest \
   telesrv-key-migrate \
   -in /data/server_rsa.pem \
   -out /data/server_rsa.pem
```

The command creates `/data/server_rsa.pem.before-pkcs1` before replacing the
key. Back up the data directory first and restart the MTProto services after
the migration. Service startup validates the key but does not migrate it
automatically.

---

## 3. Network Security Architecture & UFW Safety

Docker directly configures `iptables` forwarding rules, which bypasses standard `UFW` rules on Linux hosts. Telesrv implements defense-in-depth isolation:

### Private Docker Network (`telesrv-net`)

All microservices communicate over a private bridge network. Internal gRPC endpoints have **no published host ports**:

| Service | Internal Port | Purpose | Public? |
|---|---|---|---|
| telesrv-core | `2440` (gRPC) | CoreExec RPC | ❌ Internal only |
| telesrv-file | `2520` (gRPC) | FileData RPC | ❌ Internal only |
| telesrv-egress | `2510` (gRPC) | Egress ACK writeback | ❌ Internal only |
| telesrv-sfu | `2450` (gRPC) | SFU control | ❌ Internal only |
| telesrv-core | `2420` (HTTP) | GroupCall control | ❌ Internal only |
| telesrv-admin | `2601` (gRPC) | Admin API | ❌ Internal only |
| postgres | `5432` | Database | ❌ `127.0.0.1` only |
| redis | `6379` | Cache | ❌ `127.0.0.1` only |

### Client-Facing Public Ports

These ports are published to the host per `.env` settings:

| Service | Port | Protocol | Default Bind | Purpose | Setting |
|---|---|---|---|---|---|
| telesrv-edge | `2398` | TCP | `0.0.0.0` | MTProto & WebSocket | `TELESRV_EDGE_PORT` |
| telesrv-core | `2401` | TCP | `0.0.0.0` | Public links & login | `TELESRV_PUBLIC_LINK_PORT` |
| telesrv-update | `2402` | TCP | `0.0.0.0` | Auto-update CDN | `TELESRV_UPDATE_PORT` |
| telesrv-admin | `2600` | TCP | `127.0.0.1` | Admin web UI | `TELESRV_ADMIN_UI_PORT` |
| telesrv-core | `8081` | TCP | `0.0.0.0` | Bot API HTTP | `TELESRV_BOT_API_PORT` |
| telesrv-core | `12500` | UDP | `0.0.0.0` | TURN Relay | `TELESRV_TURN_RELAY_MIN_PORT` |
| telesrv-core | `12999` | UDP | `0.0.0.0` | TURN Relay | `TELESRV_TURN_RELAY_MAX_PORT` |
| telesrv-sfu | `12399` | UDP | `0.0.0.0` | WebRTC media | `TELESRV_SFU_UDP_PORT` |

**Firewall Strategy:**

- Open only required public ports: `2398/tcp`, `12399/udp`, `12500:12999/udp`, `2401/tcp`, `8081/tcp`, `2402/tcp` (optional).
- Keep admin UI (`2600`) bound to `127.0.0.1` and access via SSH tunnel: `ssh -L 2600:127.0.0.1:2600 user@server`.
- Database and cache ports (`5432`, `6379`) bound to `127.0.0.1`; access for maintenance via SSH tunnel if needed.

### UFW Configuration (Example)

```bash
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow 22/tcp                    # SSH
sudo ufw allow 2398/tcp                  # telesrv-edge (MTProto)
sudo ufw allow 2400/tcp                  # Livestream
sudo ufw allow 12399:12400/udp           # telesrv-sfu (WebRTC)
sudo ufw allow 12500:12999/udp           # TURN Relay
sudo ufw enable
```

---

## 4. Host `./data` Directory Management

All stateful data is mounted directly from `./data` on your host to `/app/data` inside containers. This includes uploaded media, cache, and dynamically generated artifacts.

### Automatic Initialization

On first startup, `docker-entrypoint.sh` in each container:
1. Generates the MTProto RSA key pair (`server_rsa.pem`, `server_rsa.pub`).
2. Creates required subdirectories.
3. Validates secret tokens are set.

### Public RSA Key for Clients

After first startup, retrieve the public key:
```bash
cat data/server_rsa.pub
```

Share this public key and your `TELESRV_ADVERTISE_IP:2398` (DC `2`) with patched Telegram clients. The client uses these to establish MTProto connections.

### Custom Seed Assets

You can pre-populate seed data by placing files into `./data/` before or after startup:

| Directory | Purpose | Note |
|---|---|---|
| `./data/official-gifts/` | Star Gift catalogs (JSON) | Loaded on container startup |
| `./data/sticker-seed/` | Custom sticker packs | Imported by `stickerfetch` service or API |
| `./data/premium-promo/` | Premium feature videos/images | Served by web UI |
| `./data/langpack/` | Language `.strings` files | Synced to clients |
| `./data/gifs/` | GIF/animation seeds | For GIF search/trending |
| `./data/maptiles/` | Map tile cache | Mapbox-compatible MBTiles |

**Adding assets after startup:**
```bash
# Example: Add custom sticker pack
curl -o data/sticker-seed/my-pack.json https://example.com/pack.json
# Services will pick up new files on next sync/fetch cycle
```

### Data Persistence & Backups

The Docker volumes are defined as:
```yaml
volumes:
  pgdata:      # PostgreSQL data directory
  redisdata:   # Redis persistence (AOF)
```

These are backed by the Docker engine and typically stored in `/var/lib/docker/volumes/`. For production:

**Backup PostgreSQL:**
```bash
docker compose exec postgres pg_dump -U telesrv telesrv | gzip > backup-$(date +%Y%m%d).sql.gz
```

**Backup Redis:**
```bash
docker compose exec redis redis-cli BGSAVE
# Copy /var/lib/docker/volumes/telesrv_redisdata/_data/dump.rdb
```

**Backup media files:**
```bash
tar -czf backup-data-$(date +%Y%m%d).tar.gz data/
```

---

## 5. Troubleshooting Deployment Issues

### Services fail to start: "config file not found"

**Cause:** `configs/docker/*.yaml` is baked into the image (see [Section 2](#microservice-yaml-configs)),
so this almost always means either an old/custom image that predates the bake-in, or a
`docker-compose.override.yml` (or an inherited `docker-compose-development.yml` habit) that mounts a
`./configs/docker` host directory over the image's copy without that directory actually having the
files in it.

**Solution:**
```bash
# Confirm the image actually has the configs baked in
docker compose run --rm --entrypoint ls telesrv-core /app/configs/docker/
# Should list: admin.yaml core.yaml edge.yaml egress.yaml file.yaml sfu.yaml ton.yaml

# If you intentionally have a configs/docker bind mount (see "Advanced: overriding
# baked-in configs" in Section 2), make sure it's fully populated, not partial:
ls -la configs/docker/

# Pull the latest image if yours predates the bake-in, then recreate:
docker compose pull
docker compose up -d --force-recreate
```

### Services fail with "invalid token" or "authentication failed"

**Cause:** Token mismatch between services (e.g., `TELESRV_CORE_EXEC_TOKEN` differs between telesrv-core and telesrv-edge).

**Solution:**
1. Verify tokens are identical in `.env`:
   ```bash
   grep TELESRV_.*_TOKEN .env | sort
   ```
2. Regenerate if necessary:
   ```bash
   CORE_TOKEN=$(openssl rand -hex 24)
   sed -i "s/^TELESRV_CORE_EXEC_TOKEN=.*/TELESRV_CORE_EXEC_TOKEN=${CORE_TOKEN}/" .env
   ```
3. Restart all services:
   ```bash
   docker compose restart
   ```

### PostgreSQL connection refused

**Cause:** Incorrect DSN or PostgreSQL not ready.

**Solution:**
1. Check `.env` has the correct DSN:
   ```bash
   grep TELESRV_POSTGRES_DSN .env
   # Should be: postgres://telesrv:PASSWORD@postgres:5432/telesrv?sslmode=disable
   ```
2. Wait for PostgreSQL to be healthy:
   ```bash
   docker compose ps postgres
   # Should show "Up (healthy)"
   ```
3. Check logs:
   ```bash
   docker compose logs postgres
   ```

### Admin UI not accessible

**Cause:** Port binding or container not running.

**Solution:**
```bash
# Check container is running
docker compose ps telesrv-admin

# Verify port binding
docker compose port telesrv-admin 2600
# Should output: 127.0.0.1:2600

# Access via SSH tunnel
ssh -L 2600:127.0.0.1:2600 user@server
# Then open http://localhost:2600 in your browser

# Check logs
docker compose logs telesrv-admin
```

### High CPU/Memory usage

**Cause:** Service misconfiguration, memory leaks, or insufficient resources.

**Solution:**
1. Monitor resource usage:
   ```bash
   docker stats telesrv-core telesrv-edge telesrv-sfu
   ```
2. Check service logs for errors:
   ```bash
   docker compose logs telesrv-core | tail -100
   ```
3. Review `.env` settings:
   - `TELESRV_OUTBOX_WORKERS`: Reduce if CPU-bound.
   - `TELESRV_POSTGRES_MAX_CONNS`: Reduce if connection pool is exhausted.
   - `TELESRV_SFU_INSTANCE_MAX_ACTIVE_CALLS`: Limit concurrent SFU calls.
4. Increase host resources (vCPU, RAM) if baseline is insufficient.

---

## 6. Scaling & Multi-Server Deployments

For production clusters with multiple servers, consider:

### Shared PostgreSQL/Redis

Use managed services (AWS RDS, Google Cloud SQL, Redis Enterprise) or a separate HA cluster:
- Update `TELESRV_POSTGRES_DSN` to point to the remote database.
- Update `TELESRV_REDIS_ADDR` to point to the remote cache.

### Multiple Edge/SFU Instances

Deploy `telesrv-edge` and `telesrv-sfu` on separate servers for horizontal scalability:
- Core and Egress can run on a single shared server (shared PostgreSQL/Redis).
- Update `TELESRV_CORE_EXEC_GRPC_TARGETS`, `TELESRV_SFU_CONTROL_GRPC_TARGETS` to target the shared instances.

### Load Balancing

- Place a reverse proxy (nginx, HAProxy) in front of `telesrv-edge:2398` for TCP load balancing.
- For `telesrv-sfu:12399/udp`, use DNS round-robin or application-level client steering via `TELESRV_ADVERTISE_IP`.

---

## 7. Development Workflow

To build and test images locally from source:

```bash
git clone https://github.com/iamxvbaba/gramsrv.git
cd gramsrv

# Build multi-arch images for local testing
docker compose -f docker-compose-development.yml build

# Start the stack with locally built images
docker compose -f docker-compose-development.yml up -d

# Watch logs
docker compose -f docker-compose-development.yml logs -f
```

For more details, see [local-setup.md](local-setup.md).

---

## References

- [configuration.en.md](configuration.en.md) — Complete reference of all `.env` settings.
- [README.md](../README.md) — Overview and architecture.
- [local-setup.md](local-setup.md) — Development setup guide.