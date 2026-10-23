package gen

import (
	"fmt"
	"sort"

	"github.com/iamxvbaba/td/gen/semantic"
)

// layerRPCAction is an explicit generated RPC boundary decision. There is no
// implicit "use the canonical codec" state: every accepted request and every
// result conversion is either statically supported, delegated to a named
// policy adapter, or rejected before canonical bytes can escape.
type layerRPCAction uint8

const (
	layerRPCDirect layerRPCAction = iota
	layerRPCAdapter
	layerRPCReject
	layerRPCUnavailable
)

func (a layerRPCAction) String() string {
	switch a {
	case layerRPCDirect:
		return "direct"
	case layerRPCAdapter:
		return "adapter"
	case layerRPCReject:
		return "reject"
	case layerRPCUnavailable:
		return "unavailable"
	default:
		return fmt.Sprintf("layerRPCAction(%d)", a)
	}
}

// layerRPCNestedProfile says how a transparent generic wrapper chooses the
// exact profile used to decode its nested query. Layers are never clamped.
type layerRPCNestedProfile uint8

const (
	layerRPCInheritProfile layerRPCNestedProfile = iota
	layerRPCProfileFromField
)

func (p layerRPCNestedProfile) String() string {
	switch p {
	case layerRPCInheritProfile:
		return "inherit"
	case layerRPCProfileFromField:
		return "from-field"
	default:
		return fmt.Sprintf("layerRPCNestedProfile(%d)", p)
	}
}

// layerRPCGenericSlot is the identity of a TL generic parameter. QName alone
// is deliberately insufficient: nested wrappers commonly call every slot X.
// Profile + owner + ordinal makes the binding exact and collision-free.
type layerRPCGenericSlot struct {
	Profile int
	Owner   semantic.SemanticKey
	Ordinal int
	Name    string
}

// layerRPCValuePlan attaches owner-scoped generic identity to the reusable
// static value emitter plan. Element mirrors arbitrary vector nesting.
type layerRPCValuePlan struct {
	Value   *layerValuePlan
	Generic *layerRPCGenericSlot
	Element *layerRPCValuePlan
}

// layerRPCFieldPlan describes one encoded value field in an exact method
// profile. Flags words have no value plan and are retained for ordinal checks.
type layerRPCFieldPlan struct {
	Ordinal   int
	Name      string
	Shape     *semantic.FieldShape
	Value     *layerRPCValuePlan
	Admission *layerRPCAdmissionFieldUse
}

// layerRPCResultPlan freezes both sides of a method result conversion. The
// canonical reference is the handler-facing target-schema result; WireRef is
// the exact result grammar expected by this profile's request ID.
type layerRPCResultPlan struct {
	CanonicalRef      *semantic.TypeRef
	WireRef           *semantic.TypeRef
	CanonicalValue    *layerRPCValuePlan
	WireValue         *layerRPCValuePlan
	Action            layerRPCAction
	Adapter           string
	Obligations       []LayerObligation
	ClientAction      layerRPCAction
	ClientAdapter     string
	ClientObligations []LayerObligation
}

// layerRPCWrapperPlan is a transparent generic RPC envelope. QuerySlot and
// ResultSlot must identify the same owner-scoped generic parameter. A wrapper
// is recursively removed while admitting a request; only the innermost method
// becomes the immutable LayerCall.
type layerRPCWrapperPlan struct {
	Slots []layerRPCGenericSlot

	QueryFieldOrdinal int
	QueryFieldName    string
	QuerySlot         *layerRPCGenericSlot
	ResultSlot        *layerRPCGenericSlot

	NestedProfile    layerRPCNestedProfile
	ProfileField     int
	ProfileFieldName string
}

// layerRPCMethodProfile is one exact profile view of a semantic method. A
// method which is not present still has an explicit unavailable entry.
type layerRPCMethodProfile struct {
	Layer                    int
	Availability             LayerAvailability
	Definition               *semantic.Definition
	WireID                   uint32
	Fields                   []layerRPCFieldPlan
	Request                  layerRPCAction
	RequestHook              string
	RequestObligations       []LayerObligation
	ClientRequest            layerRPCAction
	ClientRequestObligations []LayerObligation
	Result                   layerRPCResultPlan
	Wrapper                  *layerRPCWrapperPlan
	Conversion               *LayerFamilyConversion
}

