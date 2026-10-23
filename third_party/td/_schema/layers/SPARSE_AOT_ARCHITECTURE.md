# Sparse AOT multi-layer architecture

Status: implemented in the v1.1 protocol line and advanced through Layer 229.
The dense Layer 225--229
backend and its public `tg.Layer*` surface have been deleted. This document is
normative for generated code. The schema
manifest and policy remain the normative wire inputs.

## 1. Goal and compatibility boundary

The multi-layer backend preserves external Telegram wire compatibility, not
the public Go API introduced by the first fork implementation. In particular:

- `tg` remains the canonical Layer 229 Go model and stays byte-for-byte equal
  to a single-schema canonical generation;
- `tlprofile` is the only public multi-layer package;
- Layer 225--229 request, result and object bytes remain exact, including
  flags, bare/boxed form, vectors, generic bindings and method result types;
- generation may break every existing `tg.Layer*` API and telesrv migrates in
  the same protocol line;
- no generated client facade or per-method server facade is retained.

The old backend generated a typed codec for every schema node and then exposed
the same catalog through TypeRef, wire, family, class, dynamic, client and
server surfaces. That made a four-layer delta look like a second complete TL
implementation. The replacement generates code by distinct execution plan.

## 2. Package topology

```text
github.com/iamxvbaba/td/tg
    canonical Layer 229 types and codecs only

github.com/iamxvbaba/td/tlprofile
    public Profile, Admission, Call and object/result encoding API
    handwritten bounded dispatcher/runtime plus generated sparse scanners,
    projections, historical codecs, route metadata and semantic constants

github.com/iamxvbaba/td/gen
    generation-time semantic closure, execution-plan construction and emitters
```

Consumers import `tg` for canonical values and `tlprofile` for all exact-profile
operations. No runtime package contains the input schema, a general TypeRef
walker or a mutable schema registry.

## 3. Generation-time IR

The complete manifest, semantic universe and reviewed conversion policy exist
only while gotdgen runs. For every `(profile, semantic family)` the generator
computes a transitive body state:

```text
direct       same wire ID and complete nested wire graph
retag        different wire ID, identical complete nested body graph
rewrite      body, projection, flags policy or nested graph differs
unavailable  absent in the target profile
policy       explicit adapter/reject/default/project decision
```

Body changes seed a reverse dependency closure. Dependencies include exact
bare constructors, every member of boxed abstract classes, recursively nested
vectors, and dynamic `Object`/generic parents. Method result graphs are frozen
independently from request bodies.

An emitted execution plan is identified by a stable digest over:

```text
operation kind
+ physical shape digest
+ transitive route digest
+ canonical projection digest
+ flags policy digest
+ complete result TypeRef digest (for result plans)
+ generic slot layout
+ resource/preflight policy digest
```

Wire IDs and Layer numbers route to a plan; they are not plan identities. Two
different wire IDs or profiles with the same digest share one emitted body.

## 4. Runtime model

Runtime routing is static and bounded:

```text
(profile, wire ID) --generated switch--> plan ID --static call--> code
```

There is no schema loading, reflection, dynamic `(Layer, CRC)` map, TypeRef
walker or bytes-to-bytes transcode. A canonical-direct plan calls the canonical
typed codec. A retag plan consumes/emits the historical ID and calls the
canonical bare codec. Only rewrite/policy plans contain generated field code.

Inbound RPC admission runs a non-materializing generated preflight plan before
the typed decode. It enforces wire-byte, depth, vector, aggregate-element and
field policy limits before canonical decoders can allocate client-controlled
collections. Preflight plans are also deduplicated by execution-plan digest.
Generated generic-wrapper parsers and the terminal use the same bounded scan
state. Envelope depth, wrapper metadata `Object` graphs and all vectors share
one aggregate/depth budget, with remaining-wire checks before allocation.
Every named wrapper metadata field calls its generated exact class scanner;
the generic materializer runs only after the declared `TypeRef` is proven.

`gzip_packed` may transparently replace the `Object` at any point in that
wrapper chain. It is recognized by constructor only; an explicit
caller-supplied `AdmissionOptions.ExpandGZIP` capability owns decompression and
resource reservation. `AdmitUnprofiledWithOptions` may therefore discover
`invokeWithLayer` evidence inside a top-level compressed envelope. Admission
then resumes the generated exact route on the expanded single TL object. The
full compressed input remains the exact wire identity, the typed terminal
remains the semantic identity, and releases run exactly once after all decoded
values have been copied. This adds no runtime schema, reflection walker or
canonical-bytes fallback.

`Admission` freezes:

