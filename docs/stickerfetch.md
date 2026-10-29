# stickerfetch: exporting sticker, emoji and reaction seeds

`cmd/stickerfetch` downloads sticker sets, custom emoji, reactions and message
effects from official Telegram and writes them in the layout `SeedMedia` imports
at startup from `TELESRV_STICKER_SEED_DIR` (`data/sticker-seed` by default).
It is a fetcher only: it never talks to telesrv.

## Session and API credentials

It needs an authorized user session. The `SESSION` file path defaults to
`/tmp/appearance.session`; the QR login of `cmd/giftfetch` produces a compatible
session without a phone number (see [giftfetch.md](giftfetch.md#qr-login-no-phone-number-no-sms)):

```sh
SESSION=$HOME/.tg-seed.session go run ./cmd/giftfetch -login-qr
```

The built-in TDesktop API id is shared by everyone and Telegram may answer it with
`API_ID_PUBLISHED_FLOOD`. Set your own credentials from
<https://my.telegram.org> to avoid that (`giftfetch` reads the same variables):

```sh
export TG_API_ID=123456 TG_API_HASH=0123456789abcdef0123456789abcdef
```

## Running

```sh
SESSION=$HOME/.tg-seed.session go run ./cmd/stickerfetch [-timeout 12m] <out_dir> <spec> [spec...]
```

`-timeout` bounds the whole run (default 12 minutes). `featured_emoji` and
`featured_stickers` download a hundred sets or more; give them hours, e.g.
`-timeout 3h`. Re-running is cheap: files that already exist with the expected
size are not downloaded again.

| Spec | Source |
|---|---|
| `short:<name>` | a set by short name (regular, mask or custom emoji) |
| `id:<id>:<access_hash>` | a set by id |
| `emoji_default_statuses`, `emoji_channel_default_statuses`, `emoji_default_topic_icons` | default emoji status / topic icon sets |
| `premium_gifts`, `ton_gifts` | gift sticker sets |
| `animated_emoji`, `animated_emoji_animations`, `emoji_generic_animations` | big animated emoji and their interaction animations |
| `dice:<emoji>` | dice animations (`🎲 🎯 🏀 ⚽ 🎳 🎰`) |
| `effects` | `messages.getAvailableEffects` |
| `reactions` | `messages.getAvailableReactions` |
| `featured_emoji` | every set in `messages.getFeaturedEmojiStickers` |
| `featured_stickers` | every set in `messages.getFeaturedStickers` |

A set spec writes `<out_dir>/<ShortName>_<id>/set_info.json` plus
`stickers/<doc_id>.<tgs|webm|webp>`. `reactions` and `effects` write
`telegram_reactions_export/` and `telegram_effects_export/` directly under
`<out_dir>`.

## Building a seed directory

`SeedMedia` looks for these subdirectories of the seed root:

| Directory | Contents | Command |
|---|---|---|
| `telegram_default_stickers_export/` | system sets; the kind comes from `input_set_type` in `set_info.json` | `stickerfetch <root>/telegram_default_stickers_export animated_emoji emoji_default_statuses ...` |
| `telegram_emoji_export/` | custom emoji sets (not limited by `TELESRV_STICKER_SEED_MAX_SETS`) | `stickerfetch <root>/telegram_emoji_export featured_emoji` |
| `telegram_stickers_export/` | regular sticker sets | `stickerfetch <root>/telegram_stickers_export featured_stickers` |
| `telegram_reactions_export/` | reactions | `stickerfetch <root> reactions` |
| `telegram_effects_export/` | message effects | `stickerfetch <root> effects` |

Dice sets are not identified by `input_set_type`: rename each exported directory
to `DefaultSet_Dice_<Normal|Dart|Basketball|Football|Bowling|Casino>` inside
`telegram_default_stickers_export/`.

In Docker the seed directory is mounted read-only from
`TELESRV_STICKER_SEED_HOST_DIR` (see `deploy/docker/.env.example`). Files must be
readable by the server user; `stickerfetch` writes them with mode `0644`.

## Thumbs

Every document is exported with its thumbs: inline `PhotoPathSize` /
`PhotoStrippedSize` / `PhotoCachedSize` bytes go into the JSON as hex, and
downloadable `PhotoSize` renditions are stored next to the document as
`<doc_id>_thumb<N>_PhotoSize_type<t>.jpg` (the content may be WebP; the server
sniffs it). They are not optional: Android shows a `.tgs` sticker without thumbs
as a file attachment, the emoji picker shows empty cells for custom emoji
without them, and the server draws the still image of emoji avatars from them.

## Importing

New and changed sets are picked up on the next server start. Reactions are
imported only while the reaction table is empty or incomplete, so replacing an
existing reaction export needs a cleared `available_reactions` table.
