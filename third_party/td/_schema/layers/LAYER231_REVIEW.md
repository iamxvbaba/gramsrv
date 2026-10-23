# Layer 231 schema review

Source: official Telegram Desktop `dev@78ceaa548ae0e055aa56b53fde22064d13f250ef`.
The last change to `Telegram/SourceFiles/mtproto/scheme/api.tl` is
`91ed9dbf2004ed5531a94ecc4fe5751e008dc8ed`; manifest.json locks its
Git blob `01e31d552dd496e592f262989c8452f7a3ce411a` and normalized
SHA-256 `0921a7bba8be9ad7e615d1d4f8e068126a421e3cba18f499a16765f1ab6129bf`.
The TDesktop source path is clean and this schema commit is reachable from
`origin/dev`. No client patch is part of the schema import.

Relative to Layer 230, the schema adds `InputTonConnectOauthSession`, changes
`urlAuthResultRequest` from `#3cd623ec` to `#bdca6167` with `request_wallet`,
`tc_session_id`, and `tc_client_id`, and changes `messages.acceptUrlAuth` from
`#67a3f0de` to `#18dd4e92` with optional `tonconnect_session`. No update
constructor or `pts`/`pts_count` field changes; MTProto service schema is
unchanged. Canonical moves to 231; exact profiles 225–230 remain, including
explicit 228/229 PageBlock storage readers.

## Compatibility decisions

| Scenario | Decision |
|---|---|
| Ordinary `messages.acceptUrlAuth` | Exact 231 uses the new constructor; older exact profiles retain their old constructor and result shape. No wallet session means the existing login operation is unchanged. |
| `tonconnect_session` in an older profile projection | Reject if present, including an explicitly set zero value. Never convert a wallet authorization into a plain login. |
| `urlAuthResultRequest` wallet flag/session/client metadata projected to 225–230 | Drop all three unsupported fields, retaining the ordinary login prompt and permissions. Wallet connection is optional; absence is not wallet consent or proof. Business must allow plain login and must never infer wallet authorization from that login's success. |
| Wallet authorization business | Schema support does not authorize wallet approval. The server must reject a presented `tonconnect_session` before mutating the login aggregate until ownership proof and wallet-session binding are implemented. |
| New `InputTonConnectOauthSession` in old profiles | Unavailable; no constructor alias or byte bridge. |

Only the six request-session projections override the generic field-drop
default; optional response metadata uses the default. This is a reviewed local
compatibility policy, not a claim that Telegram mandates rejecting all wallet
fields. TDesktop `wallet/wallet_ton_connect_login.cpp` exposes a wallet toggle;
`boxes/url_auth_box.cpp` sends the session only when that wallet path is chosen,
otherwise it sends plain login. Missing wallet metadata must not be interpreted
as a completed wallet grant. The server currently emits only plain login
prompts and rejects supplied wallet sessions before approval or replay.

## Verification matrix

Check literal old/new method and result constructor IDs; exact 225–231
request/result round trips without wallet data; reject set and raw-flag request
sessions when projecting to 225–230; allow every combination of optional wallet
response metadata to project to plain login, preserving other consent flags and
the original canonical object; preserve wallet metadata in 231; reject wallet-session input before
server authorization mutation; preserve malformed-input and transactional
cursor/output behavior; retain 228/229 PageBlock storage and other prior
profiles. Run deterministic generation, protocol tests/vet, then downstream
RPC/edge tests and command builds against the published module without a
permanent local `replace`. This protocol change allocates no PTS/QTS, durable
outbox event, or session state.

## 2026-10-21 verification

On Windows/amd64, Go 1.26.2:

- Policy audit: 249 retained entries, zero stale or unresolved entries; the 243
  prior decisions are unchanged and only six request-session guards were added.
- Two final `go generate .` runs reproduced 3,277 generated Go files and the
  execution-plan audit byte for byte. The 74 profile sidecar files occupy
  12,745,415 bytes / 329,354 lines; the 16 MiB / 400,000-line source caps remain.
  The measured typed-wire count is 1,501; its small structural budget is 1,510.
- `go test -count=1 ./gen ./gen/semantic ./cmd/gotdgen ./tg ./tlprofile ./mtproto`
  passed. The new prompt test checks all eight metadata-presence combinations,
  both empty and populated values, all seven profiles, object and RPC-result
  encoders, and canonical input preservation.
- `go vet ./gen ./cmd/gotdgen ./tg ./tlprofile ./mtproto` and
  `go test -race -count=1 ./tlprofile ./mtproto` passed (MinGW GCC for race).
- Downstream `telesrv` RPC / MTProto edge tests, targeted vet, and `./cmd/...`
  builds passed through a temporary `go.work` without changing its module to a
  local replacement. The RPC run explicitly excludes the existing
  `TestWalletLayer230OwnershipAndAddressFlow`: its Unix `0700` file-store check
  fails on Windows before the protocol path. WSL verification was attempted,
  but the Go 1.26.2 toolchain download timed out; no storage guard was weakened.
- Downstream targeted race checks passed for Layer/Profile, Passkey wire E2E,
  PageBlock, RPC-result, and Telegram Login tests with the same explicit
  platform-specific exclusion.

These are local protocol/application checks, not a live client or deployment
acceptance. TDesktop compilation remains paused at the user's request.
