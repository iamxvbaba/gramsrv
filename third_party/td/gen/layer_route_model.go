package gen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iamxvbaba/td/gen/semantic"
)

// layerRouteModel is the compact static join between exact wire admission and
// canonical constructors. It contains no TypeRef or schema data at runtime.
type layerRouteModel struct {
	Wires     []layerRouteWire
	Factories []layerRouteFactory
}

type layerRouteWire struct {
	WireID uint32
	Hex    string
	Groups []layerRouteGroup
}

type layerRouteGroup struct {
	Layers       []int
	Semantic     string
	Mode         string
	CanonicalID  uint32
	CanonicalHex string
}

type layerRouteFactory struct {
	WireID uint32
	Hex    string
	GoType string
}

type layerRouteIdentity struct {
	Semantic    semantic.SemanticKey
	Mode        layerExecutionMode
	CanonicalID uint32
}

func (g *Generator) buildLayerRouteModel() (*layerRouteModel, error) {
	execution, err := g.buildLayerExecutionModel()
	if err != nil {
		return nil, fmt.Errorf("gen: sparse route execution model: %w", err)
	}
	bindings, err := g.buildLayerBindings()
	if err != nil {
		return nil, fmt.Errorf("gen: sparse route canonical bindings: %w", err)
	}

	byWire := make(map[uint32]map[layerRouteIdentity][]int)
	for _, route := range execution.Routes {
		identity := layerRouteIdentity{Semantic: route.Key, Mode: route.Mode}
		if binding := bindings.definition(route.Key); binding != nil && binding.Definition != nil {
			identity.CanonicalID = binding.Definition.WireID
		}
		groups := byWire[route.WireID]
		if groups == nil {
			groups = make(map[layerRouteIdentity][]int)
			byWire[route.WireID] = groups
		}
		groups[identity] = append(groups[identity], route.Layer)
	}

	model := &layerRouteModel{}
	wireIDs := make([]uint32, 0, len(byWire))
	for id := range byWire {
		wireIDs = append(wireIDs, id)
	}
	sort.Slice(wireIDs, func(i, j int) bool { return wireIDs[i] < wireIDs[j] })
	for _, id := range wireIDs {
		wire := layerRouteWire{WireID: id, Hex: fmt.Sprintf("%08x", id)}
		identities := make([]layerRouteIdentity, 0, len(byWire[id]))
		for identity := range byWire[id] {
			identities = append(identities, identity)
		}
		sort.Slice(identities, func(i, j int) bool {
			left, right := identities[i], identities[j]
			if left.Semantic.String() != right.Semantic.String() {
				return left.Semantic.String() < right.Semantic.String()
			}
			if left.Mode != right.Mode {
				return left.Mode < right.Mode
			}
			return left.CanonicalID < right.CanonicalID
		})
		for _, identity := range identities {
			layers := byWire[id][identity]
			sort.Ints(layers)
			wire.Groups = append(wire.Groups, layerRouteGroup{
				Layers: layers, Semantic: strings.TrimPrefix(layerSemanticConstant(identity.Semantic), "Layer"),
				Mode: layerRouteModeConstant(identity.Mode), CanonicalID: identity.CanonicalID,
				CanonicalHex: fmt.Sprintf("%08x", identity.CanonicalID),
			})
		}
		model.Wires = append(model.Wires, wire)
	}

	canonical := g.schemaSet.Schemas[g.schemaSet.CanonicalLayer]
	if canonical == nil {
		return nil, fmt.Errorf("gen: sparse route canonical schema %d is absent", g.schemaSet.CanonicalLayer)
	}
	definitions := append([]*semantic.Definition(nil), canonical.Definitions...)
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].WireID < definitions[j].WireID })
	for _, definition := range definitions {
		binding := bindings.definition(definition.Key)
		if binding == nil || binding.Structure == nil {
			return nil, fmt.Errorf("gen: sparse route factory %s has no canonical Go binding", definition.Key)
		}
		model.Factories = append(model.Factories, layerRouteFactory{
			WireID: definition.WireID, Hex: fmt.Sprintf("%08x", definition.WireID), GoType: binding.Structure.Name,
		})
	}
	return model, nil
}

func layerRouteModeConstant(mode layerExecutionMode) string {
	switch mode {
	case layerExecutionDirect:
		return "tlRouteDirect"
	case layerExecutionRetag:
		return "tlRouteRetag"
	case layerExecutionRewrite:
		return "tlRouteRewrite"
	case layerExecutionUnavailable:
		return "tlRouteUnavailable"
	case layerExecutionPolicy:
		return "tlRoutePolicy"
	case layerExecutionProfileOnly:
		return "tlRouteProfileOnly"
	default:
		return "tlRouteInvalid"
	}
}
