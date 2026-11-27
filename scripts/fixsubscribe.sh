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

printf 'fixsubscribe.sh: running %s\n' "$binary" >&2
rc=0
"$binary" "${args[@]}" "${flags[@]}" || rc=$?
exit "$rc"
