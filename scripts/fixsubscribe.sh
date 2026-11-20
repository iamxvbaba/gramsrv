#!/usr/bin/env bash
# Ensure every active account is a member of the auto-subscribe channels. This is the repair half of
# the auto-subscribe feature: the server itself joins only NEWLY registered accounts
# (TELESRV_AUTOSUBSCRIBE_CHANNELS); accounts created before the setting existed, or before the
# channels were picked, are joined by this script via cmd/subscribebackfill.
#
#   ./scripts/fixsubscribe.sh                    # use TELESRV_AUTOSUBSCRIBE_CHANNELS, else top 3 channels
#   ./scripts/fixsubscribe.sh @news @updates     # by username
#   ./scripts/fixsubscribe.sh 1001 1002 1003     # by numeric channel id
#   ./scripts/fixsubscribe.sh --users 105,107    # repair specific accounts only
#   ./scripts/fixsubscribe.sh --list             # show candidate channels and exit
#   ./scripts/fixsubscribe.sh --dry-run          # show what would be joined, write nothing
#
# Safe to re-run any time: already-subscribed accounts are detected and skipped. The server does not
# need to be stopped. Real-time "you joined" notifications are not fanned out; clients see the
# channels on their next dialog sync.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./scripts/fixsubscribe.sh [channel specs...] [flags]

Builds bin/subscribebackfill when it is missing, then joins every active non-bot account into the
selected channels through the same store path the channels RPC uses.

Channel specs (optional):
  numeric channel ids and/or @usernames, e.g. `1001 @news 1003`.
  Without specs the tool uses TELESRV_AUTOSUBSCRIBE_CHANNELS (env or .env),
  and falls back to the top 3 channels by participant count.

Flags (forwarded to subscribebackfill):
  --users id,id   restrict the backfill to these account ids
  --list          list candidate channels and exit
  --dry-run       report the planned joins without writing
  --dsn DSN       PostgreSQL DSN override (default: TELESRV_POSTGRES_DSN, then .env)
  --env PATH      deployment .env path (default: repo root .env)
  --top N         top-N fallback when no channels resolve (default: 3)

Examples:
  ./scripts/fixsubscribe.sh
  ./scripts/fixsubscribe.sh @announcements @support
  ./scripts/fixsubscribe.sh --users 105 --dry-run
EOF
}

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
binary="$repo_root/bin/subscribebackfill"

has_flag() {
  local name=$1
  shift
  local arg
  for arg in "$@"; do
    [[ $arg == "$name" || $arg == "$name="* ]] && return 0
  done
  return 1
}

build_binary() {
  command -v go >/dev/null 2>&1 || {
    printf 'fixsubscribe.sh: go is required to build %s\n' "$binary" >&2
    return 1
  }
  printf 'fixsubscribe.sh: building %s\n' "$binary" >&2
  # Serial compilation keeps peak memory low enough for small hosts.
  (cd "$repo_root" && GOFLAGS=-p=1 go build -o "$binary" ./cmd/subscribebackfill)
}

if has_flag --help "$@" || has_flag -h "$@"; then
  usage
  exit 0
fi

[[ -x $binary ]] || build_binary

args=()
if ! has_flag --env "$@" && ! has_flag -env "$@"; then
  args+=(--env "$repo_root/.env")
fi

# Split the invocation into channel specs (positional) and flags. Value-taking flags consume their
# next argument so its value is never mistaken for a channel spec.
specs=()
flags=()
while (($#)); do
  case "$1" in
    --users|--dsn|--env|--top)
      flags+=("$1" "$2")
      shift 2
      ;;
    --help|-h|--list|--dry-run)
      flags+=("$1")
      shift
      ;;
    --)
      shift
      specs+=("$@")
      break
      ;;
    -*)
      flags+=("$1")
      shift
      ;;
    *)
      specs+=("$1")
      shift
      ;;
  esac
