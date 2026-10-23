package gen

import (
	"fmt"
	"sort"
	"strings"
)

func (g *Generator) buildLayerSparseResults(model *layerCodecModel, canonicalNames map[string]struct{}) ([]string, error) {
	rpc, err := g.buildLayerRPCModel()
	if err != nil {
		return nil, err
	}
	wires, err := g.buildLayerWireModel()
	if err != nil {
		return nil, err
	}
	values, err := g.newLayerValueCompilerForWire(wires)
	if err != nil {
		return nil, err
	}
	emitter := &layerCodecEmitter{
		model: model, wire: wires, values: values,
		hookByName: make(map[string]string), genericSlots: make(map[string]int),
	}
	planBySource := make(map[string]int)
	methodRoutes := make(map[string]map[int]int)
	var resultSources []string
	for methodIndex := range rpc.Methods {
		method := &rpc.Methods[methodIndex]
		if method.Canonical == nil || len(method.Canonical.Definition.GenericParams) != 0 {
			continue
		}
		for profileIndex := range method.Profiles {
			profile := &method.Profiles[profileIndex]
			if profile.Definition == nil || profile.Wrapper != nil || profile.Request == layerRPCReject || profile.Request == layerRPCUnavailable {
				continue
			}
			if profile.Result.CanonicalRef == nil || profile.Result.WireRef == nil {
				return nil, fmt.Errorf("gen: sparse result %s profile %d has incomplete TypeRefs", method.Key, profile.Layer)
			}
			source, err := emitLayerSparseResultPlan(emitter, profile)
			if err != nil {
				return nil, fmt.Errorf("gen: sparse result %s profile %d: %w", method.Key, profile.Layer, err)
			}
			source = qualifyLayerSparseIdentifiers(source, canonicalNames)
			plan, ok := planBySource[source]
			if !ok {
				plan = len(model.SparseResultPlans)
				planBySource[source] = plan
				model.SparseResultPlans = append(model.SparseResultPlans, layerSparseResultPlan{ID: plan, Source: source})
				resultSources = append(resultSources, source)
			}
			routes := methodRoutes[method.Constant]
			if routes == nil {
				routes = make(map[int]int)
				methodRoutes[method.Constant] = routes
			}
			routes[profile.Layer] = plan
		}
	}
	constants := make([]string, 0, len(methodRoutes))
	for constant := range methodRoutes {
		constants = append(constants, constant)
	}
	sort.Strings(constants)
	for _, constant := range constants {
		entry := layerSparseResultMethod{Semantic: strings.TrimPrefix(constant, "Layer")}
		entry.Groups = groupLayerSparseResultPlans(methodRoutes[constant])
		model.SparseResultMethods = append(model.SparseResultMethods, entry)
	}
	return resultSources, nil
}

func emitLayerSparseResultPlan(emitter *layerCodecEmitter, profile *layerRPCMethodProfile) (string, error) {
	if emitter == nil || profile == nil || profile.Result.CanonicalRef == nil || profile.Result.WireRef == nil {
		return "", fmt.Errorf("incomplete result emitter")
	}
	canonical, err := emitter.values.Compile(emitter.wire.CanonicalLayer, profile.Result.CanonicalRef)
	if err != nil {
		return "", err
	}
	wire, err := emitter.values.Compile(profile.Layer, profile.Result.WireRef)
	if err != nil {
		return "", err
	}
	canonicalType, err := layerCodecGoType(canonical)
	if err != nil {
		return "", err
	}
	wireType, err := layerCodecGoType(wire)
	if err != nil {
		return "", err
	}
	var source strings.Builder
	canonicalExpression, canonicalHookExpression := emitLayerSparseResultAssertion(&source, "value", "canonical", canonicalType, layerSparseResultAcceptPointer(canonical))
	expression := canonicalExpression
	switch profile.Result.Action {
	case layerRPCDirect:
		if canonicalType != wireType {
			return "", fmt.Errorf("direct result Go types differ: %s and %s", canonicalType, wireType)
		}
	case layerRPCAdapter:
		hook := strings.TrimSpace(profile.Result.Adapter)
		if hook == "" {
			return "", fmt.Errorf("result adapter has no hook")
		}
		canonicalHookType := canonicalType
		if layerSparseResultAcceptPointer(canonical) {
			canonicalHookType = "*" + canonicalHookType
		}
		wireHookType := wireType
		if layerSparseResultAcceptPointer(wire) {
			wireHookType = "*" + wireHookType
		}
		if err := emitter.addHook(hook, fmt.Sprintf("func(LayerProfile, %s) (%s, error)", canonicalHookType, wireHookType)); err != nil {
			return "", err
		}
		fmt.Fprintf(&source, "adapted, err := %s(profile, %s)\nif err != nil { return fmt.Errorf(\"adapt RPC result: %%w\", err) }\n", hook, canonicalHookExpression)
		expression = "adapted"
		if layerSparseResultAcceptPointer(wire) {
			source.WriteString("if adapted == nil { return fmt.Errorf(\"adapted RPC result is nil\") }\n")
			expression = "*(adapted)"
		}
	default:
		return "", fmt.Errorf("unsupported result action %s", profile.Result.Action)
	}
	emitter.temp = 0
	body, err := emitter.emitEncodeValue(wire, expression, "b", "state", "RPC result")
	if err != nil {
		return "", err
	}
	source.WriteString(body)
	source.WriteString("return nil\n")
	return source.String(), nil
}

func emitLayerSparseResultAssertion(out *strings.Builder, value, local, goType string, acceptPointer bool) (expression, hookExpression string) {
	if acceptPointer {
		fmt.Fprintf(out, "var %s %s\nswitch candidate := %s.(type) {\ncase %s:\n\t%s = candidate\ncase *%s:\n\tif candidate == nil { return fmt.Errorf(\"nil RPC result %s\") }\n\t%s = *candidate\ndefault:\n\treturn fmt.Errorf(\"RPC result expected %s, got %%T\", %s)\n}\n", local, goType, value, goType, local, goType, goType, local, goType, value)
		return local, "&" + local
	}
	fmt.Fprintf(out, "%s, ok := %s.(%s)\nif !ok { return fmt.Errorf(\"RPC result expected %s, got %%T\", %s) }\n", local, value, goType, goType, value)
	return local, local
}

func layerSparseResultAcceptPointer(plan *layerValuePlan) bool {
	return plan != nil && (plan.Kind == layerValueExactBare || plan.Kind == layerValueBoxedConcrete)
}

func groupLayerSparseResultPlans(routes map[int]int) []layerSparseResultGroup {
	byPlan := make(map[int][]int)
	var plans []int
	for layer, plan := range routes {
		if _, ok := byPlan[plan]; !ok {
			plans = append(plans, plan)
		}
		byPlan[plan] = append(byPlan[plan], layer)
	}
	sort.Ints(plans)
	groups := make([]layerSparseResultGroup, 0, len(plans))
	for _, plan := range plans {
		sort.Ints(byPlan[plan])
		groups = append(groups, layerSparseResultGroup{Layers: byPlan[plan], Plan: plan})
	}
	return groups
}
