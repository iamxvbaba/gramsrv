# layerwire 操作手册（多 Layer 向后兼容）

> 本文是 **怎么操作**（runbook）。**为什么这么设计**见 [`docs/layer-compat-220-227-design.md`](../../../docs/layer-compat-220-227-design.md)。
> 改这个包前请先读完本文 + 设计文档。所有命令都从 **telesrv 模块根目录**执行。

## 这个包是干什么的

让 telesrv 同时正确服务 **Layer 220–227** 的客户端，而业务 handler / gotd 永远只跑 canonical(227)、一行不改。
- **出站**：把 227 对象降级成老客户端能解的 wire 形态（`Transcode`）。
- **入站**：把老客户端发来的旧构造器升级成 227 请求，再交正常 gotd dispatcher（`UpgradeInbound`）。

两个正交维度（**务必分清**）：
- **官方层漂移**：构造器在 layer N 的字段/CRC 与 227 不同。真值=官方 TDesktop `api.tl` 各层。**自动从 schema 生成**。
- **客户端构造器漂移**：某客户端（如 DrKLO Android）手维护的 TL 实际发了个旧 layer 的官方构造器，但它声明的整体 layer 却是新的。真值=该客户端源码（TLRPC.java）。**声明在 `client-drift.tl` / `client_aliases.go`**。

## 文件地图

| 文件 | 角色 | 谁改 |
|---|---|---|
| `schema/canonical-227.tl` | **embed**，运行期 walker 的 227 字段布局（= gotd `td/_schema/tdesktop.tl` 的副本） | gotd 升级时 re-sync |
| `_schema/layer-2NN.tl` | 历史层官方 schema（从 TDesktop git 抽，**仅生成期用**，下划线=不编译/不 embed） | 升级/下探 floor 时抽取 |
| `schema/client-drift.tl` | **声明式**：客户端发的旧构造器老布局（body 与 227 不同的） | 发现客户端漂移时 +1 行 |
| `schema/routable-compat.tl` | **仅结构预检**：已有 RPC fallback adapter 的非 canonical wire 布局（当前只含 4 个 DrKLO theme 构造器）；与 canonical 图合并后完整 walk，但不自动升级 | 收敛既有手写 adapter 时维护，禁止借此新增业务 fallback |
| `client_aliases.go` | 客户端漂移里 **body 与 227 字节一致**的，纯 `老CRC→227CRC` | 发现纯换 CRC 漂移时 +1 条 |
| `tables_gen.go` | **生成产物**（勿手改）：官方层降级表 + 入站升级表 + 新类型集 | 跑 `gen` 重生成 |
| `gen/main.go` | 生成器：对拍 schema、证明机械性、产 `tables_gen.go` | 升级逻辑变更时 |
| `layout.go` `walk.go` `tables.go` | 通用解释器（读/丈量/递归转码）| 核心，少动 |
| `fallback.go` | 出站手写兜底（结构性 / 227-only 类型）| CoverageGate 报缺时 |
| `inbound.go` | 入站通用升级引擎 + `fieldConverters` + `driftFieldRenames` | DriftCoverage 报缺时 |

## 核心命令

```bash
# 复核 schema 差异数字（不改文件）
go run ./internal/compat/layerwire/gen -report

# 重新生成 tables_gen.go（官方层漂移表）
go run ./internal/compat/layerwire/gen -emit internal/compat/layerwire/tables_gen.go

# 全部护栏（漂移门禁 + 对各历史层真实 schema 对拍 + 性能基准）
go test ./internal/compat/layerwire/
go test ./internal/compat/layerwire/ -run '^$' -bench . -benchmem   # 性能

# 改完务必：
gofmt -w internal/compat/layerwire/ && go build ./... && go vet ./internal/...
```

---

## 操作 1：gotd 升级（canonical layer 上移，例 227 → 230）

> gotd bump 是显式任务（见 AGENTS.md 铁律 #6）。canonical schema 随之变化，按下列步骤同步。

1. **同步 canonical schema**（gotd 的就是实际编出的字节）：
   ```bash
   cp ../td/_schema/tdesktop.tl internal/compat/layerwire/schema/canonical-230.tl
   rm internal/compat/layerwire/schema/canonical-227.tl
   ```
   改 `layout.go`：`//go:embed schema/canonical-230.tl`、`const CanonicalLayer = 230`。
2. **把原 canonical 层并入历史 TO 层**：现在 227/228/229 成了"老层"，从 TDesktop git 抽进 `_schema/`（见文末「抽取 api.tl@N」）。
3. **改生成期常量**：`gen/main.go` 的 `canonicalLayer = 230`。（`supportedFloor` 不变。）
4. **重生成 + 复核**：
   ```bash
   go run ./internal/compat/layerwire/gen -report   # 看 changed/new 数字是否合理
   go run ./internal/compat/layerwire/gen -emit internal/compat/layerwire/tables_gen.go
   ```
5. **跑护栏、按报告 triage**：
   ```bash
   go test ./internal/compat/layerwire/
   ```
   - `TestCoverageGate` 失败 = 出现了 telesrv 可达但没处理的 227(新 canonical)-only / 结构性类型 → 去 `fallback.go` 加 by-type 兜底或结构性转换，或确认 telesrv 不发就加进 `unemittedAllowlist`（`gate_test.go`，附理由）。
   - 生成器 `-report` 里 "structural" 列出的需手写转换（参照 `fallback.go transcodePollAnswerVoters`）。
6. `gofmt`/`build`/`vet`/全量 `go test`。真机 220/老层/新层各一台回归。

## 操作 2：下探 floor（支持更老客户端，例 220 → 215）