- effective profile and whether it came from explicit evidence;
- semantic method identity and exact request wire ID;
- immutable result plan ID and wire-invariance proof;
- canonical request object;
- wrapper proof and frozen wrapper field values;
- canonical and wire request digests/sizes.

`Call.EncodeResult` is the only RPC result encoder. `EncodeObject` is the only
general proactive-update/object encoder. Both fail closed when the target
profile cannot represent the canonical value.

## 5. Public API

The stable entry points are intentionally capability-oriented:

```go
type Profile int
type Limits struct { /* stable, comparable bounded decode limits */ }
type AdmissionOptions struct { Limits Limits; ExpandGZIP GZIPExpander }
type Admission struct { /* immutable, opaque */ }
type Call struct { /* immutable, opaque */ }
type Result interface { bin.Encoder /* plus immutable call metadata */ }
type Dispatcher struct { /* semantic handlers and bounded hooks */ }

func NewDispatcher() *Dispatcher
func (d *Dispatcher) Register(method SemanticID, handler Handler) error
func (d *Dispatcher) Admit(profile Profile, body *bin.Buffer, limits Limits) (Admission, error)
func (d *Dispatcher) AdmitDefault(profile Profile, body *bin.Buffer, limits Limits) (Admission, error)
func (d *Dispatcher) AdmitUnprofiled(body *bin.Buffer, limits Limits) (Admission, error)
func (d *Dispatcher) AdmitWithOptions(profile Profile, body *bin.Buffer, options AdmissionOptions) (Admission, error)
func (d *Dispatcher) AdmitDefaultWithOptions(profile Profile, body *bin.Buffer, options AdmissionOptions) (Admission, error)
func (d *Dispatcher) AdmitUnprofiledWithOptions(body *bin.Buffer, options AdmissionOptions) (Admission, error)
func (d *Dispatcher) Dispatch(ctx context.Context, admission Admission) (Result, error)

func (a Admission) Call() Call
func (c Call) EncodeResult(value any, out *bin.Buffer) error

func EncodeObject(profile Profile, value bin.Object, out *bin.Buffer) error
func DecodeObject(profile Profile, in *bin.Buffer, limits Limits) (bin.Object, error)
func FreezeObject(value bin.Object) (*FrozenObject, error)
```

Wrapper inspection, request identity and preflight hooks remain available as
small opaque capabilities required by telesrv. Semantic dispatch is one
handler keyed by generated semantic ID. The generator does not emit `OnX`,
`LayerClient`, public arbitrary TypeRef descriptors, or one facade per Layer.

## 6. Forbidden states

- clamping an unknown/future Layer to a generated profile;
- falling back to canonical bytes after historical encoding fails;
- reusing prepared bytes across profiles or across different result plans;
- declaring direct reuse from local CRC/body equality without transitive proof;
- materializing a request before generated resource preflight succeeds;
- mutating an admitted call when a connection later changes Layer;
- accepting a constructor absent from the exact target class/profile;
- retaining the old `tg.Layer*` API as a compatibility implementation.

## 7. Generated evidence and gates

Production source contains only runtime plans and compact route metadata.
The sparse typed closure is pruned at generated-function granularity: family
and class helpers survive only when another emitted function calls them, and a
dirty wire retains its typed bare body without the unused boxed/bare atomic
wrappers because the reachable family or class entry point already owns that
transactional boundary. The scanner likewise emits only class scanners named
by a reachable field scan body. Admission field registrations are statically
chunked and field-plan routes are grouped by wire/profile plan; neither
optimization introduces a runtime registry.
Review evidence is emitted as deterministic JSON under `_schema/layers/audit/`
and consumed by tests. At minimum it records profile/family classification,
closure reason, plan digest, route memberships, policy decisions and result
plan identity.

Required gates:

1. canonical `tg` generation has byte-zero diff from canonical-only generation;
2. a second generation has zero diff;
3. all Layer 225--229 request/result/object goldens match their frozen exact
   fixtures (with the v1.0.0 oracle retained for Layers 225--228);
4. malformed, over-limit, truncated and trailing input fails transactionally;
5. source-size and plan-count budgets fail on dense-regeneration regressions;
6. repository scans reject runtime schema/walker/transcode/canonical-fallback
   paths;
7. telesrv contains no import-time or identifier dependency on `tg.Layer*`.

The Layer 225--229 implementation currently emits 74 generated `tlprofile`
files, 11,792,413 bytes and 312,628 lines. The hard source budget is 16 MiB
and 400k lines. The generation audit currently records 12,120 routes sharing
1,173 body plans, 980 preflight plans and 692 result plans; 10,595 routes reuse
the canonical implementation directly. `tg/tl_layer*_gen.go` must remain
empty. These numbers are evidence and regression bounds, not an API promise.
