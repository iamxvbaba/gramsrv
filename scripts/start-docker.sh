#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./scripts/start-docker.sh [options]

Options used only when deploy/docker/.env does not exist:
  --advertise-ip IP
  --public-base-url URL
  --public-web-base-url URL
  --admin-bind-ip IP
  --sfu-host-network       Use direct host networking for SFU (default).
  --sfu-bridge-network     Use Docker UDP publishing as a compatibility fallback.
  --allow-insecure-development-auth

Other options:
  --build    Build local role images instead of pulling published images.
  --no-pull  Never contact the registry: start the role images already present
             locally (for example a stack built earlier with --build). With
             --build, base images are not refreshed.
  --help
EOF
}

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
docker_dir="$repo_root/deploy/docker"
compose_path="$docker_dir/compose.yaml"
env_path="$docker_dir/.env"
generator_path="$script_dir/new-docker-env.sh"

advertise_ip=
public_base_url=
public_web_base_url=
admin_bind_ip=
sfu_network_mode=
allow_insecure=false
build=false
pull=true
initialization_options=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --advertise-ip)
      [[ $# -ge 2 ]] || { printf '%s\n' "$1 requires a value" >&2; exit 1; }
      advertise_ip=$2
      initialization_options=true
      shift 2
      ;;
    --public-base-url)
      [[ $# -ge 2 ]] || { printf '%s\n' "$1 requires a value" >&2; exit 1; }
      public_base_url=$2
      initialization_options=true
      shift 2
      ;;
    --public-web-base-url)
      [[ $# -ge 2 ]] || { printf '%s\n' "$1 requires a value" >&2; exit 1; }
      public_web_base_url=$2
      initialization_options=true
      shift 2
      ;;
    --admin-bind-ip)
      [[ $# -ge 2 ]] || { printf '%s\n' "$1 requires a value" >&2; exit 1; }
      admin_bind_ip=$2
      initialization_options=true
      shift 2
      ;;
    --sfu-host-network)
      sfu_network_mode=host
      initialization_options=true
      shift
      ;;
    --sfu-bridge-network)
      sfu_network_mode=bridge
      initialization_options=true
      shift
      ;;
    --allow-insecure-development-auth)
      allow_insecure=true
      initialization_options=true
      shift
      ;;
    --build)
      build=true
      shift
      ;;
    --no-pull)
      pull=false
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *) printf 'start-docker: unknown argument: %s\n' "$1" >&2; exit 1 ;;
  esac
done

if [[ ! -f "$env_path" ]]; then
  [[ -n "$advertise_ip" ]] || advertise_ip=127.0.0.1
  generator_args=(--advertise-ip "$advertise_ip")
  [[ -z "$public_base_url" ]] || generator_args+=(--public-base-url "$public_base_url")
  [[ -z "$public_web_base_url" ]] || generator_args+=(--public-web-base-url "$public_web_base_url")
  [[ -z "$admin_bind_ip" ]] || generator_args+=(--admin-bind-ip "$admin_bind_ip")
  [[ "$sfu_network_mode" != host ]] || generator_args+=(--sfu-host-network)
  [[ "$sfu_network_mode" != bridge ]] || generator_args+=(--sfu-bridge-network)
  [[ "$allow_insecure" = false ]] || generator_args+=(--allow-insecure-development-auth)
  "$generator_path" "${generator_args[@]}"
elif [[ "$initialization_options" = true ]]; then
  printf 'start-docker: deploy/docker/.env already exists; initialization options were ignored to preserve credentials and deployment identity.\n' >&2
fi

configured_sfu_host_network=$(awk -F= '$1 == "TELESRV_SFU_HOST_NETWORK" { print substr($0, length($1) + 2); exit }' "$env_path")
[[ -n "$configured_sfu_host_network" ]] || configured_sfu_host_network=true
case "$configured_sfu_host_network" in
  true|false) ;;
  *) printf 'start-docker: TELESRV_SFU_HOST_NETWORK must be true or false\n' >&2; exit 1 ;;
