# Vendored `github.com/iamxvbaba/td` — Layer 232

`go.mod` replaces `github.com/iamxvbaba/td` with this directory because no
published fork release carries Layer 232 yet. Drop the `replace` and this
directory once an upstream tag with Layer 232 exists.

## Provenance

- Base: `github.com/iamxvbaba/td` `v1.5.0` (`2fba6b334`, canonical Layer 231).
- Schema: Telegram Desktop `dev@850fb90eb6db96c124b6da1349060f9ed00f4fe4`
  ("Update API scheme to layer 232"), blob
  `8319b7df634d145390c299404129fd161a5dbd2a`, SHA-256
  `76922ce265441d3f2f5188f56b69b41e1cb11354657ea547707507f8c3b93826`.
- Not copied: `_fuzz/` corpora, `examples/` and `_tools/` (separate modules,
  unused by the server build), `.github/`.

## Procedure (per `_schema/layers/README.md`)

1. `gotdgen --schema-import` of the TDesktop `api.tl` (Layer 232 canonical,
   exact profiles 225–231 kept).
2. Policy audit: 249 retained, 0 stale, 21 new obligations — all for
   `payments.getStarGiftWithdrawalUrl`, whose `password:InputCheckPasswordSRP`
   became `tmp_password:bytes`. Decisions (7 per old profile):
   - old→canonical `password` discard → adapter
     `layerCaptureStarGiftWithdrawalLegacyPassword` (packs the SRP proof into a
     NUL-prefixed `tmp_password`, see `tlprofile/adapters_star_gift_withdrawal.go`);
   - old→canonical required `tmp_password` → adapter
     `layerRequireStarGiftWithdrawalTmpPassword`;
   - canonical→old required `password` → `reject` (a server never sends this
     request).
   Every other 232 change is an optional field/constructor handled by the
   generic projection (dropped for older profiles).
3. `go generate .` twice — byte-identical output.
4. Tests: `./gen ./gen/semantic ./cmd/gotdgen ./tg ./tlprofile ./mtproto
   ./gen/example ./bin` pass; new `tlprofile/layer232_test.go`. Locked
   counts/budgets updated for the seventh exact profile. `go test -race` was
   not run (no C toolchain on the build host).

## ShuzaGram link rebrand (post-generation)

After generation, every official Telegram short-link host becomes `sgq.me`
and the `tg:` deep-link scheme becomes `sg:` across this copy (45 files:
deeplink parsing, markdown/HTML/rich `sg://user|emoji|time`, the QR login
link, tests and doc comments of generated code). Re-apply after any
regeneration:

```sh
perl -pi -e '
s/telegram(\\*)\.(?:me|dog)(?![A-Za-z0-9_])/sgq$1.me/g;
s/(?<![A-Za-z0-9_-])t(\\*)\.me(?![A-Za-z0-9_])/sgq$1.me/g;
s/(?<![A-Za-z0-9_.-])tg:(?=\/\/|[a-z_]+[?\/])/sg:/g;
s/"tg:"/"sg:"/g; s/\ba tg: link\b/an sg: link/g;
s/\b(u|parsed)\.Scheme (==|!=) "tg"/$1.Scheme $2 "sg"/g;' <files>
```

then collapse the resulting duplicate hosts in `telegram/deeplink/deeplink.go`
(`hasTelegramPrefix` and the `https` host `case`). `make check_generated`
therefore differs from a pristine regeneration only by this pass.

## Server side

`internal/rpc` verifies `tmp_password` either as a packed legacy SRP proof
(`tlprofile.DecodeLegacySRPTmpPassword`) or as an `account.getTmpPassword`
token (HMAC, bound to user, auth key, expiry and last password change).
Layer 232 methods: free-messages tokens (`contacts.export/importNoPaidMessagesToken`
+ `messageActionNoPaidMessagesTokenUsed`), share themes (`messages.setShareTheme`,
`messages.getShareThemeBackdrops`, `userFull`/`channelFull.share_theme`) and
`payments.deleteStarGift` are implemented; `quote_rich` is kept as plain quote
text. `payments.withdrawStarGift`, `depositStarGift`, `displayStarGift` and
`wallet.setCardStickers` need on-chain TON NFTs and answer `WALLET_UNAVAILABLE`.