1. 从 TDesktop git 抽 `layer-215.tl … layer-219.tl` 进 `_schema/`（见文末）。
2. 改 `supportedFloor`：`layout.go` 的 `SupportedFloor = 215` **和** `gen/main.go` 的 `supportedFloor = 215`（两处都要）。
3. `go run ... -emit ...` 重生成 → `go test`。
4. 越老的层结构性差异越多，按 `TestCoverageGate` / 生成器 report triage（同操作 1 第 5 步）。

## 操作 3：新增「客户端构造器漂移」（最常见）

触发：某客户端发的旧构造器导致 `NOT_IMPLEMENTED`（入站）或对端渲染异常；或主动审计客户端源码发现它发旧 CRC。

1. **拿到老构造器的精确 TL 定义**：
   - 优先看该客户端源码的序列化（DrKLO Android：`TMessagesProj/.../TLRPC.java` 的 `serializeToStream`，按 `writeInt32/writeString/...` 顺序还原字段）。
   - 或它是某旧 layer 官方构造器：`git -C ../tdesktop/tdesktop log -S"#<crc>" -- <api.tl>` 找到所在层，再取该层定义。
2. **判断 body 是否与 227 字节一致**：
   - **一致**（只是 CRC 不同；典型=227 只追加了 flag-gated 可选字段而客户端不设）→ 往 `client_aliases.go clientMethodAliases` 加 `0x<老CRC>: 0x<227CRC>`。
   - **不一致**（缺 flags 整数 / 字段类型变了 / 缺必填字段）→ 往 `schema/client-drift.tl` 加**一行老布局 TL**（用 method 的限定名，结果类型随便填合法值，引擎只按名字匹配 227）。
3. **跑测试**：
   ```bash
   go test ./internal/compat/layerwire/ -run TestInbound
   ```
   - 绿 = 通用引擎已能自动升级（复制共享字段 + 插 flags=0 + 按 kind 补默认）。**完事**。
   - `TestInboundDriftCoverage` 报 `needs converter A->B` = 有字段类型变更 → 往 `inbound.go fieldConverters` 加一条 `"A->B"`（可复用，参照 `Vector<int>->Vector<InputMessage>`）。
   - 报 `field X not defaultable` 或字段**改名** → 往 `inbound.go driftFieldRenames` 加 `"<method>\x00<227字段>": "<老字段>"`（参照 `bots.exportBotToken\x00bot`）。
4. **绝不**为此写一个新的 `handleLegacyXxx` 解码 handler——统一走数据 + 通用引擎。`routable-compat.tl` 只给既存 DrKLO theme fallback 补 dispatcher 前结构门禁，不是新增 adapter 的入口。

## 操作 4：出站 `TestCoverageGate` 失败

说明 telesrv 现在会发某个"经保留字段可达"的 227-only / 结构性类型，但没处理。
- 该类**有同抽象类的老成员**可降级 → `fallback.go` 加 `newTypeFallbacksByType["<抽象类>"]`（如 `PageBlock→pageBlockUnsupported`）。
- 是**结构性变更类型**且 telesrv 真发 → `fallback.go structuralTransforms` 加手写转换。
- **确认 telesrv 不发** → 加进 `gate_test.go unemittedAllowlist`（**必须附理由**，引用出站构造器审计）。

---

## 护栏：每个测试拦什么

| 测试 | 拦截 |
|---|---|
| `TestWalkConsumesCanonicalObjects` | 解释器读不全某个 227 类型(字段布局漏) |
| `TestTranscodeDowngradeValid` | 降级输出对 220..226 **真实 schema** 解析失败/有残留字节 |
| `TestTranscodeChangedTypeNestedInUnchangedContainer` | 「外层 CRC 不变但内含变更类型」被误整段拷贝 |
| `TestCoverageGate` | 出站 227-only/结构性类型无 handler 又不在 allowlist（gotd bump/客户端升级引入新形态时报） |
| `TestInboundDriftCoverage` | `client-drift.tl` 某条目无法自动升级（缺 converter/rename） |
| `TestInboundBodyTransforms` / `TestInboundCRCSwaps` | 入站升级产出不是合法 227 请求 |
| `TestNegotiatedLayerStickyContract` | layer 协商的 `(layer, ok)` 契约（避免缓存驱逐把老客户端误降回 227） |

**运行期 fail-safe**：出站遇未处理类型 → `Transcode` 返错 → 边界记日志并发 canonical 字节（连接存活，单对象可能渲染异常）。入站遇未覆盖旧 CRC → 落 gotd dispatcher → `NOT_IMPLEMENTED`（须按 AGENTS.md #5 进 compatibility trace + 矩阵）。**护栏的意义就是把这些从"线上撞见"提前到"提交期/测试期发现"。**

## 抽取 api.tl@N（操作 1/2 用）

```bash
TD=../tdesktop/tdesktop
APITL=Telegram/SourceFiles/mtproto/scheme/api.tl

# 找 layer N 的提交（取最后一个写入 "// LAYER N" 的；可能有初版+修订，选最全的）
git -C "$TD" log --oneline -S"// LAYER N" -- "$APITL"

# 抽取（务必校验文件末尾确是 "// LAYER N"）
git -C "$TD" show <commit>:"$APITL" > internal/compat/layerwire/_schema/layer-N.tl
tail -1 internal/compat/layerwire/_schema/layer-N.tl   # 应为: // LAYER N
```

各层→commit 对照见设计文档 §3 表（220..227 的 canonical 抽取点）。`gotd/tl` 解析器能直接吃 TDesktop api.tl，无需改格式。

## 稳态心法

**喂新 schema（gotd 或更老层）→ 跑 `gen` + `go test` → 护栏吐出短清单 → 人只处理新出现的 fallback / 结构性 / converter / rename。** 不再有"运行时撞 NOT_IMPLEMENTED 再手写 handler"。
