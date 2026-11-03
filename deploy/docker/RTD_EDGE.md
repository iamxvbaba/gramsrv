# Running the `rtd` edge in the v2 stack

`compose.rtd-edge.yaml` is an **opt-in overlay** that swaps the Go edge for the
Rust [`rtd`](https://github.com/roon9e/rtd) edge, keeping the rest of the v2
stack (postgres, redis, migrate, file, core) unchanged. The default
`docker compose -f compose.yaml up -d` is **not** affected: the overlay is only
applied when you pass it explicitly with `-f`.

The `rtd` edge serves the same MTProto ingress on `0.0.0.0:2398` and forwards
admitted calls to the core's `CoreExecService` at `core:2440`, presenting the
shared bearer token as gRPC `authorization` metadata.

The edge image is published to **`ghcr.io/roon9e/rtd-edge`** for `linux/amd64`
and `linux/arm64` by the rtd repository's release workflow (on `v*` tags), so no
local build is required. GHCR packages are private until they are made public
once; if the package is still private, `docker login ghcr.io` first (or flip the
package to public under your GitHub package settings).

## 1. Bring up the stack

```sh
cd deploy/docker
docker compose --env-file .env -f compose.yaml -f compose.rtd-edge.yaml \
  up -d postgres redis migrate file core edge
```

## 2. RSA identity

The RSA identity works exactly like the v2 edge: the image entrypoint resolves
it onto the shared `edge_state` volume (mounted at `/var/lib/telesrv-edge`) on
first start, and the key is never a file in the checkout.

- `TELESRV_RSA_IDENTITY_MODE=test` (the default) decodes the published test key
  from the read-only `./assets` mount. Clients should use the matching public
  key, `deploy/docker/assets/test-server-rsa.pub`.
- `TELESRV_RSA_IDENTITY_MODE=generated` creates a fresh 2048-bit key on the
  volume on first run; read the public half out with:

  ```sh
  docker compose --env-file .env -f compose.yaml -f compose.rtd-edge.yaml \
    run --rm --no-deps --entrypoint openssl edge \
    rsa -in /var/lib/telesrv-edge/server_rsa.pem -RSAPublicKey_out
  ```

Either way the key persists on the `edge_state` volume until it is removed, and
reverting to the Go edge reuses the same key.

## Configuration

The overlay sets these `RTD_EDGE_*` variables (see the overlay for the exact
values):

| variable | meaning |
| --- | --- |
| `RTD_EDGE_BIND` | MTProto listen address (`0.0.0.0:2398`) |
| `RTD_EDGE_DC` | serving datacenter id |
| `RTD_EDGE_LAYER` | layer profile bare requests are admitted under; **must match the schema the core re-admits** (the v2 core pins layer `229`) |
| `RTD_EDGE_STATE_DIR` | mounted state volume (`/var/lib/telesrv-edge`) |
| `RTD_EDGE_RSA_IDENTITY_MODE` | `test` (decode the mounted test key) or `generated` |
| `RTD_EDGE_TEST_KEY` | base64 PEM decoded in `test` mode |
| `RTD_EDGE_CORE` | core `CoreExecService` endpoint |
| `RTD_EDGE_CORE_TOKEN` | bearer token (`${TELESRV_CORE_EXEC_TOKEN}`) |
| `RTD_EDGE_WEBSOCKET` | enable WebSocket ingress |

## Reverting

Drop the overlay and recreate the edge to go back to the Go edge:

```sh
docker compose --env-file .env -f compose.yaml up -d --force-recreate edge
```

## Current limitations

The `rtd` edge implements the MTProto RPC path only. The Go edge's file-data and
egress fast paths, Redis caches, location registry, and media sockets are not
implemented, so uploads and media do not work through this overlay. `rtd` is a
plain MTProto 2.0 edge; it deliberately does not implement `/owpengram/server-info`.

> **Windows checkout note:** `deploy/docker/docker-entrypoint.sh` must keep LF
> endings — a CRLF shebang makes the core container fail with
> `exec .../telesrv-container-entrypoint failed: No such file or directory`.
> This is enforced by `*.sh text eol=lf` in `.gitattributes`.
