#!/usr/bin/env bash
# One-command setup for a working gramsrv/telesrv stack:
#
#   1. Go toolchain (uses an existing one, or installs the latest stable release)
#   2. PostgreSQL (existing server, a fresh local instance, or docker compose deps)
#   3. Redis (existing, freshly started local redis-server, or docker compose redis)
#   4. Build bin/telesrv and bin/subscribebackfill
#   5. .env from .env.example with TELESRV_POSTGRES_DSN / TELESRV_REDIS_ADDR filled in
#   6. Start the server (it applies its own schema migrations, including the
#      phone-identity data migration between v181 and v182 -- do NOT apply the
#      migrations with raw psql, that step would be skipped)
#   7. Create 3 demo channels (News @news, Updates @updates, Announcements
#      @announcements) when the database has no channels yet
#   8. Wire TELESRV_AUTOSUBSCRIBE_CHANNELS and backfill every existing account
#      (scripts/subscribe.sh), then restart the server so new registrations
#      auto-join from the first one on
#
#   ./setup.sh                     # full setup, server left running on :2398
#   ./setup.sh --no-start          # build and configure only
#   ./setup.sh --channels @a,@b    # explicit auto-subscribe channels
#   ./setup.sh --no-demo-channels  # do not create demo channels
#   ./setup.sh --help              # all flags
#
# Idempotent: safe to re-run; existing state is detected and kept.
set -euo pipefail

repo_root=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
log_file="$repo_root/data/telesrv.log"
pid_file="$repo_root/data/telesrv.pid"

# ---- defaults (mirroring internal/config/config.go) -------------------------
DSN="postgres://telesrv:telesrv@127.0.0.1:5432/telesrv_main?sslmode=disable"
PGHOST="127.0.0.1"
PGPORT="5432"
REDIS_ADDR="127.0.0.1:6399"
LISTEN_PORT="2398"
GO_MIN_MAJOR=1           # GOTOOLCHAIN=auto fetches go1.26.11 (go.mod) on demand
GO_MIN_MINOR=21
MIGRATE_WAIT=600         # seconds to wait for the server to finish 247 migrations

# ---- flags -----------------------------------------------------------------
channels_args=""         # positional specs for the subscribe step
dsn_override=""
redis_override=""
pg_bin_dir=""
env_path="$repo_root/.env"
do_start=1
demo_channels=1
skip_build=0
skip_subscribe=0

