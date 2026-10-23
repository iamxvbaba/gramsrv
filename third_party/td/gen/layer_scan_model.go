package gen

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/iamxvbaba/td/gen/semantic"
)

// layerScanModel is the generation-only projection for the allocation-free
// exact-profile wire scanner. It deliberately contains emitted statements,
// not a runtime schema graph.
type layerScanModel struct {
	MaxWireBytes             int
	MaxDepth                 int
	MaxVectorElements        int
	MaxAggregateElements     int
	DefaultWireBytes         int
	DefaultDepth             int
	DefaultVectorElements    int
	DefaultAggregateElements int
	Bodies                   []layerScanBody
	WireBuckets              []layerScanWireBucket
	Classes                  []layerScanClass
	Bares                    []layerScanBare
	FieldPlans               []layerScanFieldPlan
	FieldWires               []layerScanFieldWire
	CompleteAdmissionFields  [][]uint64
	FailedAdmissionFields    []layerScanAdmissionField
}

type layerScanBody struct {
	ID     int
	Source string
	Reject bool
}

type layerScanProfileBody struct {
	Layers []int
	Body   int
}

type layerScanWire struct {
	WireID uint32
	Hex    string
	Groups []layerScanProfileBody
}

type layerScanWireBucket struct {
	Index int
	Wires []layerScanWire
}

type layerScanProfileIDs struct {
	Layers  []int
	WireIDs []uint32
}

type layerScanClass struct {
	QName  string
	Suffix string
	Groups []layerScanProfileIDs
}

type layerScanBare struct {
	QName  string
	Suffix string
	Groups []layerScanProfileBody
}

type layerScanFieldSlot struct {
	Ordinal int
	ID      uint64
	Metric  string
}

type layerScanFieldPlan struct {
	ID    int
	Slots []layerScanFieldSlot
}

type layerScanFieldRoute struct {
	WireID uint32
	Hex    string
	Plan   int
}

type layerScanFieldPlanGroup struct {
	Layers []int
	Plan   int
}

type layerScanFieldWire struct {
	WireID uint32
	Hex    string
	Groups []layerScanFieldPlanGroup
}

type layerScanAdmissionField struct {
	ID       uint64
	Complete bool
	Failure  string
}

type layerScanEmitter struct {
	bodyBySource map[string]int
	bodies       []layerScanBody
	temp         int
}

