#!/bin/bash
set -e

DATA_DIR="${TELESRV_DATA_DIR:-/app/data}"
RSA_KEY="${TELESRV_RSA_KEY:-${DATA_DIR}/server_rsa.pem}"
RSA_PUB="${RSA_KEY%.*}.pub"
LOGIN_DIR="${DATA_DIR}/telegram-login"

mkdir -p "$DATA_DIR"

# ---------------------------------------------------------
# 1. Background watcher: Generate .pub once server writes .pem
# ---------------------------------------------------------
(
    # Wait until telesrv generates server_rsa.pem
    until [ -f "$RSA_KEY" ]; do
        sleep 0.5
    done

    # Generate public key if not already present
    if [ ! -f "$RSA_PUB" ]; then
        echo "[Entrypoint Watcher] Detected $RSA_KEY, generating $RSA_PUB..."
        openssl rsa -in "$RSA_KEY" -pubout -out "$RSA_PUB" 2>/dev/null || openssl pkey -in "$RSA_KEY" -pubout -out "$RSA_PUB"
        chmod 644 "$RSA_PUB"
        echo "[Entrypoint Watcher] Public key generated at $RSA_PUB"
    fi
) &

# ---------------------------------------------------------
# 2. Telegram Login / OIDC Keygen Initialization
# ---------------------------------------------------------
if [ ! -f "${LOGIN_DIR}/signing-keys.json" ]; then
    if [ -x "/app/bin/telegramloginkeygen" ]; then
        echo "[Entrypoint] Initializing Telegram Login keys in $LOGIN_DIR..."
        mkdir -p "$LOGIN_DIR"
        /app/bin/telegramloginkeygen -mode=init -dir="$LOGIN_DIR" || true[cite: 1]
        chmod 0700 "$LOGIN_DIR" 2>/dev/null || true[cite: 1]
        chmod 0600 "$LOGIN_DIR"/* 2>/dev/null || true[cite: 1]
    fi
fi

# ---------------------------------------------------------
# 3. Start telesrv (letting the server generate server_rsa.pem)
# ---------------------------------------------------------
echo "[Entrypoint] Starting application: $@"
exec "$@"