// layerRPCMethodPlan owns exactly one semantic handler registration. All wire
// IDs used by all profiles route here; IDs never create duplicate handlers.
// Canonical is nil only for an old-only method, whose routes are fail-closed
// unless an explicit adapter policy exists.
type layerRPCMethodPlan struct {
	Key        semantic.SemanticKey
	SemanticID uint64
	Constant   string
	Canonical  *layerDefinitionBinding
	Handler    bool
	WireIDs    []uint32
	Profiles   []layerRPCMethodProfile
}

func (m *layerRPCMethodPlan) profile(layer int) *layerRPCMethodProfile {
	if m == nil {
		return nil
	}
	for i := range m.Profiles {
		if m.Profiles[i].Layer == layer {
			return &m.Profiles[i]
		}
	}
	return nil
}

type layerRPCRouteKey struct {
	Layer  int
	WireID uint32
}

// layerRPCRoutePlan is the unique admitted (exact profile, wire ID) route.
type layerRPCRoutePlan struct {
	Layer   int
	WireID  uint32
	Method  *layerRPCMethodPlan
	Profile *layerRPCMethodProfile
}

// layerRPCDefaultWrapperPlan groups every exact generated wrapper route which
// owns one CRC. Default admission must probe every profile-specific prefix:
// the same outer field TypeRefs can have different transitive wire layouts in
// different profiles even when the wrapper's direct BodyShape is unchanged.
// No candidate is authoritative until its prefix reaches invokeWithLayer and
// exact admission under that selected profile validates the complete request.
type layerRPCDefaultWrapperPlan struct {
	WireID uint32
	Key    semantic.SemanticKey
	Layers []int
}

// layerRPCMaxDefaultWrapperCandidatesPerCRC bounds both generated source size
// and the width of one speculative wrapper-probe level. Runtime recursion has
// an independent candidate-attempt budget; neither limit is a semantic decode
// budget shared by candidates.
const layerRPCMaxDefaultWrapperCandidatesPerCRC = 32

// layerRPCModel is a generation-time RPC dispatch projection. Indexes are
// private accelerators and are not emitted as a runtime schema catalog.
type layerRPCModel struct {
	CanonicalLayer  int
	Profiles        []int
	Methods         []layerRPCMethodPlan
	Routes          []layerRPCRoutePlan
	DefaultWrappers []layerRPCDefaultWrapperPlan
	AdmissionFields []layerRPCAdmissionFieldPlan

	methodIndex map[semantic.SemanticKey]*layerRPCMethodPlan
	routeIndex  map[layerRPCRouteKey]*layerRPCRoutePlan
}

func (m *layerRPCModel) method(key semantic.SemanticKey) *layerRPCMethodPlan {
	if m == nil {
		return nil
	}
	return m.methodIndex[key]
}

func (m *layerRPCModel) route(layer int, wireID uint32) *layerRPCRoutePlan {
	if m == nil {
		return nil
	}
	return m.routeIndex[layerRPCRouteKey{Layer: layer, WireID: wireID}]
}

func (m *layerRPCModel) defaultWrapper(wireID uint32) *layerRPCDefaultWrapperPlan {
	if m == nil {
		return nil
	}
	index := sort.Search(len(m.DefaultWrappers), func(index int) bool {
		return m.DefaultWrappers[index].WireID >= wireID
	})
	if index >= len(m.DefaultWrappers) || m.DefaultWrappers[index].WireID != wireID {
		return nil
	}
	return &m.DefaultWrappers[index]
}