func (g *Generator) buildLayerScanModel() (*layerScanModel, error) {
	if g == nil || g.schemaSet == nil {
		return nil, fmt.Errorf("gen: layer scanner requires a schema set")
	}
	layers := g.schemaSet.Layers()
	conversions := g.LayerConversionPlan()
	emitter := &layerScanEmitter{bodyBySource: make(map[string]int)}
	bodyByDefinition := make(map[layerScanDefinitionKey]int)
	wireRoutes := make(map[uint32]map[int]int)
	bareQNames := make(map[string]struct{})
	classQNames := make(map[string]struct{})

	for _, layer := range layers {
		schema := g.schemaSet.Schemas[layer]
		if schema == nil {
			return nil, fmt.Errorf("gen: layer scanner schema %d is absent", layer)
		}
		definitions := append([]*semantic.Definition(nil), schema.Definitions...)
		sort.Slice(definitions, func(i, j int) bool {
			return definitions[i].Key.String() < definitions[j].Key.String()
		})
		for _, definition := range definitions {
			source, err := emitter.definition(definition, bareQNames, classQNames)
			if err != nil {
				return nil, fmt.Errorf("gen: layer %d scanner %s: %w", layer, definition.Key, err)
			}
			rejected := layerRejectedHistoricalType(conversions.Profile(layer).Family(definition.Key))
			if rejected {
				source = "return fmt.Errorf(\"historical constructor rejected by exact-profile policy\")\n"
			}
			body := emitter.addBody(source)
			emitter.bodies[body].Reject = rejected
			key := layerScanDefinitionKey{Layer: layer, Key: definition.Key}
			bodyByDefinition[key] = body
			routes := wireRoutes[definition.WireID]
			if routes == nil {
				routes = make(map[int]int)
				wireRoutes[definition.WireID] = routes
			}
			if previous, duplicate := routes[layer]; duplicate && previous != body {
				return nil, fmt.Errorf("gen: layer scanner wire %#08x profile %d has conflicting bodies", definition.WireID, layer)
			}
			routes[layer] = body
		}
	}

	model := &layerScanModel{
		MaxWireBytes:             layerCodecMaximumWireBytes,
		MaxDepth:                 layerCodecMaximumDepth,
		MaxVectorElements:        layerCodecMaximumVectorSize,
		MaxAggregateElements:     layerCodecMaximumAggregateElements,
		DefaultWireBytes:         layerCodecDefaultWireBytes,
		DefaultDepth:             layerCodecDefaultDepth,
		DefaultVectorElements:    layerCodecDefaultVectorSize,
		DefaultAggregateElements: layerCodecDefaultAggregateElements,
		Bodies:                   emitter.bodies,
	}
	rpc, err := g.buildLayerRPCModel()
	if err != nil {
		return nil, fmt.Errorf("gen: layer scanner RPC field plans: %w", err)
	}
	if err := buildLayerScanFieldPlans(model, rpc); err != nil {
		return nil, err
	}

	wireIDs := make([]uint32, 0, len(wireRoutes))
	for id := range wireRoutes {
		wireIDs = append(wireIDs, id)
	}
	sort.Slice(wireIDs, func(i, j int) bool { return wireIDs[i] < wireIDs[j] })
	const bucketCount = 64
	model.WireBuckets = make([]layerScanWireBucket, bucketCount)
	for index := range model.WireBuckets {
		model.WireBuckets[index].Index = index
	}
	for _, id := range wireIDs {
		wire := layerScanWire{WireID: id, Hex: fmt.Sprintf("%08x", id), Groups: groupLayerBodies(wireRoutes[id])}
		bucket := int(id % bucketCount)
		model.WireBuckets[bucket].Wires = append(model.WireBuckets[bucket].Wires, wire)
	}

	classNames := sortedLayerScanNames(classQNames)
	for _, qname := range classNames {
		groups := make(map[string]*layerScanProfileIDs)
		var order []string
		for _, layer := range layers {
			schema := g.schemaSet.Schemas[layer]
			keys := schema.ConstructorsByClass[qname]
			ids := make([]uint32, 0, len(keys))
			for _, key := range keys {
				definition := schema.ByKey[key]
				if definition == nil {
					return nil, fmt.Errorf("gen: layer scanner class %q profile %d has missing constructor %s", qname, layer, key)
				}
				ids = append(ids, definition.WireID)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			identity := layerScanWireIDsKey(ids)
			group := groups[identity]
			if group == nil {
				group = &layerScanProfileIDs{WireIDs: ids}
				groups[identity] = group
				order = append(order, identity)
			}
			group.Layers = append(group.Layers, layer)
		}
		entry := layerScanClass{QName: qname, Suffix: layerScanNameSuffix(qname)}
		for _, identity := range order {
			entry.Groups = append(entry.Groups, *groups[identity])
		}
		model.Classes = append(model.Classes, entry)
	}

	bareNames := sortedLayerScanNames(bareQNames)
	for _, qname := range bareNames {
		routes := make(map[int]int)
		key := semantic.SemanticKey{Category: semantic.CategoryType, QName: qname}
		for _, layer := range layers {
			if body, ok := bodyByDefinition[layerScanDefinitionKey{Layer: layer, Key: key}]; ok {
				routes[layer] = body
			}
		}
		if len(routes) == 0 {
			return nil, fmt.Errorf("gen: layer scanner bare constructor %q has no exact profile", qname)
		}
		model.Bares = append(model.Bares, layerScanBare{
			QName: qname, Suffix: layerScanNameSuffix(qname), Groups: groupLayerBodies(routes),
		})
	}
	return model, nil
}

func buildLayerScanFieldPlans(model *layerScanModel, rpc *layerRPCModel) error {
	if model == nil || rpc == nil {
		return fmt.Errorf("gen: nil scanner or RPC model for field plans")
	}
	const admissionFieldGroupSize = 16
	for _, field := range rpc.AdmissionFields {
		if !field.Complete {
			model.FailedAdmissionFields = append(model.FailedAdmissionFields, layerScanAdmissionField{
				ID: field.ID, Complete: false, Failure: field.Failure,
			})
			continue
		}
		if len(model.CompleteAdmissionFields) == 0 || len(model.CompleteAdmissionFields[len(model.CompleteAdmissionFields)-1]) == admissionFieldGroupSize {
			model.CompleteAdmissionFields = append(model.CompleteAdmissionFields, nil)
		}
		index := len(model.CompleteAdmissionFields) - 1
		model.CompleteAdmissionFields[index] = append(model.CompleteAdmissionFields[index], field.ID)
	}
	planByKey := make(map[string]int)
	routesByWire := make(map[uint32]map[int]int)
	seenRoute := make(map[string]int)
	for methodIndex := range rpc.Methods {
		method := &rpc.Methods[methodIndex]
		for profileIndex := range method.Profiles {
			profile := &method.Profiles[profileIndex]
			if !layerRPCAdmissionProfileRoutable(method, profile) {
				continue
			}
			var slots []layerScanFieldSlot
			for ordinal := range profile.Fields {
				admission := profile.Fields[ordinal].Admission
				if admission == nil {
					continue
				}
				slots = append(slots, layerScanFieldSlot{
					Ordinal: ordinal, ID: admission.ID, Metric: layerScanMetricConstant(admission.Metric),
				})
			}
			if len(slots) == 0 {
				continue
			}
			var key strings.Builder
			for _, slot := range slots {
				fmt.Fprintf(&key, "%d:%016x:%s;", slot.Ordinal, slot.ID, slot.Metric)
			}
			plan, ok := planByKey[key.String()]
			if !ok {
				plan = len(model.FieldPlans)
				planByKey[key.String()] = plan
				model.FieldPlans = append(model.FieldPlans, layerScanFieldPlan{ID: plan, Slots: slots})
			}
			routeKey := fmt.Sprintf("%d:%08x", profile.Layer, profile.WireID)
			if previous, duplicate := seenRoute[routeKey]; duplicate && previous != plan {
				return fmt.Errorf("gen: scanner field route profile %d wire %#08x has conflicting plans %d and %d", profile.Layer, profile.WireID, previous, plan)
			}
			seenRoute[routeKey] = plan
			profiles := routesByWire[profile.WireID]
			if profiles == nil {
				profiles = make(map[int]int)
				routesByWire[profile.WireID] = profiles
			}
			profiles[profile.Layer] = plan
		}
	}
	wireIDs := make([]uint32, 0, len(routesByWire))
	for wireID := range routesByWire {
		wireIDs = append(wireIDs, wireID)
	}
	sort.Slice(wireIDs, func(i, j int) bool { return wireIDs[i] < wireIDs[j] })
	for _, wireID := range wireIDs {
		model.FieldWires = append(model.FieldWires, layerScanFieldWire{
			WireID: wireID,
			Hex:    fmt.Sprintf("%08x", wireID),
			Groups: groupLayerFieldPlans(routesByWire[wireID]),
		})
	}
	return nil
}

func groupLayerFieldPlans(routes map[int]int) []layerScanFieldPlanGroup {
	planLayers := make(map[int][]int)
	var plans []int
	for layer, plan := range routes {
		if _, ok := planLayers[plan]; !ok {
			plans = append(plans, plan)
		}
		planLayers[plan] = append(planLayers[plan], layer)
	}
	sort.Ints(plans)
	groups := make([]layerScanFieldPlanGroup, 0, len(plans))
	for _, plan := range plans {
		sort.Ints(planLayers[plan])
		groups = append(groups, layerScanFieldPlanGroup{Layers: planLayers[plan], Plan: plan})
	}
	return groups
}

func layerScanMetricConstant(metric layerRPCAdmissionMetricKind) string {
	switch metric {
	case layerRPCAdmissionMetricVectorLength:
		return "tlFieldMetricVectorLength"
	case layerRPCAdmissionMetricBytesLength:
		return "tlFieldMetricBytesLength"
	case layerRPCAdmissionMetricInt32:
		return "tlFieldMetricInt32"
	default:
		return "tlFieldMetricInvalid"
	}
}

type layerScanDefinitionKey struct {
	Layer int
	Key   semantic.SemanticKey
}

func (e *layerScanEmitter) addBody(source string) int {
	if id, ok := e.bodyBySource[source]; ok {
		return id
	}
	id := len(e.bodies)
	e.bodyBySource[source] = id
	e.bodies = append(e.bodies, layerScanBody{ID: id, Source: source})
	return id
}

func (e *layerScanEmitter) definition(definition *semantic.Definition, bare, classes map[string]struct{}) (string, error) {
	e.temp = 0
	var out strings.Builder
	flags := make(map[string]string)
	profileVariable := "profile"
	for fieldIndex := range definition.Fields {
		field := &definition.Fields[fieldIndex]
		if field.Kind == semantic.FieldFlagsWord {
			name := e.next("flags")
			fmt.Fprintf(&out, "%s, err := b.Uint32()\nif err != nil { return err }\n_ = %s\n", name, name)
			flags[field.Name] = name
			continue
		}
		if field.Condition != nil && field.Condition.PresenceOnly {
			continue
		}
		var target *strings.Builder = &out
		if field.Condition != nil {
			word, ok := flags[field.Condition.Word]
			if !ok {
				return "", fmt.Errorf("field %q refers to unavailable flags word %q", field.Name, field.Condition.Word)
			}
			fmt.Fprintf(&out, "if %s&(uint32(1)<<%d) != 0 {\n", word, field.Condition.Bit)
		}
		if definition.Key.Category == semantic.CategoryFunction && definition.Key.QName == "invokeWithLayer" && field.Name == "layer" {
			name := e.next("layer")
			fmt.Fprintf(target, "%s, err := b.Int32()\nif err != nil { return err }\n", name)
			profileVariable = "Profile(" + name + ")"
		} else {
			metric, observed := layerRPCAdmissionMetric(&field.Type, field.Kind)
			if definition.Key.Category != semantic.CategoryFunction {
				observed = false
			}
			var source string
			var err error
			if observed {
				source, err = e.metricTypeRef(&field.Type, profileVariable, bare, classes, fieldIndex, metric)
			} else {
				source, err = e.typeRef(&field.Type, profileVariable, bare, classes)
			}
			if err != nil {
				return "", fmt.Errorf("field %q TypeRef %s: %w", field.Name, field.Type.String(), err)
			}
			target.WriteString(source)
		}
		if field.Condition != nil {
			if metric, observed := layerRPCAdmissionMetric(&field.Type, field.Kind); observed && definition.Key.Category == semantic.CategoryFunction {
				fmt.Fprintf(&out, "} else {\nif err := state.observe(%d, %s, false, 0); err != nil { return err }\n}\n", fieldIndex, layerScanMetricConstant(metric))
			} else {
				out.WriteString("}\n")
			}
		}
	}
	return out.String(), nil
}

func (e *layerScanEmitter) metricTypeRef(ref *semantic.TypeRef, profile string, bare, classes map[string]struct{}, ordinal int, metric layerRPCAdmissionMetricKind) (string, error) {
	if ref == nil {
		return "", fmt.Errorf("nil observed TypeRef")
	}
	switch metric {
	case layerRPCAdmissionMetricInt32:
		name := e.next("value")
		return fmt.Sprintf("%s, err := b.Int32()\nif err != nil { return err }\nif err := state.observe(%d, tlFieldMetricInt32, true, int64(%s)); err != nil { return err }\n", name, ordinal, name), nil
	case layerRPCAdmissionMetricBytesLength:
		name := e.next("length")
		return fmt.Sprintf("%s, err := tlScanBytesLength(b)\nif err != nil { return err }\nif err := state.observe(%d, tlFieldMetricBytesLength, true, int64(%s)); err != nil { return err }\n", name, ordinal, name), nil
	case layerRPCAdmissionMetricVectorLength:
		if ref.Kind != semantic.TypeVector || ref.Arg == nil {
			return "", fmt.Errorf("observed vector metric has non-vector TypeRef %s", ref.String())
		}
		body, err := e.typeRef(ref.Arg, profile, bare, classes)
		if err != nil {
			return "", err
		}
		length := e.next("length")
		index := e.next("i")
		boxed := ref.QName == "Vector" && !ref.Bare && !ref.Percent
		return fmt.Sprintf("%s, err := tlScanVector(%s, b, state, %t)\nif err != nil { return err }\nif err := state.observe(%d, tlFieldMetricVectorLength, true, int64(%s)); err != nil { state.leave(); return err }\nfor %s := 0; %s < %s; %s++ {\n%s}\nstate.leave()\n", length, profile, boxed, ordinal, length, index, index, length, index, body), nil
	default:
		return "", fmt.Errorf("unsupported observed metric %s", metric)
	}
}

func (e *layerScanEmitter) typeRef(ref *semantic.TypeRef, profile string, bare, classes map[string]struct{}) (string, error) {
	if ref == nil {
		return "", fmt.Errorf("nil TypeRef")
	}
	switch ref.Kind {
	case semantic.TypePrimitive:
		switch ref.QName {
		case "int", "int32", "Int":
			return "if err := tlScanTake(b, 4); err != nil { return err }\n", nil
		case "int53", "long", "int64", "Long", "double", "Double":
			return "if err := tlScanTake(b, 8); err != nil { return err }\n", nil
		case "int128":
			return "if err := tlScanTake(b, 16); err != nil { return err }\n", nil
		case "int256":
			return "if err := tlScanTake(b, 32); err != nil { return err }\n", nil
		case "string", "String", "bytes", "Bytes":
			return "if err := tlScanBytes(b); err != nil { return err }\n", nil
		case "bool", "Bool", "true", "false", "True":
			return "if _, err := b.Bool(); err != nil { return err }\n", nil
		case "Object":
			return fmt.Sprintf("if err := tlScanDynamic(%s, b, state); err != nil { return err }\n", profile), nil
		default:
			return "", fmt.Errorf("unsupported primitive %q", ref.QName)
		}
	case semantic.TypeGenericRef:
		return fmt.Sprintf("if err := tlScanDynamic(%s, b, state); err != nil { return err }\n", profile), nil
	case semantic.TypeVector:
		if ref.Arg == nil {
			return "", fmt.Errorf("vector has no element")
		}
		body, err := e.typeRef(ref.Arg, profile, bare, classes)
		if err != nil {
			return "", err
		}
		length := e.next("length")
		boxed := ref.QName == "Vector" && !ref.Bare && !ref.Percent
		return fmt.Sprintf("%s, err := tlScanVector(%s, b, state, %t)\nif err != nil { return err }\nfor %s := 0; %s < %s; %s++ {\n%s}\nstate.leave()\n", length, profile, boxed, e.next("i"), e.tempName(-1), length, e.tempName(-1), body), nil
	case semantic.TypeNamed:
		if ref.Arg != nil {
			return "", fmt.Errorf("named TypeRef has unexpected argument")
		}
		if ref.Bare || ref.Percent {
			bare[ref.QName] = struct{}{}
			return fmt.Sprintf("if err := tlScanBare%s(%s, b, state); err != nil { return err }\n", layerScanNameSuffix(ref.QName), profile), nil
		}
		classes[ref.QName] = struct{}{}
		return fmt.Sprintf("if err := tlScanClass%s(%s, b, state); err != nil { return err }\n", layerScanNameSuffix(ref.QName), profile), nil
	default:
		return "", fmt.Errorf("unsupported TypeRef kind %d", ref.Kind)
	}
}

func (e *layerScanEmitter) next(prefix string) string {
	name := fmt.Sprintf("%s%d", prefix, e.temp)
	e.temp++
	return name
}

func (e *layerScanEmitter) tempName(offset int) string {
	return fmt.Sprintf("i%d", e.temp+offset)
}

func groupLayerBodies(routes map[int]int) []layerScanProfileBody {
	bodyLayers := make(map[int][]int)
	var bodies []int
	for layer, body := range routes {
		if _, ok := bodyLayers[body]; !ok {
			bodies = append(bodies, body)
		}
		bodyLayers[body] = append(bodyLayers[body], layer)
	}
	sort.Ints(bodies)
	groups := make([]layerScanProfileBody, 0, len(bodies))
	for _, body := range bodies {
		sort.Ints(bodyLayers[body])
		groups = append(groups, layerScanProfileBody{Layers: bodyLayers[body], Body: body})
	}
	return groups
}

func sortedLayerScanNames(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func layerScanNameSuffix(qname string) string {
	digest := sha256.Sum256([]byte("gotd.tlprofile.scan-name.v1\x00" + qname))
	return fmt.Sprintf("%x", digest[:8])
}

func layerScanWireIDsKey(ids []uint32) string {
	var out strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&out, "%08x,", id)
	}
	return out.String()
}
