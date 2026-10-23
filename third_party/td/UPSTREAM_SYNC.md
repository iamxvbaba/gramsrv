# Upstream selective-replay log

This file records reviewed `gotd/td` ranges. A candidate head is not the fork's
adopted upstream base until the selected changes pass every fork/downstream
gate, enter `fork/main`, and receive a new immutable release tag.

## 2026-07-23: `9e5dde8b` to `eb1128fb`

- Adopted base before this audit:
  `9e5dde8b2a752dfb989b8ce0fe57069c8435775a`
- Candidate upstream head:
  `eb1128fbda671ff2b211f2d8010d65f26fb307c1`
- Reviewed range:
  `9e5dde8b2a752dfb989b8ce0fe57069c8435775a..eb1128fbda671ff2b211f2d8010d65f26fb307c1`
- Scope: 37 commits, including 21 non-merge commits.

### Selected

| Upstream commit/group | Decision |
|---|---|
| `046ff0e81` + `96a39f8f2` | Replay the final AES-256-IGE helper design only: use `github.com/gotd/ige v0.3.0` and `DecryptAES256Blocks`. Do not retain the superseded in-tree ARM C implementation. |
| `a7e2e260d` | Replay the root `golang.org/x/{crypto,net,sync,tools}` group and its tidy-selected transitive versions. |
| `b20ae3bed` | Replay the `_tools` `x/tools` update and matching `x/mod`/`x/sync` versions. |
| `f3bd84e5b`, `57f72062e`, `d5834544b` | Replay the Goldmark, ogen and klauspost/compress maintenance releases. Deterministic generation must remain byte-identical. |
| `61ca13a02`, `1dc4dcd75`, `4af1f884d` | Replay the coordinated Pion ICE/WebRTC/interceptor maintenance releases and versions selected by `go mod tidy`. |
| `ef769c7f0` | Replay the `k0kubun/pp` maintenance release. |
| `9bb0e3912` | Replay `actions/setup-go@v7` in workflows retained by this fork. The upstream schema-update workflow is intentionally absent here. |

### Skipped

| Upstream commit/group | Decision |
|---|---|
| `9a6ccb7e6`, `2b2010633` | Nested examples are outside the fork release boundary and still require upstream-module contrib types. |
| `1f3840549` | Client-side stalled-request tracing does not affect the server/runtime paths consumed by `telesrv`. |
| `b33d3ba4d`, `09b95f11a` | Layer 228 is already the canonical fork schema. The normalized Telegram schema body is identical; provenance-only upstream changes do not trigger regeneration. |
| `3b9d10373`, `f23e7907f` | MTProto-over-HTTP and `http_wait` are client transport features, not required by the `telesrv` server dependency. |
| `1fdbc4a89` | Client query-helper behavior is outside the fork's current server/runtime requirement. |
| `ede0407cc` | Client `initConnection` Layer overriding is not the fork's exact multi-Layer server codec path and does not provide historical request encoding. |

### Validation

The selected replay passed:

- `go mod verify` in the root and `_tools` modules;
- deterministic `go generate` and `go generate ./...` with zero generated diff;
- targeted `crypto`, `proto/codec` and `transport` tests and vet;
- `go test --timeout 15m ./...`;
- `CGO_ENABLED=1 go test --timeout 15m -race ./...`;
- downstream `telesrv/internal/mtprotoedge` tests through a temporary `go.work`
  using this local fork.
