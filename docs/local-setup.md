# Local setup

This guide shows the shortest safe path for running gramsrv on a development
machine or a small test server.

## 1. Prepare local configuration

The repository intentionally tracks only `.env.example`. Your real `.env` is
ignored by Git and must not be committed.

Linux / macOS:

```bash
cp .env.example .env
${EDITOR:-nano} .env
```

Windows PowerShell:

```powershell
Copy-Item .env.example .env
notepad .env
```

If you prefer a different config filename, set `TELESRV_CONFIG` as a process
environment variable before starting the server.

## 2. Set the network values

Review at least these values in `.env`:

- `TELESRV_LISTEN` is the MTProto bind address. Use `0.0.0.0:2398` when
  external clients must connect to this host, or `127.0.0.1:2398` for
  same-machine testing only.
- `TELESRV_ADVERTISE_IP` must be a client-reachable IPv4 or IPv6 address, not a
  DNS name. Use `127.0.0.1` only when the patched client runs on the same
  machine. Use a LAN or public IP for phones, other computers, or remote tests.
- `TELESRV_PUBLIC_BASE_URL` and `TELESRV_PUBLIC_WEB_BASE_URL` are HTTP(S) URLs
  used in generated public links. Put hostnames here, not in
  `TELESRV_ADVERTISE_IP`.
- `TELESRV_DEV_AUTH_CODE=12345` is convenient for local development but must not
  be exposed as a production login code.

## 3. Run Telesrv Stack with Docker

To build and run the entire microservice stack locally:

```bash
docker compose -f docker-compose-development.yml up --build -d
```

Or for production deployment using prebuilt GHCR images, see [`docs/production.md`](production.md).

If you want to run only PostgreSQL and Redis locally with Docker while running Go binaries on the host:

```bash
docker compose -f docker-compose-development.yml up -d postgres redis
```

## 4. Build and run individual microservices on host

Linux / macOS:

```bash
go build -o bin/telesrv-edge ./cmd/telesrv-edge
go build -o bin/telesrv-core ./cmd/telesrv-core
```

Windows PowerShell:

```powershell
.\scripts\restart-local-microservices.ps1
```

## 5. First-start checklist

After startup, confirm:

- migrations completed successfully;
- `data/server_rsa.pem` was created if it did not already exist;
- MTProto is listening on `TELESRV_LISTEN`;
- Postgres and Redis connections are healthy;
- patched clients use the matching DC address, port, and server RSA key.

For the complete configuration reference, see
[`docs/configuration.en.md`](configuration.en.md).