usage() {
  cat <<'EOF'
Usage: ./setup.sh [flags]

Bring a fresh gramsrv checkout to a running, auto-subscribing server:
toolchain check/install, PostgreSQL + Redis provisioning (local binaries or
docker compose), build, .env, server start with schema migrations, demo
channels, auto-subscribe wiring and account backfill.

Flags:
  --dsn DSN             use this PostgreSQL DSN instead of the default
                        postgres://telesrv:telesrv@127.0.0.1:5432/telesrv_main
  --redis-addr ADDR     Redis address (default 127.0.0.1:6399)
  --pg-bin DIR          directory with initdb/postgres/psql binaries when they
                        are not in PATH or /usr/lib/postgresql/*/bin
  --env PATH            .env path (default: repo root .env)
  --channels SPECS      comma-separated channel ids / @usernames to auto-subscribe
                        (default: the demo channels, else .env value, else top 3)
  --no-demo-channels    do not create demo channels on an empty database
  --no-start            build and configure only; do not start the server
  --skip-build          reuse existing bin/telesrv and bin/subscribebackfill
  --skip-subscribe      do not touch auto-subscribe channels or backfill
  --help                this text
EOF
}

log()  { printf 'setup.sh: %s\n' "$*" >&2; }
warn() { printf 'setup.sh: WARNING: %s\n' "$*" >&2; }
die()  { printf 'setup.sh: ERROR: %s\n' "$*" >&2; exit 1; }

port_open() { (exec 3<>"/dev/tcp/$1/$2") 2>/dev/null; }
wait_port() { # host port timeout label
  local host=$1 port=$2 timeout=$3 label=$4 waited=0
  until port_open "$host" "$port"; do
    sleep 2
    waited=$((waited + 2))
    if ((waited >= timeout)); then
      die "timed out waiting for $label on $host:$port"
    fi
  done
}

# Role/database bootstrap shared by the provisioning paths. Migration 0001 sets
# session_replication_role, which only a superuser may do -- matching the compose
# deployment, where POSTGRES_USER=telesrv is the instance superuser.
ensure_telesrv_role_and_db() { # psql-command... (connects as a superuser, DB 'postgres')
  "$@" -q -v ON_ERROR_STOP=1 <<'SQL'
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'telesrv') THEN
    CREATE ROLE telesrv LOGIN PASSWORD 'telesrv' SUPERUSER;
  ELSE
    ALTER ROLE telesrv SUPERUSER;
  END IF;
END $$;
SQL
  "$@" -q -tAc "SELECT 1 FROM pg_database WHERE datname='telesrv_main'" | grep -q 1 \
    || "$@" -q -c "CREATE DATABASE telesrv_main OWNER telesrv;"
}

while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0;;
    --dsn) dsn_override=$2; shift 2;;
    --redis-addr) redis_override=$2; shift 2;;
    --pg-bin) pg_bin_dir=$2; shift 2;;
    --env) env_path=$2; shift 2;;
    --channels) channels_args=$2; shift 2;;
    --no-demo-channels) demo_channels=0; shift;;
    --no-start) do_start=0; shift;;
    --skip-build) skip_build=1; shift;;
    --skip-subscribe) skip_subscribe=1; shift;;
    *) die "unknown flag: $1 (see --help)";;
  esac
done
[[ -n $redis_override ]] && REDIS_ADDR=$redis_override
REDIS_PORT=${REDIS_ADDR##*:}
[[ -n $dsn_override ]] && DSN=$dsn_override

mkdir -p "$repo_root/data"

# ---- 1. Go toolchain --------------------------------------------------------
go_bin=""
for candidate in "$(command -v go 2>/dev/null || true)" /usr/local/go/bin/go "$HOME/.local/go/bin/go"; do
  if [[ -n $candidate && -x $candidate ]]; then
    go_bin=$candidate
    break
  fi
done
if [[ -z $go_bin ]]; then
  log "no Go toolchain found; installing the latest stable release"
  tmp_tar=$(mktemp /tmp/goXXXX.tar.gz)
  meta=$(curl -fsSL "https://go.dev/dl/?mode=json") || die "cannot reach go.dev to download Go; install Go >= 1.21 manually and re-run"
  latest=$(printf '%s' "$meta" | python3 -c "import json,sys; print(json.load(sys.stdin)[0]['version'])")
  want_file="${latest}.linux-amd64.tar.gz"
  want_sha=$(printf '%s' "$meta" | python3 -c "
import json,sys
for f in json.load(sys.stdin)[0]['files']:
    if f['filename']=='$want_file': print(f['sha256'])")
  curl -fsSL -o "$tmp_tar" "https://go.dev/dl/$want_file"
  echo "$want_sha  $tmp_tar" | sha256sum -c - >/dev/null || die "Go tarball checksum mismatch"
  install_dir=/usr/local/go
  if ! mkdir -p /usr/local 2>/dev/null || [ ! -w /usr/local ]; then
    install_dir=$HOME/.local/go
    mkdir -p "$HOME/.local"
  fi
  rm -rf "$install_dir"
  tar -C "$(dirname "$install_dir")" -xzf "$tmp_tar" 2>/dev/null || tar -C "$(dirname "$install_dir")" -xzf "$tmp_tar"
  rm -f "$tmp_tar"
  go_bin=$install_dir/bin/go
  [[ -x $go_bin ]] || die "Go installed but $go_bin is not executable"
  log "installed $(basename "$latest") at $install_dir (add $(dirname "$go_bin") to PATH)"
fi
go_ver=$("$go_bin" version | awk '{print $3}')   # e.g. go1.27.4
go_nums=${go_ver#go}                             # 1.27.4
go_major=${go_nums%%.*}                          # 1
go_minor=${go_nums#*.}                           # 27.4
go_minor=${go_minor%%.*}                         # 27
[[ $go_major =~ ^[0-9]+$ && $go_minor =~ ^[0-9]+$ ]] || die "cannot parse Go version from: $go_ver"
((go_major > GO_MIN_MAJOR || (go_major == GO_MIN_MAJOR && go_minor >= GO_MIN_MINOR))) \
  || die "Go $go_ver is too old; need >= go1.$GO_MIN_MINOR (go.mod wants toolchain go1.26.11, fetched automatically)"
export GOTOOLCHAIN=auto
log "using Go: $go_bin ($go_ver)"

# ---- 2. psql / postgres binaries --------------------------------------------
psql_bin=""
initdb_bin=""
postmaster_bin=""
pg_search_dirs=()
[[ -n $pg_bin_dir ]] && pg_search_dirs+=("$pg_bin_dir")
pg_search_dirs+=(/usr/lib/postgresql/*/bin /usr/pgsql-*/bin "$HOME/.local/pgsql/bin")
for dir in "${pg_search_dirs[@]}"; do
  [[ -x $dir/psql ]] && psql_bin=$dir/psql
  [[ -x $dir/initdb ]] && initdb_bin=$dir/initdb
  [[ -x $dir/postgres ]] && postmaster_bin=$dir/postgres
