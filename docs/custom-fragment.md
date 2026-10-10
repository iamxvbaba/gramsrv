# CustomFragment gift withdrawal

CustomFragment replaces the old local-only completion page behind
`payments.getStarGiftWithdrawalUrl` with a self-hosted TON mainnet mint flow.
The RPC's existing SRP/2FA check remains the gate: its returned withdrawal URL
is a short-lived bearer capability. Username minting is not implemented.

## Contracts

The upstream TON reference implementation requested for new collections is
vendored at [`contracts/nft-v1.1`](../contracts/nft-v1.1). It is the
`ton-blockchain/acton-contracts` `nft-v1.1` package (including its wrappers,
scripts, and tests), with only a local Acton manifest pinned to `1.1.0` so it
can be built reproducibly alongside gramsrv.

```sh
cd contracts/nft-v1.1
acton build NftCollection
acton test tests
```

That reference collection deliberately accepts deployment messages only from
its admin address. The already deployed **InvGram Gifts** collection remains
the signed-mint compatibility collection for existing withdrawal URLs; it is
not replaced in place (TON contract addresses and storage layouts are
immutable). A new upstream collection can be deployed for a future relayer
flow after its admin wallet and collection address are configured.

The contracts in `contracts/customfragment` are an idiomatic typed Tolk rewrite
of the minting subset of Telegram's GPL-3.0 TeleMint contracts. They use a new
storage layout and deterministic item address scheme, so they are not address-
compatible with the original FunC contracts.

```sh
cd contracts/customfragment
acton build
acton test --coverage --coverage-format text
acton wrapper CustomFragmentCollection
acton wrapper CustomFragmentItem
```

Deploy `CustomFragmentItem`, then deploy `CustomFragmentCollection` with:

- `subwalletId`: the configured uint32 (default `1196573005`, ASCII `GRAM`);
- `publicKey`: the Ed25519 public key printed by telesrv on startup;
- `itemCode`: the compiled CustomFragment item code;
- collection metadata and royalty cells for the deployment.

Deployment is intentionally an operator action: telesrv will never deploy a
mainnet contract or generate/replace the mint authority key automatically.
The included `deploy-mainnet` Acton script deploys the collection from the
global `customfragment-deployer` wallet and takes the authority seed only from
`CUSTOM_FRAGMENT_SIGNING_SEED`.

## Server configuration

Set the `TELESRV_CUSTOM_FRAGMENT_*` variables documented in `.env.example` and
enable the public Web listener. The collection address must be a TON basechain
mainnet address. Keep the signing key outside the repository with restrictive
filesystem permissions.

Set `TELESRV_CUSTOM_FRAGMENT_PUBLIC_BASE_URL` to a dedicated HTTPS host served
by the same Web listener. It must differ from the native client's internal link
host; otherwise Android may parse `/gift-withdrawal/{token}` as a username link
instead of opening the browser.

The browser sends a wallet-bound, five-minute authorization to TON Connect. The
server does not accept the wallet callback as proof. It derives the expected NFT
address with `get_nft_address_by_index`, reads `get_nft_data` through proof-
checking mainnet lite servers, and atomically marks the gift exported only when
the index, collection and owner all match.

The claim path uses the same fail-closed compare-and-swap rule: the database
owner observed when a TON Proof challenge is issued must still be the owner
when the single-use challenge is committed. A concurrent ownership sync or
second claim therefore returns a conflict instead of overwriting a newer
projection.

Public routes:

- `/custom-fragment` — service and collection information;
- `/gift-withdrawal/{token}` — private mint page returned by the TL method;
- `/custom-fragment/tonconnect-manifest.json` — TON Connect manifest;
- `/custom-fragment/collection.json` — TEP-64 metadata for **InvGram Gifts**;
- `/custom-fragment/metadata/gift/{slug}.json` — TEP-64 metadata;
- `/custom-fragment/media/gift/{slug}.png` — rendered gift poster with its
  backdrop, tiled pattern, and model rest frame;
- `/custom-fragment/media/gift/{slug}.lottie.json` — the selected model's raw
  Lottie JSON, cached in memory and suitable for client-side animation.

The public Web listener also exposes `/botfather` and `/stickers` as local
Telegram-style mini apps. They use only relative `/api/miniapps/*` requests;
creation is delegated to the existing bot/files services and requires signed
Telegram `initData`. A newly generated bot token is returned once over the
server response and is not retained by the page.

The media routes and all metadata URLs use
`TELESRV_CUSTOM_FRAGMENT_PUBLIC_BASE_URL`, so CustomFragment can live on a
dedicated HTTPS host while the main server keeps its normal public URL.
