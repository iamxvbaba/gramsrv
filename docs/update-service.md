# Native client update service

`cmd/telesrv-update` is a standalone HTTP service that provides two
Telegram-compatible update surfaces:

- `/current4` and `/files/*` for the built-in Telegram Desktop updater;
- `/v1/resolve` for the main `telesrv` process to answer
  `help.getAppUpdate` for Android, iOS, and other supported builds.

The service has no HTTP upload endpoint. Operators publish a release by placing
an immutable artifact in the configured `files` directory and atomically
replacing `manifest.json`. The catalog validates the file size and SHA-256
before exposing it, so truncated or accidentally replaced packages fail closed.

The runtime container mounts only `data/updates` read-only. It does not create
directories, generate identity keys, or modify the catalog. Publish updates
from a separate operator or release job.

## Quick start

Create the working directories and start with the disabled example catalog:

```powershell
New-Item -ItemType Directory -Force data\updates\files
Copy-Item deploy\update\manifest.example.json data\updates\manifest.json

go run ./cmd/telesrv-update -check
go run ./cmd/telesrv-update
```

Check the local endpoints:

```powershell
Invoke-RestMethod http://127.0.0.1:2402/readyz
Invoke-RestMethod http://127.0.0.1:2402/current4
```

Connect the main server:

```dotenv
TELESRV_UPDATE_PUBLIC_URL=https://updates.example.test
TELESRV_UPDATE_SERVICE_URL=http://127.0.0.1:2402
TELESRV_UPDATE_REQUEST_TIMEOUT=2s
```

`PUBLIC_URL` must be reachable by clients and is advertised as
`help.getConfig.autoupdate_url_prefix`. `SERVICE_URL` may remain a loopback or
private route. When both routes are identical, `SERVICE_URL` may be omitted.
Production deployments should place an HTTPS reverse proxy in front of the
service without rewriting `/current4` or `/files/*`.

### Publish a manifest

Place the immutable package in `data/updates/files`, create a candidate
manifest, and validate and publish it atomically:

```bash
docker run --rm \
  -v "$PWD/data/updates:/data/updates" \
  telesrv:latest \
  telesrv-update-publish \
  -manifest /data/updates/manifest.next.json \
  -active /data/updates/manifest.json \
  -files /data/updates/files
```

The command refuses malformed manifests, missing packages, size mismatches, or
SHA-256 mismatches before replacing the active catalog. Keep the candidate and
active manifest in the same filesystem so `rename` remains atomic.

Standalone service settings:

```dotenv
TELESRV_UPDATE_LISTEN=127.0.0.1:2402
TELESRV_UPDATE_MANIFEST=data/updates/manifest.json
TELESRV_UPDATE_FILES_DIR=data/updates/files
```

The manifest is reloaded automatically when its timestamp or size changes. A
malformed replacement makes readiness and catalog requests return `503`; it is
never combined with the previously validated snapshot. Validate a candidate
before atomically replacing the active file:

```powershell
go run ./cmd/telesrv-update `
  -manifest .\manifest.next.json `
  -files .\data\updates\files `
  -check
```

## Telegram Desktop contract

TDesktop requests `<autoupdate_url_prefix>/current4`. A Windows x64 stable
release is represented as:

```json
{
  "win64": {
    "stable": {
      "released": 7000007,
      "link": "/files/tx64upd7000007"
    }
  }
}
```

The client compares `released` with its numeric `AppVersion`, downloads the
artifact with HTTP Range support, verifies the embedded RSA signature, unpacks
it, and only then exposes the normal update banner. A regular EXE or ZIP is not
a valid update package.

Build packages with TDesktop's `Packer` target. The equivalent Windows x64
command is:

```powershell
Packer.exe `
  -version 7000007 `
  -path Telegram.exe `
  -path Updater.exe `
  -path "modules\x64\d3d\d3dcompiler_47.dll" `
  -target win64
```

It produces `tx64upd7000007` and must report `Signature verified!` before the
artifact is published.

| Platform | `/current4` key | Typical package name |
|---|---|---|
| Windows x64 | `win64` | `tx64upd<build>` |
| Windows ARM64 | `winarm` | `tarm64upd<build>` |
| Windows x86 | `win` | `tupdate<build>` |
| macOS Intel | `mac` | `tmacupd<build>` |
| macOS Apple Silicon | `armac` | `tarmacupd<build>` |
| Linux | `linux` | `tlinuxupd<build>` |

## Update signing

The public TDesktop sources contain Telegram's public update key; the matching
private key is not published. A custom deployment must establish its own update
signing identity:

1. Generate a dedicated RSA-1024 key pair and keep the private key only in a
   protected build secret store.
2. Provide the private key to TDesktop's local `DesktopPrivate/packer_private.h`.
3. Embed the matching public key in both the client update verifier and Packer.
4. Rebuild the bootstrap client and Packer before publishing updates.
5. Do not rotate the key without a transition client that trusts both identities.

The update service never reads the private key and never creates signatures. It
checks SHA-256 and serves an artifact that Packer has already signed. A stock
TDesktop binary cannot install a package signed only by a custom key; the first
custom client build must be distributed out of band.

## HTTP behavior

- `/healthz` reports process liveness.
- `/readyz` validates the current catalog.
- `/current`, `/current1` ... `/current4` return desktop metadata with
  `Cache-Control: no-cache`.
- `/files/<name>` serves only artifacts referenced by the current validated
  catalog, supports GET/HEAD and Range, and emits an immutable cache policy and
  a SHA-256-based ETag.
- `/v1/resolve` returns a newer application release or `204 No Content`.

Unknown package names are not exposed merely because a file exists in the
directory. Published package names are immutable: changing an active file makes
the endpoint return `503` until a matching manifest snapshot is loaded.

## Android and iOS

The main server forwards the client platform, current `app_version`, source,
channel, and `lang_code` to `/v1/resolve`. The resolver selects localized notes,
does not offer an equal or older version, and applies `url_by_source` when a
matching installer/store source is configured.

- A standalone Android build may open or install an APK URL, but the APK must be
  signed with the same Android application signing key as the installed build.
- Google Play and other store builds should use the corresponding store URL;
  this mechanism does not bypass store policy.
- iOS may display information returned by `help.getAppUpdate`, but installation
  still happens through App Store, TestFlight, or MDM. A normal iOS application
  cannot replace itself from an arbitrary IPA URL.
- Set `can_not_skip` only after confirming that the target release is actually
  available to every affected client.

## Manifest fields

- `desktop.<platform>.<channel>.build`: numeric TDesktop `AppVersion`.
- `file`, `sha256`, `size`: immutable signed artifact and integrity metadata.
- `apps.<platform>.<channel>.id`: stable positive release identifier.
- `version`: value compared with the client's `initConnection.app_version`.
- `notes`: localized text keyed by `en`, `ru`, `ru-ru`, and similar codes.
- `url_by_source`: source-specific installer or store URL.
- `can_not_skip`: whether the client may dismiss the application update.
- `disabled`: keep a valid entry as a draft without publishing it.

Supported channels are `stable`, `beta`, and `alpha`. See
`deploy/update/manifest.example.json` for a complete disabled example.