done
[[ -z $psql_bin ]] && psql_bin=$(command -v psql 2>/dev/null || true)
[[ -z $initdb_bin ]] && initdb_bin=$(command -v initdb 2>/dev/null || true)
[[ -z $postmaster_bin ]] && postmaster_bin=$(command -v postgres 2>/dev/null || true)
local_pg=0
[[ -n $initdb_bin && -n $postmaster_bin ]] && local_pg=1

# SQL runner: host psql when available, docker exec into the compose container
# when it is not (the compose postgres keeps the same role/db names).
run_sql() { # <database> [psql arguments...] (SQL read from stdin unless -f/-c given)
  local db=$1
  shift
  if [[ -n $psql_bin ]]; then
    PGPASSWORD=telesrv "$psql_bin" -q -v ON_ERROR_STOP=1 -h "$PGHOST" -p "$PGPORT" -U telesrv -d "$db" "$@"
  elif command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -qx telesrv-postgres; then
    docker exec -i telesrv-postgres psql -q -v ON_ERROR_STOP=1 -U telesrv -d "$db" "$@"
  else
    return 127
  fi
}

# ---- 3. PostgreSQL up and reachable ------------------------------------------
pg_kind="none"
if run_sql postgres -c "SELECT 1;" >/dev/null 2>&1; then
  pg_kind="existing"
  log "PostgreSQL reachable at $PGHOST:$PGPORT"
elif port_open "$PGHOST" "$PGPORT" && command -v sudo >/dev/null 2>&1 && sudo -n -u postgres psql -q -tAc "SELECT 1" >/dev/null 2>&1; then
  # A system PostgreSQL is already running but has no telesrv role yet (Debian style).
  log "creating role 'telesrv' and database 'telesrv_main' on the existing server"
  ensure_telesrv_role_and_db sudo -n -u postgres psql
  pg_kind="existing"
  log "PostgreSQL reachable at $PGHOST:$PGPORT"
else
  if ((local_pg)); then
    if [[ ${EUID:-$(id -u)} == 0 ]]; then
      die "initdb/postgres cannot run as root; run setup.sh as a normal user, or use --dsn / docker"
    fi
    if port_open "$PGHOST" "$PGPORT"; then
      die "something already listens on $PGHOST:$PGPORT but rejects telesrv/telesrv_main; start it with role 'telesrv' + database 'telesrv_main' or pass --dsn"
    fi
    pg_data="$repo_root/data/postgres"
    if [[ ! -s $pg_data/PG_VERSION ]]; then
      log "initializing a fresh PostgreSQL cluster in $pg_data"
      mkdir -p "$pg_data"
      "$initdb_bin" -D "$pg_data" --auth=trust --no-locale --encoding=UTF8 >/dev/null
    fi
    log "starting PostgreSQL on $PGPORT"
    "$postmaster_bin" -D "$pg_data" -k "$pg_data" -p "$PGPORT" -c fsync=off -c synchronous_commit=off >/dev/null 2>&1 &
    pg_pid=$!
    disown "$pg_pid" 2>/dev/null || true
    wait_port "$PGHOST" "$PGPORT" 60 "local postgres"
    # Create the role/database the default DSN expects (trust auth on a local instance).
    ensure_telesrv_role_and_db "$psql_bin" -h "$PGHOST" -p "$PGPORT" -d postgres
    pg_kind="local"
    log "local PostgreSQL ready (data: $pg_data, stop with: kill $pg_pid)"
  elif command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    log "starting postgres + redis via docker compose (deploy/docker-compose.yml)"
    (cd "$repo_root/deploy" && docker compose up -d postgres redis)
    wait_port "$PGHOST" "$PGPORT" 120 "docker postgres"
    pg_kind="docker"
  else
    die "no PostgreSQL available: install postgresql + redis-server (or docker) and re-run, or pass --dsn to an existing server"
  fi
fi

# ---- 4. Redis ----------------------------------------------------------------
redis_via_compose() {
  command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1 \
    && (cd "$repo_root/deploy" && docker compose up -d redis >/dev/null 2>&1) \
    && port_open 127.0.0.1 "$REDIS_PORT"
}
if port_open 127.0.0.1 "$REDIS_PORT"; then
  log "Redis reachable at $REDIS_ADDR"