done
if ((${#specs[@]})); then
  args+=(--channels "$(IFS=,; echo "${specs[*]}")")
fi

# ---- helpers: Postgres / Redis access (host client when present, else the compose containers) ----
env_value() { # KEY -> value from the deployment .env (last match, quotes stripped)
  [[ -f $repo_root/.env ]] || return 0
  grep -E "^$1=" "$repo_root/.env" | tail -1 | cut -d= -f2- | sed -e 's/^ *//' -e 's/^"//' -e 's/"$//' || true
}
pg_query() { # SQL -> tuples-only output; host psql first, then the compose container
  local dsn=${TELESRV_POSTGRES_DSN:-$(env_value TELESRV_POSTGRES_DSN)} out
  dsn=${dsn:-postgres://telesrv:telesrv@127.0.0.1:5432/telesrv_main?sslmode=disable}
  if command -v psql >/dev/null 2>&1 && out=$(psql "$dsn" -tAc "$1" 2>/dev/null); then
    printf '%s\n' "$out"
    return 0
  fi
  docker exec telesrv-postgres psql -U telesrv -d telesrv_main -tAc "$1"
}
redis_cmd() {
  local addr pass host port
  addr=${TELESRV_REDIS_ADDR:-$(env_value TELESRV_REDIS_ADDR)}
  addr=${addr:-127.0.0.1:6399}
  pass=${TELESRV_REDIS_PASSWORD:-$(env_value TELESRV_REDIS_PASSWORD)}
  host=${addr%:*}
  port=${addr##*:}
  if command -v redis-cli >/dev/null 2>&1; then
    REDISCLI_AUTH=$pass redis-cli -h "$host" -p "$port" "$@"
  else
    docker exec -i telesrv-redis redis-cli "$@"
  fi
}

# The backfill tool inserts "joined" service messages through the Postgres id allocator, while the
# running server allocates channel message ids from the Redis counter counter:channel_msg_id:{id}.
# Left alone the Redis counter falls behind and every later send fails with a duplicate
# channel_messages_pkey. After each real run, raise the counter to the true maximum (atomically,
# never lowering it).
sync_channel_counters() {
  local ids id max
  ids=$("$binary" "${args[@]}" "${flags[@]}" --resolve 2>/dev/null | tail -1 || true)
  [[ $ids =~ ^[0-9]+(,[0-9]+)*$ ]] || { printf 'fixsubscribe.sh: WARNING: cannot resolve channels to sync Redis counters\n' >&2; return 0; }
  for id in ${ids//,/ }; do
    max=$(pg_query "SELECT COALESCE(max(id),0) FROM channel_messages WHERE channel_id=$id" | tr -d '[:space:]' || true)
    [[ $max =~ ^[0-9]+$ ]] || { printf 'fixsubscribe.sh: WARNING: cannot read max message id of channel %s; Redis counter NOT synced\n' "$id" >&2; continue; }
    redis_cmd EVAL "local c=tonumber(redis.call('GET',KEYS[1]) or '0'); if c < tonumber(ARGV[1]) then redis.call('SET',KEYS[1],ARGV[1]); return 1 end; return 0" 1 "counter:channel_msg_id:{$id}" "$max" >/dev/null 2>&1 \
      && printf 'fixsubscribe.sh: channel %s message counter synced (max id %s)\n' "$id" "$max" >&2 \
      || printf 'fixsubscribe.sh: WARNING: could not sync Redis counter for channel %s (set counter:channel_msg_id:{%s} to %s by hand)\n' "$id" "$id" "$max" >&2
  done
}

printf 'fixsubscribe.sh: running %s\n' "$binary" >&2
rc=0
"$binary" "${args[@]}" "${flags[@]}" || rc=$?
if (( rc == 0 )) && ! has_flag --dry-run "${flags[@]}" && ! has_flag --list "${flags[@]}" && ! has_flag --help "${flags[@]}"; then
  sync_channel_counters
fi
exit "$rc"
