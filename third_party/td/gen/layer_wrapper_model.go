package gen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iamxvbaba/td/gen/semantic"
)

// layerWrapperModel is the small generated prefix parser for transparent
// generic RPC envelopes. It materializes only immutable wrapper metadata and
// leaves the generic query untouched for recursive exact admission.
type layerWrapperModel struct {
	Bodies                 []layerWrapperBody
	Profiles               []layerWrapperProfile
	UnprofiledSelectorWire uint32
	UnprofiledSelectorHex  string
	UnprofiledInvariants   []layerWrapperInvariant
	KnownRPCWireIDs        []uint32
}

type layerWrapperInvariant struct {
	WireID uint32
	Hex    string
}

type layerWrapperBody struct {
	ID     int
	Source string
}

type layerWrapperRoute struct {
	WireID uint32
	Hex    string
	Body   int
}

type layerWrapperProfile struct {
	Layer  int
	Routes []layerWrapperRoute
}

func (g *Generator) buildLayerWrapperModel() (*layerWrapperModel, error) {
	rpc, err := g.buildLayerRPCModel()
	if err != nil {
		return nil, err
	}
	model := &layerWrapperModel{}
	bodyBySource := make(map[string]int)
	routesByLayer := make(map[int][]layerWrapperRoute)
	for methodIndex := range rpc.Methods {
		method := &rpc.Methods[methodIndex]
		canonical := method.profile(rpc.CanonicalLayer)
		if canonical == nil || canonical.Wrapper == nil || canonical.Definition == nil {
			continue
		}
		for profileIndex := range method.Profiles {
			profile := &method.Profiles[profileIndex]
			if profile.Wrapper == nil || profile.Definition == nil || profile.Request == layerRPCReject || profile.Request == layerRPCUnavailable {
				continue
			}
			// The compact parser deliberately has no wrapper conversion VM. Any
			// semantic metadata drift must become an explicit generated adapter.
			if profile.Definition.BodyShape != canonical.Definition.BodyShape {
				return nil, fmt.Errorf("E_SPARSE_WRAPPER_SHAPE_CHANGED: %s profile %d differs from canonical wrapper metadata", method.Key, profile.Layer)
			}
			source, err := emitLayerWrapperBody(method, profile)
			if err != nil {
				return nil, fmt.Errorf("gen: sparse wrapper %s profile %d: %w", method.Key, profile.Layer, err)
			}
			body, ok := bodyBySource[source]
			if !ok {
				body = len(model.Bodies)
				bodyBySource[source] = body
				model.Bodies = append(model.Bodies, layerWrapperBody{ID: body, Source: source})
			}
			routesByLayer[profile.Layer] = append(routesByLayer[profile.Layer], layerWrapperRoute{
				WireID: profile.WireID, Hex: fmt.Sprintf("%08x", profile.WireID), Body: body,
			})
		}
	}
	for _, layer := range rpc.Profiles {
		routes := routesByLayer[layer]
		sort.Slice(routes, func(i, j int) bool { return routes[i].WireID < routes[j].WireID })
		model.Profiles = append(model.Profiles, layerWrapperProfile{Layer: layer, Routes: routes})
	}
	if err := g.buildLayerUnprofiledModel(model, rpc); err != nil {
		return nil, err
	}
	return model, nil
}