elif command -v redis-server >/dev/null 2>&1; then
  log "starting local redis-server on port $REDIS_PORT"
  mkdir -p "$repo_root/data/redis"
  redis-server --port "$REDIS_PORT" --daemonize yes \
    --dir "$repo_root/data/redis" --pidfile "$repo_root/data/redis.pid" \
    --save "" --appendonly no
  wait_port 127.0.0.1 "$REDIS_PORT" 30 "redis-server"
elif redis_via_compose; then
  wait_port 127.0.0.1 "$REDIS_PORT" 60 "docker redis"
  log "Redis reachable via docker compose at $REDIS_ADDR"
else
  die "Redis is required (the server refuses to start without it): install redis-server, or start deploy/docker-compose.yml (redis service), or pass --redis-addr"
fi

# ---- 5. Build -----------------------------------------------------------------
# build_binary compiles one Go target. The td/tg package is huge; on memory
# constrained hosts the optimizer can get OOM-killed mid-compile. The retry
# disables optimization for that single package (same trick the repo's
# backup.sh uses serial compilation for) at some build-time/runtime cost.
build_binary() { # <output> <package>
  local out=$1 pkg=$2
  if (cd "$repo_root" && "$go_bin" build -o "$out" "$pkg"); then
    return 0
  fi
  log "build of $pkg failed (likely OOM on the large td/tg package); retrying with reduced optimizer memory"
  (cd "$repo_root" && GOFLAGS=-p=1 "$go_bin" build -gcflags='github.com/iamxvbaba/td/tg=-N -l' -o "$out" "$pkg") \
    || die "go build $pkg failed (see output above)"
}

if ((skip_build)) && [[ -x $repo_root/bin/telesrv ]]; then
  log "skipping build (--skip-build)"
else
  log "building bin/telesrv (this compiles the large td/tg package; a few minutes on first run)"
  build_binary bin/telesrv ./cmd/telesrv
  log "building bin/subscribebackfill"
  build_binary bin/subscribebackfill ./cmd/subscribebackfill
fi

# ---- 6. .env --------------------------------------------------------------------
if [[ ! -f $env_path ]]; then
  cp "$repo_root/.env.example" "$env_path"
  log "created $env_path from .env.example"
fi
ensure_env_line() { # KEY VALUE
  local key=$1 value=$2
  if grep -q "^${key}=" "$env_path"; then
    local tmp; tmp=$(mktemp "$env_path.XXXXXX")
    sed "s|^${key}=.*|${key}=${value}|" "$env_path" >"$tmp"
    cat "$tmp" >"$env_path"; rm -f "$tmp"
  else
    printf '%s=%s\n' "$key" "$value" >>"$env_path"
  fi
}
ensure_env_line "TELESRV_POSTGRES_DSN" "$DSN"
ensure_env_line "TELESRV_REDIS_ADDR" "$REDIS_ADDR"
log ".env ready: TELESRV_POSTGRES_DSN + TELESRV_REDIS_ADDR set"

# ---- 7. Start the server (it runs its own schema migrations) ----------------------
started_here=0
server_was_running=0

start_server() {
  # exec makes the subshell pid become the telesrv pid, so $! in the pid file is the real server.
  (cd "$repo_root" && exec nohup bin/telesrv >>"$log_file" 2>&1) &
  echo $! >"$pid_file"
  local pid
  pid=$(cat "$pid_file")
  log "starting bin/telesrv (pid $pid), logging to $log_file"
  local waited=0
  while ((waited < MIGRATE_WAIT)); do
    if port_open 127.0.0.1 "$LISTEN_PORT"; then
      sleep 2
      if kill -0 "$pid" 2>/dev/null; then return 0; fi
      tail -20 "$log_file" >&2 || true
      die "telesrv exited right after opening the port; see $log_file"
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      tail -20 "$log_file" >&2 || true
      die "telesrv exited during startup/migrations; see $log_file"
    fi
    sleep 2
    waited=$((waited + 2))
  done
  tail -20 "$log_file" >&2 || true
  die "timed out waiting for telesrv on 127.0.0.1:$LISTEN_PORT after ${MIGRATE_WAIT}s (first start applies all schema migrations); see $log_file"
}

if ((do_start)); then
  if port_open 127.0.0.1 "$LISTEN_PORT"; then
    server_was_running=1
    log "a server already listens on :$LISTEN_PORT; leaving it alone"
  else
    start_server
    started_here=1
    log "telesrv is up on :$LISTEN_PORT (schema migrated to the latest version)"
  fi
else
  log "--no-start: skipping server start (the server applies schema migrations on its first start)"
fi

