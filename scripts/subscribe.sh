#!/usr/bin/env bash
# Turn auto-subscribe on, end to end:
#
#   1. resolve the target channels (args, TELESRV_AUTOSUBSCRIBE_CHANNELS, then top 3 by members)
#   2. write TELESRV_AUTOSUBSCRIBE_CHANNELS=<ids> into the deployment .env, so every account
#      registered from now on is joined automatically by the server (auth.SignUp hook)
#   3. run scripts/fixsubscribe.sh to also join all accounts that already exist
#
#   ./scripts/subscribe.sh                    # pick up env/.env, else the top 3 channels
#   ./scripts/subscribe.sh @news @updates     # explicit channels, by username or id
#
# Restart the server afterwards so it reloads .env (systemctl restart gramsrv / docker compose
# restart telesrv); the backfill in step 3 applies to existing accounts without a restart.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./scripts/subscribe.sh [channel specs...]

Channel specs: numeric channel ids and/or @usernames, e.g. `1001 @news 1003`.
Without specs the current TELESRV_AUTOSUBSCRIBE_CHANNELS (env or .env) is kept,
and when nothing is configured the top 3 channels by participant count are used.

The script resolves the specs to numeric ids, stores them as
TELESRV_AUTOSUBSCRIBE_CHANNELS in the deployment .env (repo root by default),
and then applies them to existing accounts via scripts/fixsubscribe.sh.

Flags:
  --env PATH     deployment .env path (default: repo root .env)
  --top N        top-N fallback when nothing is configured (default: 3)
  --list         list candidate channels and exit
  --dry-run      write nothing; report the .env line and planned joins
EOF
}

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
fixscript="$script_dir/fixsubscribe.sh"
tool="$repo_root/bin/subscribebackfill"

has_flag() {
  local name=$1
  shift
  local arg
  for arg in "$@"; do
    [[ $arg == "$name" || $arg == "$name="* ]] && return 0
  done
  return 1
}

flag_value() {
  local name=$1
  shift
  local arg
  for arg in "$@"; do
    if [[ $arg == "$name" ]]; then
      return 0
    fi
  done
  return 1
}

if has_flag --help "$@" || has_flag -h "$@"; then
  usage
  exit 0
fi

env_path="$repo_root/.env"
if flag_value --env "$@"; then
  # Take the value right after --env.
  next=0
  for arg in "$@"; do
    if [[ $next == 1 ]]; then
      env_path=$arg
      break
    fi
    [[ $arg == "--env" ]] && next=1
  done
fi

[[ -x $tool ]] || {
  command -v go >/dev/null 2>&1 || {
    printf 'subscribe.sh: go is required to build %s\n' "$tool" >&2
    exit 1
  }
  printf 'subscribe.sh: building %s\n' "$tool" >&2
  (cd "$repo_root" && GOFLAGS=-p=1 go build -o "$tool" ./cmd/subscribebackfill)
}

# Pure inspection: list candidates and exit before anything is written.
if has_flag --list "$@"; then
  exec "$tool" --env "$env_path" --list "$@"
fi

# Resolve specs (or the env fallback) to the numeric id list stored in .env.
ids=$("$tool" --env "$env_path" --resolve "$@")
if [[ -z $ids ]]; then
  printf 'subscribe.sh: no channels resolved; nothing to do\n' >&2
  exit 1
fi
if [[ ! $ids =~ ^[0-9]+(,[0-9]+)*$ ]]; then
  printf 'subscribe.sh: channel resolver returned an unexpected value, aborting:\n%s\n' "$ids" >&2
  exit 1
fi
printf 'subscribe.sh: auto-subscribe channels: %s\n' "$ids" >&2

if has_flag --dry-run "$@"; then
  if [[ -f $env_path ]] && grep -q '^TELESRV_AUTOSUBSCRIBE_CHANNELS=' "$env_path"; then
    printf 'subscribe.sh: dry run, would replace the TELESRV_AUTOSUBSCRIBE_CHANNELS line in %s\n' "$env_path" >&2
  else
    printf 'subscribe.sh: dry run, would append TELESRV_AUTOSUBSCRIBE_CHANNELS to %s\n' "$env_path" >&2
  fi
  exit 0
fi

# Idempotent write: replace an existing line, otherwise append (creating .env when missing).
touch "$env_path"
if grep -q '^TELESRV_AUTOSUBSCRIBE_CHANNELS=' "$env_path"; then
  tmp=$(mktemp "$env_path.XXXXXX")
  sed 's|^TELESRV_AUTOSUBSCRIBE_CHANNELS=.*|TELESRV_AUTOSUBSCRIBE_CHANNELS='"$ids"'|' "$env_path" >"$tmp"
  cat "$tmp" >"$env_path"
  rm -f "$tmp"
  printf 'subscribe.sh: updated TELESRV_AUTOSUBSCRIBE_CHANNELS in %s\n' "$env_path" >&2
else
  {
    echo ''
    echo '# Channels every newly registered account joins automatically (scripts/subscribe.sh).'
    echo 'TELESRV_AUTOSUBSCRIBE_CHANNELS='"$ids"
  } >>"$env_path"
  printf 'subscribe.sh: wrote TELESRV_AUTOSUBSCRIBE_CHANNELS into %s\n' "$env_path" >&2
fi

# Apply to accounts that already exist (idempotent; skips current members).
exec "$fixscript" "$ids" "$@"