func (g *Generator) buildLayerUnprofiledModel(model *layerWrapperModel, rpc *layerRPCModel) error {
	selectorKey := semantic.SemanticKey{Category: semantic.CategoryFunction, QName: "invokeWithLayer"}
	selector := rpc.method(selectorKey)
	if selector == nil {
		return fmt.Errorf("gen: sparse unprofiled invokeWithLayer is absent")
	}
	for index, layer := range rpc.Profiles {
		profile := selector.profile(layer)
		if profile == nil || profile.Wrapper == nil || profile.Definition == nil {
			return fmt.Errorf("gen: sparse unprofiled invokeWithLayer is absent in profile %d", layer)
		}
		if index == 0 {
			model.UnprofiledSelectorWire = profile.WireID
			model.UnprofiledSelectorHex = fmt.Sprintf("%08x", profile.WireID)
		} else if profile.WireID != model.UnprofiledSelectorWire || profile.Definition.BodyShape != selector.Profiles[0].Definition.BodyShape {
			return fmt.Errorf("gen: sparse unprofiled invokeWithLayer drifted in profile %d", layer)
		}
	}
	known := make(map[uint32]struct{})
	for _, route := range rpc.Routes {
		known[route.WireID] = struct{}{}
	}
	for wireID := range known {
		model.KnownRPCWireIDs = append(model.KnownRPCWireIDs, wireID)
	}
	sort.Slice(model.KnownRPCWireIDs, func(i, j int) bool { return model.KnownRPCWireIDs[i] < model.KnownRPCWireIDs[j] })

	execution, err := g.buildLayerExecutionModel()
	if err != nil {
		return err
	}
	routesBySemantic := make(map[string][]layerExecutionRoute)
	for _, route := range execution.Routes {
		if route.Key.Category == semantic.CategoryFunction {
			routesBySemantic[route.Key.String()] = append(routesBySemantic[route.Key.String()], route)
		}
	}
	for methodIndex := range rpc.Methods {
		method := &rpc.Methods[methodIndex]
		if !method.Handler || method.Canonical == nil {
			continue
		}
		routes := routesBySemantic[method.Key.String()]
		if len(routes) != len(rpc.Profiles) {
			continue
		}
		sort.Slice(routes, func(i, j int) bool { return routes[i].Layer < routes[j].Layer })
		first := routes[0]
		invariant := first.Mode == layerExecutionDirect && first.ResultPlan >= 0
		for index, layer := range rpc.Profiles {
			route := routes[index]
			if route.Layer != layer || route.Mode != layerExecutionDirect || route.WireID != first.WireID ||
				route.PreflightPlan != first.PreflightPlan || route.ResultPlan != first.ResultPlan {
				invariant = false
				break
			}
		}
		if invariant {
			model.UnprofiledInvariants = append(model.UnprofiledInvariants, layerWrapperInvariant{WireID: first.WireID, Hex: fmt.Sprintf("%08x", first.WireID)})
		}
	}
	sort.Slice(model.UnprofiledInvariants, func(i, j int) bool {
		return model.UnprofiledInvariants[i].WireID < model.UnprofiledInvariants[j].WireID
	})
	return nil
}