// buildLayerRPCModel consumes the generator's one conversion plan, explicit
// canonical binding index, and static TypeRef compiler. It performs no schema
// comparison of its own and cannot invent a compatibility fallback.
func (g *Generator) buildLayerRPCModel() (*layerRPCModel, error) {
	if g == nil || g.schemaSet == nil {
		return nil, fmt.Errorf("gen: layer RPC model requires a schema-set generator")
	}
	conversions := g.LayerConversionPlan()
	if conversions == nil {
		return nil, fmt.Errorf("gen: layer RPC model requires the generator conversion plan")
	}
	wires, err := g.buildLayerWireModel()
	if err != nil {
		return nil, fmt.Errorf("gen: layer RPC wire model: %w", err)
	}
	compiler, err := g.newLayerValueCompilerForWire(wires)
	if err != nil {
		return nil, fmt.Errorf("gen: layer RPC value compiler: %w", err)
	}
	if compiler.conversions != conversions {
		return nil, fmt.Errorf("gen: layer RPC value compiler does not share the generator conversion plan")
	}
	bindings := compiler.bindings
	if bindings == nil {
		return nil, fmt.Errorf("gen: layer RPC canonical bindings are absent")
	}

	model := &layerRPCModel{
		CanonicalLayer: g.schemaSet.CanonicalLayer,
		Profiles:       g.schemaSet.Layers(),
	}
	keys := make([]semantic.SemanticKey, 0)
	for _, key := range g.schemaSet.SortedKeys() {
		if key.Category == semantic.CategoryFunction {
			keys = append(keys, key)
		}
	}
	model.Methods = make([]layerRPCMethodPlan, 0, len(keys))
	semanticIDs := make(map[uint64]semantic.SemanticKey, len(keys))
	for _, key := range keys {
		method, err := buildLayerRPCMethod(g.schemaSet, conversions, compiler, bindings, key)
		if err != nil {
			return nil, fmt.Errorf("gen: layer RPC method %s: %w", key, err)
		}
		if method.SemanticID == 0 {
			return nil, fmt.Errorf("gen: layer RPC method %s hashes to reserved semantic ID zero", key)
		}
		if previous, collision := semanticIDs[method.SemanticID]; collision {
			return nil, fmt.Errorf("gen: layer RPC semantic ID %#016x collides for %s and %s", method.SemanticID, previous, key)
		}
		semanticIDs[method.SemanticID] = key
		model.Methods = append(model.Methods, method)
	}

	model.methodIndex = make(map[semantic.SemanticKey]*layerRPCMethodPlan, len(model.Methods))
	model.routeIndex = make(map[layerRPCRouteKey]*layerRPCRoutePlan)
	for methodIndex := range model.Methods {
		method := &model.Methods[methodIndex]
		if _, duplicate := model.methodIndex[method.Key]; duplicate {
			return nil, fmt.Errorf("gen: layer RPC repeats semantic method %s", method.Key)
		}
		model.methodIndex[method.Key] = method
		for profileIndex := range method.Profiles {
			profile := &method.Profiles[profileIndex]
			if profile.Definition == nil {
				continue
			}
			route := layerRPCRoutePlan{Layer: profile.Layer, WireID: profile.WireID, Method: method, Profile: profile}
			key := layerRPCRouteKey{Layer: route.Layer, WireID: route.WireID}
			if previous := model.routeIndex[key]; previous != nil {
				return nil, fmt.Errorf("gen: layer RPC profile %d wire %#08x routes both %s and %s", route.Layer, route.WireID, previous.Method.Key, method.Key)
			}
			model.Routes = append(model.Routes, route)
			model.routeIndex[key] = &model.Routes[len(model.Routes)-1]
		}
	}
	// Appending Routes can move its backing array, so rebuild pointers after the
	// deterministic sort instead of retaining transient append addresses.
	sort.Slice(model.Routes, func(i, j int) bool {
		if model.Routes[i].Layer != model.Routes[j].Layer {
			return model.Routes[i].Layer < model.Routes[j].Layer
		}
		return model.Routes[i].WireID < model.Routes[j].WireID
	})
	model.routeIndex = make(map[layerRPCRouteKey]*layerRPCRoutePlan, len(model.Routes))
	for routeIndex := range model.Routes {
		route := &model.Routes[routeIndex]
		key := layerRPCRouteKey{Layer: route.Layer, WireID: route.WireID}
		if _, duplicate := model.routeIndex[key]; duplicate {
			return nil, fmt.Errorf("gen: layer RPC repeats route profile %d wire %#08x", route.Layer, route.WireID)
		}
		model.routeIndex[key] = route
	}
	if err := buildLayerRPCDefaultWrappers(model); err != nil {
		return nil, err
	}
	if err := buildLayerRPCAdmissionFields(model); err != nil {
		return nil, err
	}
	return model, nil
}

