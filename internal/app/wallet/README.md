# Wallet server integration

The router connects the current iOS wallet flow to persistent ownership-checked
account associations and the real Toncenter provider. The modified iOS client
continues to announce layer 228. The external `../td-wallet-230` dependency
contains a documented wallet overlay and generated admission/result codecs;
this does not alias the complete Telegram layer 230 to an older layer.

## Registered RPCs

- `wallet.getProofChallenge`: unpredictable challenge bound to account and
  MTProto authorization, server domain, five-minute expiry.
- `wallet.replaceWallet`: imported initial revision-00 wallet, Ed25519 TON proof,
  actual provider balance, atomic challenge consumption and association. An
  identical signed retry is accepted while its challenge remains valid. A
  superseded/expired challenge is rejected. Replacement uses account SRP checks.
- `wallet.getUserAddresses`: validated InputUser access hashes, stored addresses
  and projected Telegram user entities. Address-only lookup is limited to self.
- `toncenter.performApiRequest`: bounded HTTPS JSON relay to toncenter.com,
  canonical /api/v2/ and /api/v3/ paths, no redirects or application retries.
  JSON provider errors are preserved. Request effects depend on endpoint/payload.

Initial address derivation uses the anchor key. For newly generated 12-word
wallets the anchor and signing key agree; this path does not establish a rotated
wallet's address from its current signing key alone. `inputWalletNew` returns an
explicit capability error instead of inventing a secret or successful state.

## Persistence and configuration

`TELESRV_WALLET_STATE_DIR` selects a private directory; the default is
`data/wallet` relative to the service working directory. The directory must have
no group/other permissions. Per-user JSON files are mode 0600. A process lock
allows one writer; each mutation syncs the file, renames it atomically, and syncs
the directory. Challenge consumption and wallet replacement share one record.
Corrupt records fail closed. No wallet private key or mnemonic is stored here.
Back up the directory together with the server's other persistent files.
There are no wallet PostgreSQL migrations or test databases required.

`TELESRV_WALLET_TONCENTER_API_KEY` optionally supplies the provider API key. The
proof domain uses the server's configured advertise IP. Never log provider keys,
proofs, export tokens, or recovery material.

## Protocol reference and cryptography

The source reference is `Telegram-iOS-wallet/docs/wallet/MTProto-Wallet-Reference.ru.md`
and its `methods.tl` / `responses.tl`. It describes 25 request signatures and
refined backup, TON Connect, transaction and update response layouts. In
particular, secretPhraseParts.dcs is Vector<int>, proofChallenge.payload is a
string, and TonConnectSession optional fields use bits 3, 4 and 5.

`internal/walletbackup` implements the documented client backup crypto: three
XOR shares, Ed25519-to-X25519 conversion, TDLib ECDH/HMAC key derivation,
authenticated AES-CBC envelopes and strict indexed/bare recovery framing. Known
answer fixtures use an independent Python cryptography/OpenSSL implementation.
This crypto component is separate from export-token authorization and holder
server operations. The document distinguishes known wire fields from server
semantics and live compatibility evidence; preserve that distinction when
extending the registered RPCs.

## Validation

```
go test -race ./internal/app/wallet ./internal/walletbackup -count=1
go test ./internal/rpc -run TestWallet -count=1
env -u TELESRV_TEST_POSTGRES_DSN -u TELESRV_RUN_COMPOSE_INTEGRATION go test ./... -count=1
go vet ./...
```

Tests cover cryptographic vectors, malformed envelopes, provider validation,
proof binding/expiry, failed-provider non-mutation, restart persistence,
idempotent retries, competing initial claims, store locking/corruption, and
layer-228 dispatch/result decoding. These tests do not send real funds or prove
an end-to-end device/blockchain transfer or Telegram backup lifecycle.