func emitLayerWrapperBody(method *layerRPCMethodPlan, profile *layerRPCMethodProfile) (string, error) {
	if method == nil || profile == nil || profile.Wrapper == nil || profile.Definition == nil {
		return "", fmt.Errorf("nil wrapper route")
	}
	wrapper := profile.Wrapper
	definition := profile.Definition
	queryIndex := -1
	for index := range definition.Fields {
		if definition.Fields[index].Ordinal == wrapper.QueryFieldOrdinal {
			queryIndex = index
			break
		}
	}
	if queryIndex < 0 || queryIndex != len(definition.Fields)-1 {
		return "", fmt.Errorf("generic query must be the final wrapper field")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "if err := b.ConsumeID(0x%08x); err != nil { return sparseWrapper{}, 0, false, err }\n", profile.WireID)
	fmt.Fprintf(&out, "frame := sparseWrapper{profile: profile, semantic: %s, wireID: 0x%08x}\n", strings.TrimPrefix(method.Constant, "Layer"), profile.WireID)
	out.WriteString("nestedProfile := profile\nexplicit := false\n")
	flags := make(map[string]string)
	emitter := &layerWrapperValueEmitter{}
	for fieldIndex := range definition.Fields {
		field := &definition.Fields[fieldIndex]
		if field.Ordinal == wrapper.QueryFieldOrdinal {
			out.WriteString("return frame, nestedProfile, explicit, nil\n")
			return out.String(), nil
		}
		if field.Kind == semantic.FieldFlagsWord {
			name := emitter.next("flags")
			fmt.Fprintf(&out, "%sRaw, err := b.Uint32()\nif err != nil { return sparseWrapper{}, 0, false, err }\n%s := bin.Fields(%sRaw)\n", name, name, name)
			fmt.Fprintf(&out, "frame.fields = append(frame.fields, sparseWrapperField{name: %q, present: true, value: %s})\n", field.Name, name)
			flags[field.Name] = name
			continue
		}
		condition := "true"
		if field.Condition != nil {
			word := flags[field.Condition.Word]
			if word == "" {
				return "", fmt.Errorf("field %q references unavailable flags word %q", field.Name, field.Condition.Word)
			}
			condition = fmt.Sprintf("%s.Has(%d)", word, field.Condition.Bit)
		}
		if field.Condition != nil && field.Condition.PresenceOnly {
			fmt.Fprintf(&out, "frame.fields = append(frame.fields, sparseWrapperField{name: %q, present: %s, value: %s})\n", field.Name, condition, condition)
			continue
		}
		value := emitter.next("value")
		decode, goType, err := emitter.decode(&field.Type, value)
		if err != nil {
			return "", fmt.Errorf("field %q: %w", field.Name, err)
		}
		if field.Condition != nil {
			fmt.Fprintf(&out, "if %s {\n%s", condition, decode)
			fmt.Fprintf(&out, "frame.fields = append(frame.fields, sparseWrapperField{name: %q, present: true, value: %s})\n", field.Name, value)
			out.WriteString("} else {\n")
			fmt.Fprintf(&out, "frame.fields = append(frame.fields, sparseWrapperField{name: %q})\n}\n", field.Name)
		} else {
			out.WriteString(decode)
			fmt.Fprintf(&out, "frame.fields = append(frame.fields, sparseWrapperField{name: %q, present: true, value: %s})\n", field.Name, value)
		}
		if field.Ordinal == wrapper.ProfileField {
			if goType != "int" || field.Condition != nil {
				return "", fmt.Errorf("profile selector field %q is not an unconditional int", field.Name)
			}
			fmt.Fprintf(&out, "selected, ok := ResolveProfile(%s)\nif !ok { return sparseWrapper{}, 0, false, fmt.Errorf(\"tlprofile: invokeWithLayer selected unsupported exact profile %%d\", %s) }\nnestedProfile, explicit = selected, true\n", value, value)
		}
	}
	return "", fmt.Errorf("wrapper query return was not emitted")
}

type layerWrapperValueEmitter struct{ temp int }

func (e *layerWrapperValueEmitter) next(prefix string) string {
	name := fmt.Sprintf("%s%d", prefix, e.temp)
	e.temp++
	return name
}

