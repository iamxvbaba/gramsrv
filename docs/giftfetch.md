# giftfetch: snapshotting the official Star-gift catalog

`cmd/giftfetch` pulls the official Telegram Star-gift catalog, the current
upgrade-attribute pools, and every document resource either response references,
into a local directory that the server reads as a **read-only** snapshot. It is
a fetcher only: it never imports anything into telesrv and never copies the
authorization session anywhere except the path you name.

The snapshot it produces is what `TELESRV_OFFICIAL_GIFTS_DIR` points at
(`data/official-gifts` by default), which the admin panel's
Commerce → Gifts import screen reads for its explicit, hash-verified imports.

## Getting a session first

`payments.getStarGifts` answers **only for an authorized user session**. API
credentials are not sufficient and never will be — this is the one thing about
the official gifts API worth knowing before you start. `giftfetch` therefore
needs a session file, and there are two ways to get one.

### QR login (no phone number, no SMS)

```sh
SESSION=/root/.giftfetch.session go run ./cmd/giftfetch -login-qr
```

Scan the code from an already-logged-in Telegram:
**Settings → Devices → Link Desktop Device**. There is no phone number, no SMS
code and no 2FA password involved. The command authorizes the `SESSION` file and
exits without fetching.

Two details that matter when you are driving this from a script or a headless
box:

- **The token rotates roughly every 30 seconds and the code re-renders each
  time.** Just leave it running; it will not expire out from under you.
- **The login URL has to be rendered, not clicked.** Telegram hands it over as
  a `sg://login` deep link carrying the token as a query parameter, and no
  client opens that as a link — it is QR payload only. `giftfetch` draws it
  twice for you: as half-block terminal art (`▀ ▄ █`), and as a PNG at
  `<SESSION>.qr.png` for when the terminal is too small to scan. Override the
  PNG path with `-qr-png`.

An already-authorized session prints `[session already authorized]` and exits,
so the command is safe to re-run.

### Phone + SMS (the older route)

`cmd/appearancefetch` can mint one, but its fetch subcommand also runs the whole
wallpaper/theme harvest as a side effect. Prefer `-login-qr` unless you want
that snapshot too.

## Running a fetch

```sh
SESSION=/root/.giftfetch.session go run ./cmd/giftfetch -out /opt/gramsrv/data/official-gifts
```

This writes:

| Path | Contents |
| --- | --- |
| `manifest.json` | Every artifact with its SHA-256, size and the counts that describe the snapshot. |
| `catalog.tl` | The raw `payments.getStarGifts` response. |
| `upgrade-attributes/<gift_id>.tl` | One raw `payments.getStarGiftUpgradeAttributes` response per currently-upgradeable gift. |
| `documents/<id>.<ext>` | The downloaded document binaries, named by document id and extension. |
| `thumbs/<id>-photo-<type>.bin`, `thumbs/<id>-video-<type>.bin` | Thumbnails. Skipped entirely by `-skip-thumbs`. |

The boundary is deliberate and recorded in the manifest's `boundary_note`: this
is `getStarGifts(hash=0)` plus the attribute pools for every currently
upgradeable gift — the complete *current* official definitions, not deleted
historical ones and not precomputed model/pattern/backdrop combinations.

### Flags

| Flag | Default | What it does |
| --- | --- | --- |
| `-login-qr` | off | Authorize `SESSION` through a QR login and exit without fetching. |
| `-qr-png` | `<SESSION>.qr.png` | Where to write each rotated QR token as a PNG. |
| `-out` | — | Output directory. Required unless `-login-qr`. |
| `-reuse-metadata` | off | Strictly decode the existing `catalog.tl` and `upgrade-attributes/*.tl` instead of refetching them. Downloads only documents. |
| `-skip-thumbs` | off | Download main documents only. |
| `-workers` | 8 | Concurrent document downloads (max 128). |
| `-max-document-bytes` | 16 MiB | Ceiling for one document or thumbnail (max 256 MiB). |
| `-allow-missing-thumb` | — | Comma-separated `document_id:photo\|video:type` entries that may be recorded as explicitly missing after a failed download, instead of aborting the run. |

`-reuse-metadata` is the flag to reach for when you only need to fill in
documents that are new since the last run; a full fetch re-downloads metadata
every time.

### Network

`SOCKS5_PROXY` (`[user:pass@]host:port`) is parsed once and shared by the login
and the fetch path — a QR login from a blocked network needs it just as much as
a catalog fetch does.

### API credentials

The built-in api_id is public, and Telegram can refuse it with
`API_ID_PUBLISHED_FLOOD`. Set `TG_API_ID` and `TG_API_HASH` to your own pair
from my.telegram.org (API development tools) to use it instead.

### Timeouts

Login is bounded at 15 minutes (the code rotates for you, so this only matters
if you walk away); a full fetch is bounded at 4 hours, because a cold catalog
with every thumbnail is a lot of downloading.

## What it validates

Every document's size is checked against what Telegram reported before it is
written. `.tgs` animations are additionally run through the same
`stargifts.PrepareAnimation` the server itself uses, and the verdict is recorded
per document as `animation_validated` / `validation_error` — so you can see
before importing whether the server will accept a given file, rather than
discovering it at import time.

A thumbnail that fails to download aborts the run unless you have named it in
`-allow-missing-thumb`; documents themselves are not optional.

## Session files are credentials

A `.session` file is a live account credential. The repository ignores
`*.session` and `*.session.qr.png` for exactly this reason.

`scripts/giftfetch_telethon.py` — the Python reimplementation of this snapshot,
useful when Telethon is the only thing installed — **defaults `--session` to a
bare relative name**, so running it from the repository root drops a live
credential into the working tree where `git add -A` will pick it up. Pass an
absolute path outside the repo, or run it from elsewhere.

## Related

- `docs/configuration.en.md` — `TELESRV_OFFICIAL_GIFTS_DIR` and the other
  snapshot settings.
- `docs/collectible-gifts.md` — how the server reads and prices the catalog.
- `scripts/giftfetch_telethon.py` — the Telethon equivalent, and its
  `--self-test`.