func buildLayerRPCDefaultWrappers(model *layerRPCModel) error {
	if model == nil {
		return fmt.Errorf("gen: nil layer RPC model while grouping default wrapper probes")
	}
	grouped := make(map[uint32][]int)
	for routeIndex := range model.Routes {
		route := &model.Routes[routeIndex]
		if !layerRPCDefaultWrapperProbeEligible(route) {
			continue
		}
		grouped[route.WireID] = append(grouped[route.WireID], route.Layer)
	}
	model.DefaultWrappers = make([]layerRPCDefaultWrapperPlan, 0, len(grouped))
	for wireID, layers := range grouped {
		sort.Ints(layers)
		if len(layers) > layerRPCMaxDefaultWrapperCandidatesPerCRC {
			return fmt.Errorf("gen: default wrapper %#08x has %d profile candidates, limit %d", wireID, len(layers), layerRPCMaxDefaultWrapperCandidatesPerCRC)
		}
		var key semantic.SemanticKey
		for index, layer := range layers {
			if index != 0 && layers[index-1] == layer {
				return fmt.Errorf("gen: default wrapper %#08x repeats profile %d", wireID, layer)
			}
			route := model.route(layer, wireID)
			if route == nil || route.Method == nil || route.Profile == nil || route.Profile.Wrapper == nil {
				return fmt.Errorf("gen: default wrapper %#08x has an invalid probe route in profile %d", wireID, layer)
			}
			if index == 0 {
				key = route.Method.Key
			} else if route.Method.Key != key {
				return fmt.Errorf("gen: default wrapper %#08x maps to both %s and %s", wireID, key, route.Method.Key)
			}
		}
		model.DefaultWrappers = append(model.DefaultWrappers, layerRPCDefaultWrapperPlan{WireID: wireID, Key: key, Layers: layers})
	}
	sort.Slice(model.DefaultWrappers, func(i, j int) bool {
		return model.DefaultWrappers[i].WireID < model.DefaultWrappers[j].WireID
	})
	return nil
}

func layerRPCDefaultWrapperProbeEligible(route *layerRPCRoutePlan) bool {
	if route == nil || route.Method == nil || route.Profile == nil || route.Profile.Definition == nil || route.Profile.Wrapper == nil {
		return false
	}
	profile := route.Profile
	return profile.Availability != LayerAvailabilityProfileOnly && profile.Request != layerRPCUnavailable &&
		profile.Request != layerRPCReject && profile.RequestHook == "" && profile.Result.Action != layerRPCUnavailable &&
		profile.Result.Action != layerRPCReject
}

func buildLayerRPCMethod(
	set *SchemaSet,
	conversions *LayerConversionPlan,
	compiler *layerValueCompiler,
	bindings *layerBindingIndex,
	key semantic.SemanticKey,
) (layerRPCMethodPlan, error) {
	family := set.Families[key]
	if family == nil {
		return layerRPCMethodPlan{}, fmt.Errorf("semantic family is absent")
	}
	method := layerRPCMethodPlan{
		Key:        key,
		SemanticID: layerSemanticStableID(key),
		Constant:   layerSemanticConstant(key),
		Canonical:  bindings.definition(key),
		Profiles:   make([]layerRPCMethodProfile, 0, len(set.Schemas)),
	}
	if canonical := family.ProfilesByLayer[set.CanonicalLayer]; canonical != nil {
		if method.Canonical == nil || method.Canonical.Definition != canonical.Definition {
			return layerRPCMethodPlan{}, fmt.Errorf("canonical method binding is absent or stale")
		}
	}

	wireIDs := make(map[uint32]struct{})
	for _, layer := range set.Layers() {
		conversionProfile := conversions.Profile(layer)
		if conversionProfile == nil {
			return layerRPCMethodPlan{}, fmt.Errorf("conversion profile %d is absent", layer)
		}
		conversion := conversionProfile.Family(key)
		if conversion == nil || conversion.Canonical != family.ProfilesByLayer[set.CanonicalLayer] || conversion.Profile != family.ProfilesByLayer[layer] {
			return layerRPCMethodPlan{}, fmt.Errorf("conversion profile %d is absent or stale", layer)
		}
		profile, err := buildLayerRPCMethodProfile(set, compiler, method.Canonical, conversion, layer)
		if err != nil {
			return layerRPCMethodPlan{}, fmt.Errorf("profile %d: %w", layer, err)
		}
		if profile.Definition != nil {
			wireIDs[profile.WireID] = struct{}{}
		}
		method.Profiles = append(method.Profiles, profile)
	}
	method.WireIDs = make([]uint32, 0, len(wireIDs))
	for wireID := range wireIDs {
		method.WireIDs = append(method.WireIDs, wireID)
	}
	sort.Slice(method.WireIDs, func(i, j int) bool { return method.WireIDs[i] < method.WireIDs[j] })

	canonicalProfile := method.profile(set.CanonicalLayer)
	if canonicalProfile == nil {
		return layerRPCMethodPlan{}, fmt.Errorf("canonical profile is absent")
	}
	if canonicalProfile.Definition != nil && canonicalProfile.Wrapper == nil {
		if method.Canonical == nil || method.Canonical.Structure == nil {
			return layerRPCMethodPlan{}, fmt.Errorf("ordinary canonical method has no request backend binding")
		}
		method.Handler = true
	}
	return method, nil
}