esac
configured_bot_api_addr=$(awk -F= '$1 == "TELESRV_BOT_API_ADDR" { print substr($0, length($1) + 2); exit }' "$env_path")
case "$configured_bot_api_addr" in
  ''|0.0.0.0:8088|:8088|'[::]:8088') ;;
  *) printf 'start-docker: TELESRV_BOT_API_ADDR must be empty or listen on container port 8088 on all interfaces (0.0.0.0:8088); publish another host port with TELESRV_BOT_API_PORT\n' >&2; exit 1 ;;
esac
compose=(docker compose --project-directory "$docker_dir" --env-file "$env_path" --file "$compose_path")
if [[ "$configured_sfu_host_network" = false ]]; then
  compose+=(--file "$docker_dir/compose.sfu-bridge-network.yaml")
fi
if [[ -n "$configured_bot_api_addr" ]]; then
  compose+=(--file "$docker_dir/compose.bot-api.yaml")
fi
"${compose[@]}" version >/dev/null
"${compose[@]}" config --quiet

up_args=(up --detach --no-build --wait --wait-timeout 600)
if [[ "$build" = true && "$pull" = true ]]; then
  "${compose[@]}" build --pull
elif [[ "$build" = true ]]; then
  "${compose[@]}" build
elif [[ "$pull" = true ]]; then
  "${compose[@]}" pull
fi
# Without a pull, a missing image must fail here instead of being fetched
# silently and replacing a locally built tag (gramsrv#136).
if [[ "$pull" = false ]]; then
  up_args+=(--pull never)
fi

if ! "${compose[@]}" "${up_args[@]}"; then
  "${compose[@]}" logs --no-color --tail 120 || true
  exit 1
fi

"${compose[@]}" ps --all
printf 'telesrv Docker stack is ready. Configuration: %s\n' "$env_path"

env_value() {
  awk -F= -v key="$1" '$1 == key { print substr($0, length(key) + 2); exit }' "$env_path"
}

phone_provider=$(env_value TELESRV_PHONE_CODE_DELIVERY_PROVIDER)
dev_code=$(env_value TELESRV_DEV_AUTH_CODE)
if [[ "$phone_provider" = development && "$dev_code" =~ ^[0-9]{5,6}$ ]]; then
  printf 'Development login code: %s\n' "$dev_code"
fi
if [[ "$(env_value TELESRV_TURN_ENABLE)" = true ]]; then
  printf 'TURN/STUN: udp://%s:%s\n' "$(env_value TELESRV_TURN_ADVERTISE_IP)" "$(env_value TELESRV_TURN_UDP_PORT)"
  printf 'TURN relay UDP range: %s-%s\n' "$(env_value TELESRV_TURN_RELAY_MIN_PORT)" "$(env_value TELESRV_TURN_RELAY_MAX_PORT)"
fi
if [[ "$(env_value TELESRV_LIVESTREAM_ENABLE)" = true ]]; then
  printf 'RTMP ingest: %s (stream key is provided by the client)\n' "$(env_value TELESRV_LIVESTREAM_RTMP_URL)"
fi

admin_bind_ip=$(env_value TELESRV_ADMIN_BIND_IP)
admin_port=$(env_value TELESRV_ADMIN_PORT)
[[ -n "$admin_bind_ip" ]] || admin_bind_ip=127.0.0.1
[[ -n "$admin_port" ]] || admin_port=2600
admin_url_host=$admin_bind_ip
if [[ "$admin_url_host" = 0.0.0.0 || "$admin_url_host" = :: ]]; then
  admin_url_host=$(env_value TELESRV_ADVERTISE_IP)
fi
[[ "$admin_url_host" != *:* ]] || admin_url_host="[$admin_url_host]"
printf 'Admin UI: http://%s:%s (login password is stored in %s)\n' "$admin_url_host" "$admin_port" "$env_path"