# ---- 8. Demo channels on an empty database ---------------------------------------
demo_ids=""
count="n/a"
if ((demo_channels)) && [[ -n $psql_bin || $(command -v docker >/dev/null 2>&1 && echo docker || true) ]]; then
  count=$(run_sql telesrv_main -tAc "SELECT COUNT(*) FROM channels" 2>/dev/null || echo "n/a")
  if [[ $count == "n/a" ]]; then
    warn "cannot query channels (schema not migrated yet?); skipping demo channels"
  elif ((count > 0)); then
    log "channels exist already ($count); keeping them"
  else
    log "creating 3 demo channels: News @news, Updates @updates, Announcements @announcements"
    run_sql telesrv_main <<'SQL'
INSERT INTO channels (id, access_hash, creator_user_id, title, username, broadcast, date)
SELECT COALESCE(MAX(id), 0) + 1, 910000000001, 777000, 'News', 'news', true, EXTRACT(epoch FROM now())::int FROM channels;
INSERT INTO channels (id, access_hash, creator_user_id, title, username, broadcast, megagroup, date)
SELECT COALESCE(MAX(id), 0) + 1, 910000000002, 777000, 'Updates', 'updates', false, true, EXTRACT(epoch FROM now())::int FROM channels;
INSERT INTO channels (id, access_hash, creator_user_id, title, username, broadcast, date)
SELECT COALESCE(MAX(id), 0) + 1, 910000000003, 777000, 'Announcements', 'announcements', true, EXTRACT(epoch FROM now())::int FROM channels;
SQL
    demo_ids=$(run_sql telesrv_main -tAc "SELECT string_agg(id::text, ',' ORDER BY id) FROM channels WHERE username IN ('news','updates','announcements')")
    demo_ids=${demo_ids//$'\n'/}
    log "demo channels created: $demo_ids"
  fi
elif ((demo_channels)); then
  warn "no psql available; skipping demo channel creation (create channels with any client, then run ./scripts/fixsubscribe.sh)"
fi

# ---- 9. Auto-subscribe wiring + backfill ------------------------------------------
if ((skip_subscribe)); then
  log "--skip-subscribe: leaving TELESRV_AUTOSUBSCRIBE_CHANNELS untouched"
else
  if [[ -n $channels_args ]]; then
    sub_specs=$channels_args
  elif [[ -n $demo_ids ]]; then
    sub_specs=$demo_ids
  else
    sub_specs=""   # scripts/subscribe.sh falls back to .env value, then top 3
  fi
  schema_ready=0
  if [[ $count != "n/a" ]]; then schema_ready=1; fi
  if ((do_start && ! server_was_running)) || ((schema_ready)); then
    log "wiring auto-subscribe (TELESRV_AUTOSUBSCRIBE_CHANNELS) and backfilling existing accounts"
    (cd "$repo_root" && TELESRV_POSTGRES_DSN="$DSN" ./scripts/subscribe.sh $sub_specs)
  else
    warn "schema not migrated yet; after the first server start run: TELESRV_POSTGRES_DSN=\"$DSN\" ./scripts/subscribe.sh"
  fi
fi

# ---- 10. Restart once so the server picks up the auto-subscribe channels ----------
if ((do_start)) && ((started_here)) && (( ! skip_subscribe )) \
   && grep -q '^TELESRV_AUTOSUBSCRIBE_CHANNELS=..*' "$env_path"; then
  log "restarting telesrv so TELESRV_AUTOSUBSCRIBE_CHANNELS is active for new registrations"
  kill "$(cat "$pid_file")" 2>/dev/null || true
  for _ in $(seq 1 20); do port_open 127.0.0.1 "$LISTEN_PORT" || break; sleep 1; done
  start_server
  log "telesrv restarted with auto-subscribe active"
elif ((do_start)) && ((server_was_running)); then
  warn "a server was already running: restart it to pick up the updated .env"
fi

# ---- summary -----------------------------------------------------------------------
cat >&2 <<SUMMARY

================================================================
 setup complete
  server      : bin/telesrv, MTProto on 0.0.0.0:$LISTEN_PORT
  log / pid   : $log_file / $pid_file
  postgres    : $DSN (kind: $pg_kind)
  redis       : $REDIS_ADDR
  .env        : $env_path (TELESRV_AUTOSUBSCRIBE_CHANNELS, TELESRV_POSTGRES_DSN, TELESRV_REDIS_ADDR)
  demo channels: ${demo_ids:-none created}
  stop server : kill \$(cat $pid_file)
  re-run anytime: ./setup.sh (idempotent) ; repair subscriptions: ./scripts/fixsubscribe.sh
  register a client account -> it auto-joins the configured channels
================================================================
SUMMARY