func buildLayerRPCMethodProfile(
	set *SchemaSet,
	compiler *layerValueCompiler,
	canonicalBinding *layerDefinitionBinding,
	conversion *LayerFamilyConversion,
	layer int,
) (layerRPCMethodProfile, error) {
	profile := layerRPCMethodProfile{
		Layer:         layer,
		Availability:  conversion.Availability,
		Conversion:    conversion,
		Request:       layerRPCUnavailable,
		ClientRequest: layerRPCUnavailable,
		Result: layerRPCResultPlan{
			Action:       layerRPCUnavailable,
			ClientAction: layerRPCUnavailable,
		},
	}
	var canonicalDefinition *semantic.Definition
	if conversion.Canonical != nil {
		canonicalDefinition = conversion.Canonical.Definition
		profile.Result.CanonicalRef = &canonicalDefinition.Result
		canonicalSlots, canonicalSlotByName, err := layerRPCSlots(set.CanonicalLayer, canonicalDefinition)
		if err != nil {
			return layerRPCMethodProfile{}, fmt.Errorf("canonical generic slots: %w", err)
		}
		_ = canonicalSlots
		value, err := compiler.Compile(set.CanonicalLayer, &canonicalDefinition.Result)
		if err != nil {
			return layerRPCMethodProfile{}, fmt.Errorf("canonical result %s: %w", canonicalDefinition.Result.String(), err)
		}
		profile.Result.CanonicalValue, err = scopeLayerRPCValue(value, canonicalSlotByName)
		if err != nil {
			return layerRPCMethodProfile{}, fmt.Errorf("canonical result scope: %w", err)
		}
	}
	if conversion.Profile == nil {
		return profile, nil
	}

	definition := conversion.Profile.Definition
	profile.Definition = definition
	profile.WireID = definition.WireID
	profile.Result.WireRef = &definition.Result
	slots, slotsByName, err := layerRPCSlots(layer, definition)
	if err != nil {
		return layerRPCMethodProfile{}, err
	}
	profile.Fields = make([]layerRPCFieldPlan, 0, len(definition.Fields))
	for fieldIndex := range definition.Fields {
		field := &definition.Fields[fieldIndex]
		fieldPlan := layerRPCFieldPlan{Ordinal: field.Ordinal, Name: field.Name, Shape: field}
		if field.Kind == semantic.FieldValue {
			value, err := compiler.Compile(layer, &field.Type)
			if err != nil {
				return layerRPCMethodProfile{}, fmt.Errorf("field %q: %w", field.Name, err)
			}
			fieldPlan.Value, err = scopeLayerRPCValue(value, slotsByName)
			if err != nil {
				return layerRPCMethodProfile{}, fmt.Errorf("field %q scope: %w", field.Name, err)
			}
		}
		profile.Fields = append(profile.Fields, fieldPlan)
	}
	wireResult, err := compiler.Compile(layer, &definition.Result)
	if err != nil {
		return layerRPCMethodProfile{}, fmt.Errorf("wire result %s: %w", definition.Result.String(), err)
	}
	profile.Result.WireValue, err = scopeLayerRPCValue(wireResult, slotsByName)
	if err != nil {
		return layerRPCMethodProfile{}, fmt.Errorf("wire result scope: %w", err)
	}

	profile.RequestObligations = layerRPCAdmissionObligations(conversion.BodyObligations())
	profile.Request, profile.RequestHook = classifyLayerRPCObligations(profile.RequestObligations, true)
	profile.ClientRequestObligations = layerRPCClientRequestObligations(conversion.BodyObligations())
	profile.ClientRequest, _ = classifyLayerRPCObligations(profile.ClientRequestObligations, true)
	if conversion.Availability == LayerAvailabilityProfileOnly {
		// A historical-only request has no canonical request struct. Even an
		// explicit adapter is the entire admission path; direct is impossible.
		if profile.Request == layerRPCDirect {
			profile.Request = layerRPCReject
		}
		profile.ClientRequest = layerRPCReject
		profile.Result.Action = layerRPCReject
		if profile.Request == layerRPCAdapter && profile.RequestHook != "" {
			// The named old-only adapter owns both request admission and its
			// exact wire result. It does not route through a canonical handler.
			profile.Result.Action = layerRPCAdapter
			profile.Result.Adapter = profile.RequestHook
		}
		profile.Result.Obligations = layerRPCResultObligations(conversion.ResultObligations(), LayerDirectionCanonicalToProfile)
		profile.Result.ClientObligations = layerRPCResultObligations(conversion.ResultObligations(), LayerDirectionProfileToCanonical)
		return profile, nil
	}
	// Present methods are converted field-by-field by their unique wire codec;
	// RequestHook is reserved for a whole-request historical-only adapter.
	profile.RequestHook = ""
	if canonicalBinding == nil || canonicalDefinition == nil {
		return layerRPCMethodProfile{}, fmt.Errorf("present method has no canonical binding")
	}

	profile.Result.Obligations = layerRPCResultObligations(conversion.ResultObligations(), LayerDirectionCanonicalToProfile)
	profile.Result.ClientObligations = layerRPCResultObligations(conversion.ResultObligations(), LayerDirectionProfileToCanonical)
	if canonicalDefinition.Result.Equal(definition.Result) {
		profile.Result.Action = layerRPCDirect
		profile.Result.ClientAction = layerRPCDirect
	} else {
		profile.Result.Action, profile.Result.Adapter = classifyLayerRPCObligations(profile.Result.Obligations, false)
		if profile.Result.Action == layerRPCDirect {
			// A changed result is never silently considered direct, even if a
			// future analyzer accidentally omits its obligation.
			profile.Result.Action = layerRPCReject
		}
		profile.Result.ClientAction, profile.Result.ClientAdapter = classifyLayerRPCObligations(profile.Result.ClientObligations, false)
		if profile.Result.ClientAction == layerRPCDirect {
			// A changed result can only be decoded back to canonical through an
			// explicit profile-to-canonical policy decision.
			profile.Result.ClientAction = layerRPCReject
		}
	}

	wrapper, err := buildLayerRPCWrapper(definition, slots, slotsByName, profile.Fields, profile.Result.WireValue)
	if err != nil {
		return layerRPCMethodProfile{}, err
	}
	profile.Wrapper = wrapper
	return profile, nil
}

