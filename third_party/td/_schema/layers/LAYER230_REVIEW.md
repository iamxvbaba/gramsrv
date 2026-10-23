# Layer 230 schema review

Source: Telegram Desktop 7.3.1, local `dev` at
`5d1f64617b`, official schema commit
`d36272b193057a114ef3a9fe7f15a8f8b587c489`, reachable from fetched upstream/dev.
The exact blob and SHA-256 are locked in manifest.json. No client patch is part
of this change. The gotd upstream code baseline is unchanged; fork main was
fast-forwarded to its published v1.3.4 before importing the schema.

The canonical profile moves from 229 to 230. Profiles 225–229, including the
228/229 PageBlock storage profiles, remain generated. Schema is imported from
Git-verified source; tg and the sparse exact-profile sidecar are regenerated
through the existing semantic IR, never copied from an upstream generator.

## Reviewed conversion decisions

The first policy audit retained 167 exact decisions, reported no stale keys,
and found 60 new obligations (30 text field conversions, 5 removed Firebase
credentials, and 25 wallet update projections). Additional explicit field
policies override mechanical omission for newly meaningful flags.

| Change | Decision |
|---|---|
| BotVerification.description, BotVerifierSettings.custom_description, bots.setCustomVerification.custom_description | Historical string becomes TextWithEntities with no entities. Reverse projection accepts plain text only; nonempty entities fail closed. Optional absent and present-empty fields remain distinct. |
| username.deletable / expired | Old profiles cannot express these account state/capability flags; reject when set, including raw flag bits. The unchanged constructor CRC must not bypass this policy. |
| bots.checkUsername.additional | Reject old-profile encoding when set; do not silently check a different username category. |
| auth.initFirebasePnvLogin.except_ids | Reject old-profile encoding when present, including present-empty vectors; do not silently change account filtering. |
| inputPasskeyCredentialFirebasePNV removed | Reject the old-only credential; do not impersonate a public-key passkey or invent a token adapter. |
| Wallet/TON Connect updates | Reject projection to 225–229; never drop financial/session events or substitute an unrelated update. |
| New wallet, TON Connect, onramp and currency methods/types | Exact Layer 230 only. Schema support does not imply server business implementation. |

Client evidence: `boxes/peers/verify_peers_box.cpp:61–84` sends text plus
entities; `data/data_user.cpp:1135–1145` consumes structured verification text.
The exact schema records the new username/auth flags and the five wallet
updates. Those wallet updates have no pts/pts_count; this library upgrade
allocates no PTS, QTS, outbox entries or server business state.

## Verification matrix

Cover exact 225–230 request/result/object encoding, same-CRC flags, legacy
string adapters in both directions, optional presence, rich-entity rejection,
new update rejection on old profiles, removed credential rejection, nested
wallet result classes, malformed input and transactional cursor/buffer state.
Retain PageBlock 228/229 decoding, old keyboard adapters, client RPC overlays,
wrapper admission and frozen-result tests. Run deterministic generation,
generator/source-budget gates, full root module tests/vet, targeted races and
downstream telesrv compile/codec tests using a temporary go.work (no go.mod
replace). Verification outcomes and the release tag are recorded after gates.


## Release gates (2026-10-12, v1.4.0)

- `go generate .` repeated after the final generator changes: all 3,221
  generated outputs (Go files and the execution audit) are byte-identical.
- `go test ./... -count=1 -timeout=15m`, `go vet ./...`, and `go mod verify` pass.
- `CGO_ENABLED=1 go test -race ./tlprofile ./mtproto -count=1` passes.
- Downstream telesrv temporary-workspace gates pass: full `internal/rpc` and
  `internal/mtprotoedge` package tests (including Passkey registration/login
  E2E), their vet checks, and `go build ./cmd/...`. No local go.mod replace.
- Downstream targeted RPC/MTProto races covering Layer 230, verification,
  Passkey and rich-message storage also pass.
- Library tests cover literal legacy verification wires, optional absent and
  present-empty descriptions, structured text in 230, rich-text rejection in
  225–229, bool/raw flags, nested username updates, present-empty except_ids,
  rejected standalone/nested Firebase credentials, valid public-key passkeys,
  new wallet object/vector RPC results, wallet update rejection, truncated
  admission, and transactional input/output buffers.
- Downstream tests cover rejection before verification persistence, additional
  username rejection, unchanged ordinary username behavior, unimplemented
  wallet compatibility trace, and retained explicit 229 PageBlock storage.
  Future-Layer watermark tests now use canonical + 1 instead of assuming 230
  remains unsupported.

The sidecar is 12,980,108 bytes / 341,928 lines in 74 files; the existing 16 MiB /
400,000-line caps are unchanged. The six-profile audit has 14,686 routes:
12,370 direct, 3 retag, 2,101 rewrite, 139 policy and 73 profile-only. Shared
plans total 1,626 body / 1,085 preflight / 858 result; the plan-count review gate
was raised to 1,700 / 1,100 / 900 for this measured schema expansion, retaining
the independent source-size and sparsity-ratio gates. The original policy's
167 decisions are retained unchanged, with 76 newly explicit decisions.

No real TDesktop UI session or deployment is claimed. This is a protocol
release with minimal server boundary adaptation, not an implementation of
Wallet/TON Connect or rich-verification persistence. Historical generic
wrappers continue to be handled by RPC admission; the wrapper metadata test
uses a literal exact wire oracle independent of canonical-only object encoding.