func (e *layerWrapperValueEmitter) decode(ref *semantic.TypeRef, target string) (string, string, error) {
	if ref == nil {
		return "", "", fmt.Errorf("nil TypeRef")
	}
	var method, goType string
	switch ref.Kind {
	case semantic.TypePrimitive:
		switch ref.QName {
		case "int", "Int", "int32":
			method, goType = "Int", "int"
		case "int53", "int64", "long", "Long":
			method, goType = "Long", "int64"
		case "string", "String":
			method, goType = "String", "string"
		case "bytes", "Bytes":
			method, goType = "Bytes", "[]byte"
		case "bool", "Bool":
			method, goType = "Bool", "bool"
		case "Object":
			return fmt.Sprintf("%s, err := tlDecodeObjectPrefixValidated(profile, b, limits, scanState, tlScanDynamic)\nif err != nil { return sparseWrapper{}, 0, false, err }\n", target), "bin.Object", nil
		default:
			return "", "", fmt.Errorf("unsupported wrapper primitive %q", ref.QName)
		}
		return fmt.Sprintf("%s, err := b.%s()\nif err != nil { return sparseWrapper{}, 0, false, err }\n", target, method), goType, nil
	case semantic.TypeNamed:
		if ref.Bare || ref.Percent || ref.Arg != nil {
			return "", "", fmt.Errorf("unsupported bare or parameterized wrapper metadata TypeRef %s", ref.String())
		}
		scanner := "tlScanClass" + layerScanNameSuffix(ref.QName)
		return fmt.Sprintf("%s, err := tlDecodeObjectPrefixValidated(profile, b, limits, scanState, %s)\nif err != nil { return sparseWrapper{}, 0, false, err }\n", target, scanner), "bin.Object", nil
	case semantic.TypeVector:
		if ref.Arg == nil {
			return "", "", fmt.Errorf("wrapper vector has no element")
		}
		elementType, err := layerWrapperGoType(ref.Arg)
		if err != nil {
			return "", "", err
		}
		minElementBytes, err := layerWrapperMinWireSize(ref.Arg)
		if err != nil {
			return "", "", err
		}
		length := e.next("length")
		index := e.next("i")
		element := e.next("element")
		var out strings.Builder
		if ref.QName == "Vector" && !ref.Bare && !ref.Percent {
			out.WriteString("if err := b.ConsumeID(bin.TypeVector); err != nil { return sparseWrapper{}, 0, false, err }\n")
		}
		fmt.Fprintf(&out, "%s, err := tlWrapperVectorLength(profile, b, scanState, %d)\nif err != nil { return sparseWrapper{}, 0, false, err }\n", length, minElementBytes)
		fmt.Fprintf(&out, "%s := make([]%s, %s)\nfor %s := 0; %s < %s; %s++ {\n", target, elementType, length, index, index, length, index)
		decode, _, err := e.decode(ref.Arg, element)
		if err != nil {
			return "", "", err
		}
		out.WriteString(decode)
		fmt.Fprintf(&out, "%s[%s] = %s\n}\nscanState.leave()\n", target, index, element)
		return out.String(), "[]" + elementType, nil
	default:
		return "", "", fmt.Errorf("unsupported wrapper metadata TypeRef %s", ref.String())
	}
}

func layerWrapperGoType(ref *semantic.TypeRef) (string, error) {
	if ref == nil {
		return "", fmt.Errorf("nil TypeRef")
	}
	switch ref.Kind {
	case semantic.TypePrimitive:
		switch ref.QName {
		case "int", "Int", "int32":
			return "int", nil
		case "int53", "int64", "long", "Long":
			return "int64", nil
		case "string", "String":
			return "string", nil
		case "bytes", "Bytes":
			return "[]byte", nil
		case "bool", "Bool":
			return "bool", nil
		case "Object":
			return "bin.Object", nil
		}
	case semantic.TypeNamed:
		if !ref.Bare && !ref.Percent && ref.Arg == nil {
			return "bin.Object", nil
		}
	case semantic.TypeVector:
		element, err := layerWrapperGoType(ref.Arg)
		if err == nil {
			return "[]" + element, nil
		}
	}
	return "", fmt.Errorf("unsupported wrapper Go type %s", ref.String())
}

func layerWrapperMinWireSize(ref *semantic.TypeRef) (int, error) {
	if ref == nil {
		return 0, fmt.Errorf("nil wrapper TypeRef")
	}
	switch ref.Kind {
	case semantic.TypePrimitive:
		switch ref.QName {
		case "int", "Int", "int32", "string", "String", "bytes", "Bytes", "bool", "Bool", "Object":
			return 4, nil
		case "int53", "int64", "long", "Long":
			return 8, nil
		}
	case semantic.TypeNamed:
		if !ref.Bare && !ref.Percent && ref.Arg == nil {
			return 4, nil
		}
	case semantic.TypeVector:
		if ref.Arg != nil {
			if ref.QName == "Vector" && !ref.Bare && !ref.Percent {
				return 8, nil
			}
			return 4, nil
		}
	}
	return 0, fmt.Errorf("unsupported wrapper minimum wire size %s", ref.String())
}