func layerRPCSlots(layer int, definition *semantic.Definition) ([]layerRPCGenericSlot, map[string]*layerRPCGenericSlot, error) {
	slots := make([]layerRPCGenericSlot, len(definition.GenericParams))
	byName := make(map[string]*layerRPCGenericSlot, len(slots))
	for ordinal, name := range definition.GenericParams {
		if name == "" {
			return nil, nil, fmt.Errorf("generic slot %d has no name", ordinal)
		}
		slots[ordinal] = layerRPCGenericSlot{Profile: layer, Owner: definition.Key, Ordinal: ordinal, Name: name}
		if _, duplicate := byName[name]; duplicate {
			return nil, nil, fmt.Errorf("generic slot name %q is repeated", name)
		}
		byName[name] = &slots[ordinal]
	}
	return slots, byName, nil
}

func scopeLayerRPCValue(value *layerValuePlan, slots map[string]*layerRPCGenericSlot) (*layerRPCValuePlan, error) {
	if value == nil {
		return nil, fmt.Errorf("nil static value plan")
	}
	result := &layerRPCValuePlan{Value: value}
	if value.Kind == layerValueDynamicGeneric {
		slot := slots[value.GenericParam]
		if slot == nil {
			return nil, fmt.Errorf("generic reference %q has no owner-scoped slot", value.GenericParam)
		}
		result.Generic = slot
	}
	if value.Element != nil {
		var err error
		result.Element, err = scopeLayerRPCValue(value.Element, slots)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func classifyLayerRPCObligations(obligations []LayerObligation, composable bool) (layerRPCAction, string) {
	if len(obligations) == 0 {
		return layerRPCDirect, ""
	}
	action := layerRPCAdapter
	var hook string
	composed := false
	for _, obligation := range obligations {
		resolution := obligation.Resolution
		if !resolution.resolved() || resolution.Action == LayerResolveReject || resolution.Action == LayerResolveUnavailable {
			return layerRPCReject, ""
		}
		if resolution.Hook != "" {
			if composed {
				continue
			}
			if hook != "" && hook != resolution.Hook {
				// Canonical-present request bodies are composed field-by-field by the
				// static wire decoder, so multiple strongly typed hooks are valid.
				// Whole-request historical adapters and result adapters have a single
				// call site and therefore still require one unambiguous hook.
				if composable {
					hook = ""
					composed = true
					continue
				}
				return layerRPCReject, ""
			}
			hook = resolution.Hook
		}
	}
	return action, hook
}

// layerRPCAdmissionObligations selects only profile-to-canonical request
// decisions. Canonical-to-profile obligations belong to client request
// encoding and must not make an otherwise valid server admission fail; Both
// (notably aliases) affects admission as well.
func layerRPCAdmissionObligations(obligations []LayerObligation) []LayerObligation {
	result := make([]LayerObligation, 0, len(obligations))
	for _, obligation := range obligations {
		switch obligation.Direction {
		case LayerDirectionProfileToCanonical, LayerDirectionBoth:
			result = append(result, obligation)
		}
	}
	return result
}

func layerRPCClientRequestObligations(obligations []LayerObligation) []LayerObligation {
	result := make([]LayerObligation, 0, len(obligations))
	for _, obligation := range obligations {
		switch obligation.Direction {
		case LayerDirectionCanonicalToProfile, LayerDirectionBoth:
			result = append(result, obligation)
		}
	}
	return result
}

func layerRPCResultObligations(obligations []LayerObligation, direction LayerObligationDirection) []LayerObligation {
	result := make([]LayerObligation, 0, len(obligations))
	for _, obligation := range obligations {
		if obligation.Direction == direction || obligation.Direction == LayerDirectionBoth {
			result = append(result, obligation)
		}
	}
	return result
}

func buildLayerRPCWrapper(
	definition *semantic.Definition,
	slots []layerRPCGenericSlot,
	slotsByName map[string]*layerRPCGenericSlot,
	fields []layerRPCFieldPlan,
	result *layerRPCValuePlan,
) (*layerRPCWrapperPlan, error) {
	if result == nil || result.Generic == nil {
		return nil, nil
	}
	resultSlot := slotsByName[result.Generic.Name]
	if resultSlot == nil {
		return nil, fmt.Errorf("generic result %q has no exact slot", result.Generic.Name)
	}
	queryOrdinal := -1
	var query *layerRPCFieldPlan
	for fieldIndex := range fields {
		field := &fields[fieldIndex]
		if field.Value == nil || field.Value.Generic == nil || *field.Value.Generic != *resultSlot {
			continue
		}
		if query != nil {
			return nil, fmt.Errorf("transparent generic result slot %s has multiple query fields %q and %q", resultSlot.Name, query.Name, field.Name)
		}
		query = field
		queryOrdinal = field.Ordinal
	}
	if query == nil {
		return nil, fmt.Errorf("generic result slot %s has no matching query field", resultSlot.Name)
	}
	wrapper := &layerRPCWrapperPlan{
		Slots:             slots,
		QueryFieldOrdinal: queryOrdinal,
		QueryFieldName:    query.Name,
		QuerySlot:         query.Value.Generic,
		ResultSlot:        result.Generic,
		NestedProfile:     layerRPCInheritProfile,
		ProfileField:      -1,
	}
	if definition.Key.QName != "invokeWithLayer" {
		return wrapper, nil
	}
	for fieldIndex := range definition.Fields {
		field := &definition.Fields[fieldIndex]
		if field.Kind != semantic.FieldValue || field.Name != "layer" {
			continue
		}
		if field.Condition != nil || field.Type.Kind != semantic.TypePrimitive || field.Type.QName != "int" {
			return nil, fmt.Errorf("invokeWithLayer layer field is not an unconditional int")
		}
		if field.Ordinal >= queryOrdinal {
			return nil, fmt.Errorf("invokeWithLayer layer field must precede query")
		}
		wrapper.NestedProfile = layerRPCProfileFromField
		wrapper.ProfileField = field.Ordinal
		wrapper.ProfileFieldName = field.Name
		return wrapper, nil
	}
	return nil, fmt.Errorf("invokeWithLayer has no layer field")
}

// layerRPCRequestNode is a compact test/emitter representation of already
// read wrapper headers. The generated decoder performs the same recursion
// while consuming fields directly from bin.Buffer.
type layerRPCRequestNode struct {
	WireID      uint32
	NestedLayer int
	Nested      *layerRPCRequestNode
}

type layerRPCFrozenBinding struct {
	Slot   layerRPCGenericSlot
	Result *layerRPCResultPlan
}

// layerRPCFrozenCall is the immutable innermost call descriptor. Profile and
// complete Result are copied from the exact admitted method profile, never
// read again from mutable connection state.
type layerRPCFrozenCall struct {
	Profile  int
	Method   semantic.SemanticKey
	WireID   uint32
	Result   *layerRPCResultPlan
	Adapter  string
	Bindings []layerRPCFrozenBinding
}

func (m *layerRPCModel) freezeCall(profile int, request *layerRPCRequestNode) (*layerRPCFrozenCall, error) {
	if m == nil || request == nil {
		return nil, fmt.Errorf("gen: layer RPC freeze requires a request")
	}
	if !layerRPCProfileSupported(m.Profiles, profile) {
		return nil, fmt.Errorf("gen: layer RPC exact profile %d is unsupported", profile)
	}
	return m.freeze(profile, request, 0)
}

func (m *layerRPCModel) freeze(profile int, request *layerRPCRequestNode, depth int) (*layerRPCFrozenCall, error) {
	if depth > 256 {
		return nil, fmt.Errorf("gen: layer RPC wrapper nesting exceeds 256")
	}
	route := m.route(profile, request.WireID)
	if route == nil {
		return nil, fmt.Errorf("gen: layer RPC profile %d wire %#08x is unavailable", profile, request.WireID)
	}
	if route.Profile.Request == layerRPCReject || route.Profile.Request == layerRPCUnavailable {
		return nil, fmt.Errorf("gen: layer RPC profile %d wire %#08x is fail-closed (%s)", profile, request.WireID, route.Profile.Request)
	}
	if wrapper := route.Profile.Wrapper; wrapper != nil {
		if request.Nested == nil {
			return nil, fmt.Errorf("gen: layer RPC wrapper %s has no nested query", route.Method.Key)
		}
		nestedProfile := profile
		if wrapper.NestedProfile == layerRPCProfileFromField {
			nestedProfile = request.NestedLayer
			if !layerRPCProfileSupported(m.Profiles, nestedProfile) {
				return nil, fmt.Errorf("gen: layer RPC wrapper %s selects unsupported exact profile %d", route.Method.Key, nestedProfile)
			}
		}
		call, err := m.freeze(nestedProfile, request.Nested, depth+1)
		if err != nil {
			return nil, err
		}
		call.Bindings = append(call.Bindings, layerRPCFrozenBinding{Slot: *wrapper.ResultSlot, Result: call.Result})
		return call, nil
	}
	if request.Nested != nil {
		return nil, fmt.Errorf("gen: layer RPC ordinary method %s unexpectedly has a nested query", route.Method.Key)
	}
	if !route.Method.Handler {
		if route.Profile.Request == layerRPCAdapter && route.Profile.RequestHook != "" &&
			route.Profile.Result.Action == layerRPCAdapter {
			return &layerRPCFrozenCall{
				Profile: profile,
				Method:  route.Method.Key,
				WireID:  request.WireID,
				Result:  &route.Profile.Result,
				Adapter: route.Profile.RequestHook,
			}, nil
		}
		return nil, fmt.Errorf("gen: layer RPC method %s has no canonical handler", route.Method.Key)
	}
	if route.Profile.Result.Action == layerRPCReject || route.Profile.Result.Action == layerRPCUnavailable {
		return nil, fmt.Errorf("gen: layer RPC result for profile %d method %s is fail-closed (%s)", profile, route.Method.Key, route.Profile.Result.Action)
	}
	return &layerRPCFrozenCall{
		Profile: profile,
		Method:  route.Method.Key,
		WireID:  request.WireID,
		Result:  &route.Profile.Result,
	}, nil
}

func layerRPCProfileSupported(profiles []int, layer int) bool {
	index := sort.SearchInts(profiles, layer)
	return index < len(profiles) && profiles[index] == layer
}
