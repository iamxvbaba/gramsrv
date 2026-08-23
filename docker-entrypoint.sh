#!/bin/sh
set -e

# ==============================================================================
# Telesrv Container Entrypoint Script
# ==============================================================================

cmd_name="$(basename "${1:-}" 2>/dev/null || echo "${1:-}")"
init_profile="${TELESRV_INIT_PROFILE:-none}"

has_profile() {
  case ",$init_profile," in
    *",$1,"*) return 0 ;;
    *) return 1 ;;
  esac
}

if has_profile storage; then
  mkdir -p \
    /app/data/blobs \
    /app/data/blob-staging \
    /app/data/telegram-login \
    /app/data/updates/files \
    /app/data/langpack   
    # /app/data/sticker-seed \
    # /app/data/official-gifts \
    # /app/data/premium-promo \
    # /app/data/maptiles
fi

# ------------------------------------------------------------------------------
# 1. First-Run RSA Key Generation (server_rsa.pem & server_rsa.pub)
# ------------------------------------------------------------------------------
if has_profile server && [ ! -f "/app/data/server_rsa.pem" ]; then
  echo "[telesrv-entrypoint] Generating initial MTProto RSA private key..."
  openssl genrsa -traditional -out /app/data/server_rsa.pem 2048
  chmod 600 /app/data/server_rsa.pem || true
  
  echo "[telesrv-entrypoint] Extracting server RSA public key (for client configuration)..."
  openssl rsa -in /app/data/server_rsa.pem -pubout -out /app/data/server_rsa.pub
  echo "[telesrv-entrypoint] Server RSA public key saved to /app/data/server_rsa.pub"
fi

if has_profile server && [ -f "/app/data/server_rsa.pem" ]; then
  if grep -q "BEGIN PRIVATE KEY" /app/data/server_rsa.pem; then
    echo "[FATAL] /app/data/server_rsa.pem uses PKCS#8 format."
    echo "        Run telesrv-key-migrate before starting the service."
    exit 1
  fi
  if ! openssl rsa -in /app/data/server_rsa.pem -check -noout >/dev/null 2>&1; then
    echo "[FATAL] /app/data/server_rsa.pem is not a PKCS#1 RSA private key."
    echo "        Run telesrv-key-migrate before starting the service."
    exit 1
  fi
  if [ ! -f "/app/data/server_rsa.pub" ]; then
    openssl rsa -in /app/data/server_rsa.pem -pubout -out /app/data/server_rsa.pub >/dev/null 2>&1
  fi
fi

# ------------------------------------------------------------------------------
# 2. First-Run Telegram Login / OIDC Keys Provisioning
# ------------------------------------------------------------------------------
if has_profile server && [ ! -f "/app/data/telegram-login/signing-keys.json" ]; then
  echo "[telesrv-entrypoint] Initializing Telegram Login OIDC key ring..."
  if command -v telegramloginkeygen >/dev/null 2>&1; then
    telegramloginkeygen -mode init -dir /app/data/telegram-login || {
      echo "[telesrv-entrypoint] Warning: telegramloginkeygen initialization failed."
    }
  fi
fi

# ------------------------------------------------------------------------------
# 3. Security & Password Validation (Fail-Fast)
# ------------------------------------------------------------------------------
validate_secret() {
  var_name="$1"
  var_value="$2"
  if [ -z "$var_value" ] || [ "$var_value" = "CHANGEME" ]; then
    echo "[FATAL] Required configuration secret '$var_name' is missing or unconfigured."
    echo "        Please set secure random secrets in .env before launching."
    echo "        See docs/production.md for the automated secret generation command."
    exit 1
  fi
}

case "$cmd_name" in
  telesrv-core|telesrv-egress|telesrv-file|telesrv-admin|telesrv-ton)
    validate_secret "POSTGRES_PASSWORD" "$POSTGRES_PASSWORD"
    ;;
esac

case "$cmd_name" in
  telesrv-core|telesrv-edge)
    validate_secret "TELESRV_CORE_EXEC_TOKEN" "$TELESRV_CORE_EXEC_TOKEN"
    validate_secret "TELESRV_FILE_TOKEN" "$TELESRV_FILE_TOKEN"
    ;;
esac

case "$cmd_name" in
  telesrv-core)
    # The Admin API server itself runs inside telesrv-core (adminapi.Start,
    # addr :2599); telesrv-admin is only the client authenticating to it with
    # this same token. adminapi.Start already refuses to start on an empty
    # token, but that surfaces as a buried Go error deep in core's boot log --
    # fail fast here with the same clear message as the other secrets.
    validate_secret "TELESRV_ADMIN_API_TOKEN" "$TELESRV_ADMIN_API_TOKEN"
    ;;
esac

case "$cmd_name" in
  telesrv-edge|telesrv-egress)
    validate_secret "TELESRV_EGRESS_ACK_TOKEN" "$TELESRV_EGRESS_ACK_TOKEN"
    ;;
esac

case "$cmd_name" in
  telesrv-admin)
    # internal/config/admin_config.go already refuses to build the config
    # (fatal Go error) if UI password/token are both empty or session_key is
    # empty. Fail fast here instead, with the same clear message as every
    # other secret, rather than a buried config-parse error.
    validate_secret "TELESRV_ADMIN_UI_PASSWORD" "$TELESRV_ADMIN_UI_PASSWORD"
    validate_secret "TELESRV_ADMIN_SESSION_KEY" "$TELESRV_ADMIN_SESSION_KEY"
    validate_secret "TELESRV_ADMIN_API_TOKEN" "$TELESRV_ADMIN_API_TOKEN"
    ;;
esac

case "$cmd_name" in
  telesrv-core|telesrv-sfu)
    validate_secret "TELESRV_GROUPCALL_CONTROL_TOKEN" "$TELESRV_GROUPCALL_CONTROL_TOKEN"
    validate_secret "TELESRV_SFU_CONTROL_TOKEN" "$TELESRV_SFU_CONTROL_TOKEN"
    ;;
esac

# Execute the requested container process
exec "$@"