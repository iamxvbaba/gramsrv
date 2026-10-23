# Multi-layer TL generation

`gotdgen` treats this directory as one versioned schema universe, not as a
collection of independent generators. `manifest.json` selects the canonical
Layer and records the upstream repository, commit, blob and SHA-256 for every
input. `policy.json` contains the reviewed decisions which cannot be inferred
from TL structure alone.

The maintained exact-profile window starts at Layer 225. Layers below 225 are
intentionally absent from the manifest and fail closed at runtime; extending
the window requires importing the exact schema and reviewing its obligations.

The generated `tg` package has exactly one stable canonical Go model (the
manifest's canonical Layer). Historical schemas exist only while gotdgen builds
the transitive semantic closure. The generated `tlprofile` sidecar maps an
exact profile and wire ID to a deduplicated static execution plan. RPC results,
active updates and difference responses all use `Call.EncodeResult`,
`EncodeObject` or `FrozenObject.Encode`; there is no push-specific codec and no
fallback to canonical bytes when a projection fails.

## Codec generation optimization invariants

Profile branches are shared per emitted operation, including its rejection
reason; a decode or preflight difference must not duplicate an identical
encode body. Class encoding groups by canonical constructor dispatch, while
decoding retains each exact wire ID. This is generation-time source sharing,
not a runtime schema interpreter or a change to the reviewed execution plans.
Optional nested-field buffers are allocated only when the original shared flag
group is present. Canonical vector preallocation is capped with
`min(length, bin.PreallocateLimit)` for both outer and inner vectors.

| Scenario | Required invariant and verification |
|---|---|
| Profiles 225–230, same body or retagged child | Identical wire bytes and exact profile acceptance; per-operation grouping tests and existing wire goldens |
| Optional field absent, present, or present-empty | Preserve flags and owned data; no temporary buffer allocation for absent fields |
| Shared flag group, projected subtree, malformed explicit flag | Preserve one-shot hooks, drop/reject policy, atomic groups and output rollback |
| Vector lengths 0, 1, 1023, 1024, 1025 and 4096 | Same decoded values, bounded preallocation, including nested vectors |
| Truncated, negative, oversized or over-budget vector | Preserve rejection and exact decoder input rollback before materialization |
| Regeneration and downstream use | Deterministic generated output, unchanged schema/policy/plan audit, source budgets and local `telesrv` protocol tests through a temporary `go.work` |

### 2026-10-12 optimization verification

Against the published `v1.4.0` baseline, the 74 generated `tlprofile` files
shrank from 12,980,108 bytes / 341,928 lines to 12,551,318 bytes / 328,077 lines.
The generated-source test rejects duplicate bodies within each exact-profile
switch. A second `go generate . ./gen ./tdp/internal` reproduced all 3,275
generated Go files byte for byte; schema, policy, execution-plan audit and
module dependencies remained unchanged.

Same-fixture benchmarks on Go 1.27.0, darwin/arm64, Apple M5 Max, one CPU,
median of three 300 ms runs (dialogs use a preallocated output buffer):

| Profile 230 operation | Time before → after (µs/op) | Bytes before → after (B/op) | Allocations before → after |
|---|---:|---:|---:|
| 100-entry dialogs, absent optional fields | 23.463 → 10.899 | 21,600 → 0 | 900 → 0 |
| 100-entry dialogs, FromID / ReplyTo / entities present | 39.200 → 30.557 | 39,200 → 15,200 | 1,900 → 900 |
| Exact decode of 1,024 integers | 10.582 → 9.490 | 25,264 → 8,248 | 14 → 3 |
| Exact decode of 4,096 integers | 43.641 → 41.594 | 128,304 → 96,312 | 18 → 7 |

Validation passed:

- `go test -count=1 ./gen ./cmd/gotdgen ./tlprofile ./gen/example ./bin ./mtproto`
  and generated consumers `./tg ./tg/e2e ./mt ./tdp/... ./tgtrace`.
- `go test -race -count=1 ./tlprofile ./mtproto ./gen/example` and `go vet`
  for the generator, command, profile, example, buffer and MTProto packages.
- Local `telesrv` `./internal/rpc` and `./internal/mtprotoedge` tests through a
  temporary `go.work`; the server's published dependency was not changed.
- 15,420 differential observations across profiles 225–230 matched `v1.4.0`
  byte output, errors, remaining input and output rollback. These include
  9,537 successful round trips, 5,872 rejected fixtures and 11 pre-existing
  panics from invalid zero-value fixtures; they are a behavior comparison,
  not a claim that every zero-value constructor is valid.
- Literal optional-field wire oracles, shared flags, projection/drop/reject,
  one-shot hooks, historical keyboards, PageBlock storage profiles, malformed
  vectors, decode limits and source/plan budgets remain covered by the suites.

These are local codec and server test results; no desktop UI session or
deployment was part of this optimization validation.

## Synchronizing `gotd/td` upstream

Upstream is an evidence and patch source, not a branch that is merged wholesale
into the fork. Every synchronization uses this sequence:

1. Fetch upstream and record the current base, candidate new base, and exact
   commit range. Classify each non-merge commit or cohesive feature group as
   `selected` or `skipped`, with a short reason.
2. Replay selected source/runtime changes on a dedicated fork branch. Preserve
   the `github.com/iamxvbaba/td` module boundary and resolve upstream import-path
   differences in source. Keep required dependency changes in the same reviewed
   patch set as the feature that needs them.
3. Never copy, cherry-pick, or conflict-merge upstream-generated `tg/*_gen.go`
   files. They are an oracle for schema/codegen comparison only. Fork-generated
   outputs must come exclusively from the fork's manifest, semantic IR, policy,
   templates, and sparse AOT emitter.
4. Compare schema Layer, normalized SHA-256, and body before importing it. An
   identical schema with different generator/source header provenance is a
   no-op for canonical generation. A genuinely new or changed schema must use
   the import and policy-audit workflow below.
5. Run deterministic generation, targeted and full fork tests, `go vet`, source
   budgets, cross-Layer wire oracles, and downstream `telesrv` validation. Only
   after all gates pass may documentation adopt the candidate upstream base and
   a new immutable fork tag be published.

This policy applies to every future upstream update. A full upstream
merge/rebase into the release line is not a shortcut for this review.

## Adding a Layer

1. Import the exact source from a local upstream checkout. `gotdgen` discovers
   `// LAYER N`, finds the last commit on `HEAD` which changed the manifest's
   source path, verifies that commit is reachable from a fetched ref of the
   configured upstream remote and verifies the bytes against that commit's Git
   object, computes SHA-256, writes `layer-N.tl`,
   updates manifest provenance/canonical selection, and rebuilds
   `_schema/telegram.tl` with the locked overlays. No network is used:

   The import stages and syncs the complete output set before replacing any
   tracked file. Layer and canonical schema files are replaced first;
   `manifest.json` is replaced last as the commit marker. A replacement error
   rolls every target back to the complete previous set.

   ```powershell
   go run ./cmd/gotdgen --schema-manifest _schema/layers/manifest.json `
     --schema-import ..\tdesktop\tdesktop\Telegram\SourceFiles\mtproto\scheme\api.tl
   ```

   Fetch the upstream checkout before importing. A copied source must still be
   verified against a local checkout; pass
   `--schema-import-git-dir <checkout>` and, when needed, the exact
   `--schema-import-commit <40-hex>`. Replacing different bytes/provenance for
   an already-recorded Layer additionally requires `--schema-import-replace`.
2. Audit the existing policy without weakening an existing decision. Unlike
   normal generation, audit mode reports stale keys instead of aborting before
   a review artifact can be produced:

   ```powershell
   go run ./cmd/gotdgen --schema-manifest _schema/layers/manifest.json `
     --layer-policy _schema/layers/policy.json `
     --layer-policy-audit _schema/layers/policy.audit.json `
     --layer-policy-merge _schema/layers/policy.next.json
   ```

   The audit has three deterministic sections: `retained` (exact key still
   valid), `stale` (never copied forward), and `new` (empty-action skeleton).
   The merged file contains retained plus new entries and deliberately fails
   normal generation until every new action is reviewed.
3. Review every new obligation. Mechanical ID/body reuse needs no policy.
   Removed fields, replacements, old-only definitions, changed RPC results and
   unavailable update constructors require an explicit `reject`, `drop`,
   `default`, `alias`, `project` or typed `adapter` decision. Copy only reviewed
   entries into `policy.json`; stale keys and unresolved obligations are hard
   generation errors.
4. Regenerate and verify:

   ```powershell
   go generate .
   go test ./gen ./cmd/gotdgen ./tg ./tlprofile
   go vet ./gen ./cmd/gotdgen
   git diff --check
   ```

Adding a schema-identical next profile therefore only extends generated profile
metadata and switch cases. A genuinely changed schema stops at generation time
until its semantic policy and typed hook are supplied.

### Layer 231 review record

Canonical Layer 231 comes from official Telegram Desktop `dev@78ceaa548a`;
exact profiles 225–230 remain available. The [Layer 231 review](LAYER231_REVIEW.md)
locks provenance, URL-auth constructor changes and the request/response
distinction: a supplied wallet OAuth session cannot be silently dropped, while
optional wallet prompt metadata may be omitted for ordinary login on old
clients. The policy has 249 entries (243 existing plus six request guards).
No PTS/update or MTProto transport schema changes are part of this release.

### Layer 230 review record

Canonical Layer 230 is frozen from Telegram Desktop 7.3.1. Profiles 225–229
remain generated. The [Layer 230 review](LAYER230_REVIEW.md) records source
provenance, the 243 reviewed policy decisions, verification text adapters,
explicit rejection of the removed Firebase credential, and validation gates.
Historical-only constructors may now be explicitly rejected before typed
materialization; this never creates a fake canonical adapter.

### Layer 229 review record

Layer 229 was imported from Telegram Desktop commit
`db41bb8c753bf43b452a7b01c47f079ceb3dce09`; `manifest.json` locks the exact
Git blob and normalized SHA-256. The schema is not a layer-number-only update.
Its keyboard model consolidates reply-button constructors into
`keyboardButton` + `ButtonType`, splits inline buttons into
`KeyboardInlineButton`, and changes inline row/result structure. It also adds
new ephemeral-message fields/RPCs and privacy-sensitive fields on gift and
forwarding paths.

The reviewed compatibility scenarios are:

| Scenario | Layer 229 canonical behavior | Layers 225--228 behavior |
|---|---|---|
| Reply keyboard button | Encode/decode the consolidated constructor directly | Select exactly one historical constructor through typed bidirectional adapters |
| Inline keyboard rows | Use `KeyboardInlineButtonRow` and `KeyboardInlineButton` | Use the historical `KeyboardButtonRow` shape through a bounded static structural adapter |
| New privacy/meaning flags | Preserve the field | Reject encoding when the older wire cannot represent it; never silently drop it |
| New constructors/RPCs | Admit through the canonical profile | Remain unavailable, or use an explicit reviewed update projection where policy permits |
| Historical constructor input | Not part of the canonical class | Decode only in the exact historical profile and materialize the canonical Layer 229 value |

All historical keyboard selection is transactional and fail-closed: zero or
multiple matching constructors is an error. No runtime schema, reflection
walker, or bytes-to-bytes compatibility bridge is introduced.

The `telegram/message/markup` helpers follow the Layer 229 split as well:
reply-keyboard builders accept `tg.KeyboardButton` values, while inline
builders accept `tg.KeyboardInlineButton` values. Their nested `ButtonType`
and `InlineButtonType` constructors are populated by the helpers, so invalid
reply/inline mixtures are rejected by the Go type system before encoding.

## Client-private RPC overlays

`client_rpc_overlays` in `manifest.json` is intentionally separate from the
official Layer universe. Each entry locks a derived TL file by SHA-256 and the
upstream client commit/source blobs from which its serializers were audited.
gotdgen binds every private function to the current canonical function by
semantic name and emits a direct CRC switch plus static typed field decoders.
Nested constructors always decode with the connection's exact Layer profile.

Schema-identical fields map automatically. Renames, type converters and
explicitly consumed/dropped client-only fields are declared per method in the
manifest. Missing optional fields and required primitive/vector zero values can
be generated mechanically; a missing required object, result mismatch,
unmapped source field, unknown converter, CRC collision or stale rule aborts
generation. Upgrading canonical Layer therefore needs only the ordinary Layer
import and regeneration when the comparison remains mechanical; otherwise the
generator stops for an explicit review instead of guessing.

The current DrKLO inputs keep the 17 long-lived core private RPCs and four
theme RPCs in separate overlays. They generate into the `tlprofile` sidecar.
Production adapters call `UnknownMethodView.AdaptClientRPCOverlay`, which
shares the outer request decode budget and authoritative canonical admission
bridge. Overlay IDs are classified per exact profile, so a private ID that
collides with a historical official constructor is never globally stolen.
There is no runtime TL parser, schema map, reflection walker, or client-driven
Layer selection in this path.

## Runtime invariants

- `Dispatcher.Admit` preserves a caller-frozen exact profile and rejects a
  conflicting inner `invokeWithLayer`. `Dispatcher.AdmitDefault` instead
  treats the caller profile as inherited fallback state for naked RPCs: the first `invokeWithLayer`
  anywhere in the transparent wrapper chain may replace it, after which
  repeated selectors must agree. The admitted request reports the effective
  profile separately from explicit profile evidence.
- With neither a frozen nor inherited profile, the dispatcher accepts only
  `invokeWithLayer` or a terminal RPC whose request graph and complete result
  `TypeRef` graph were proven at generation time to be wire-identical in every
  loaded profile. Such an invariant terminal (for example
  `auth.bindTempAuthKey`) publishes neither an effective profile nor profile
  evidence; its canonical codec route remains an internal representative.
- Request admission consumes the complete outer wire value and freezes the
  exact result `TypeRef`, wire ID and request digest.
- `gzip_packed` is a transparent protocol `Object` envelope, not an RPC and
  not a schema-driven wrapper. When `AdmissionOptions.ExpandGZIP` is present,
  `Admit*WithOptions` may encounter it before profile selection, between
  generated wrappers or at the terminal, expand it
  under the caller's per-wrapper resource reservation, and continue through
  the same exact-profile route. The original compressed bytes remain the
  authoritative wire size/digest while the decoded terminal determines the
  canonical semantic identity. Expansion never creates a canonical fallback,
  dynamic TL walker or bytes-to-bytes transcode path.
- Every successful or failed expander call returns an optional reservation
  release that admission invokes exactly once in reverse order. Expanded
  buffers are released only after generated wrapper metadata and the typed
  request graph have copied their values. One wrapper is hard-capped at
  10 MiB; nested envelopes share the admission's depth and cumulative
  `MaxWireBytes` limit. A server remains responsible for a process-wide and
  transport-frame-wide budget before supplying the expander.
- Wrapper metadata is preserved outer-to-inner. Wrappers with session,
  ordering or update-suppression meaning must be consumed explicitly.
- If at least one generated wrapper is completely decoded but its innermost
  constructor misses the exact-profile RPC switch, admission may return
  `UnknownTerminalError`. It carries the exact profile, terminal wire
  ID and complete remaining terminal size plus a private immutable
  outer-to-inner wrapper identity chain. Naked unknown constructors and
  malformed wrappers never receive this proof. The proof is not admission:
  an edge consumer must whitelist both a closed terminal schema and every
  wrapper semantic before handling a non-API service terminal; otherwise it
  must fail closed. No decoder cursor or runtime TL walker is exposed.
- Flags are rebuilt by `(flags word, bit)` groups. Present-empty slices remain
  present; every value-bearing member of a set bit is encoded atomically.
- Encoding is transactional. Rejected adapters, partial projections, malformed
  flags and size limits leave the caller's buffer unchanged.
- Decode depth, wrapper vectors, wrapper metadata `Object` graphs and terminal
  vectors are bounded before allocation. Generated wrapper parsers and the
  terminal share one admission scan state, so envelope depth and aggregate
  elements cannot reset at a wrapper or gzip boundary. Named wrapper metadata
  is checked by its generated exact `TypeRef`/class scanner before generic
  materialization; being any known constructor is insufficient. Wrapper
  vectors also prove that declared elements fit the remaining wire. TL strings
  and byte strings must fit the protocol's 24-bit length header.
- Frozen values may be prepared once per exact profile/call identity. Wire bytes
  are never reused across unequal profiles or result `TypeRef` identities.
- Proactive updates still require a real frozen target profile. Only a result
  bound to a generated wire-invariant RPC may be encoded before profile
  evidence exists; the exception cannot be used as a generic unknown-profile
  fallback.

The source emitter coalesces identical body, preflight and result execution
plans. Dispatch still has an exact switch for every `(profile, wire ID)` route,
but byte-identical routes share generated helpers; identical wrapper probes
share one probe helper as well. Sparse family/class helpers and scanner class
helpers are retained by exact generated-call reachability, while dirty wire
encoding keeps the reachable typed bare body without duplicating atomic
wrappers already owned by its family/class entry point. Admission field routes
remain static switches grouped by wire/profile plan. The current source gate
rejects any regenerated `tg/tl_layer*_gen.go` and caps the complete generated
`tlprofile` sidecar at
16 MiB / 400k lines. Adding an unchanged future Layer therefore grows route
metadata instead of cloning the full TL catalog. None of these paths uses
reflection, a runtime schema walker or a dynamic schema map.
