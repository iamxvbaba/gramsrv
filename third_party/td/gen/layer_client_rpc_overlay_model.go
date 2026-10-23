package gen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iamxvbaba/td/gen/semantic"
)

type layerClientRPCOverlaySourceModel struct {
	Package  string
	Overlays []layerClientRPCOverlaySource
	Helpers  []string
}

type layerClientRPCOverlaySource struct {
	Name        string
	Constant    string
	Methods     []layerClientRPCOverlayMethodSource
	MethodCount int
}

type layerClientRPCOverlayMethodSource struct {
	WireID      uint32
	Semantic    string
	AdaptName   string
	Declaration string
}

type layerClientRPCOverlayFieldMapping struct {
	Source        *semantic.FieldShape
	Target        *layerFieldBinding
	SourceLocal   string
	SourcePresent string
	Converter     string
}

func (g *Generator) buildLayerClientRPCOverlaySourceModel(pkg string) (*layerClientRPCOverlaySourceModel, error) {
	model := &layerClientRPCOverlaySourceModel{Package: pkg}
	if g == nil || g.schemaSet == nil {
		return nil, fmt.Errorf("gen: client RPC overlays require a schema-set generator")
	}
	wire, err := g.buildLayerWireModel()
	if err != nil {
		return nil, fmt.Errorf("gen: client RPC overlays build wire model: %w", err)
	}
	values, err := g.newLayerValueCompilerForWire(wire)
	if err != nil {
		return nil, fmt.Errorf("gen: client RPC overlays build value compiler: %w", err)
	}
	emitter := &layerCodecEmitter{
		values:       values,
		genericSlots: make(map[string]int),
	}
	usedConverters := make(map[string]struct{})
	for _, source := range g.schemaSet.ClientRPCOverlays {
		if source == nil {
			return nil, fmt.Errorf("gen: nil client RPC overlay")
		}
		overlay := layerClientRPCOverlaySource{
			Name:     source.Name,
			Constant: "LayerClientRPCOverlay" + pascal(source.Name),
		}
		seenWire := make(map[uint32]string, len(source.Methods))
		for _, method := range source.Methods {
			if method == nil || method.Definition == nil {
				return nil, fmt.Errorf("gen: client RPC overlay %q contains a nil method", source.Name)
			}
			if previous, duplicate := seenWire[method.Definition.WireID]; duplicate {
				return nil, fmt.Errorf("gen: client RPC overlay wire %#08x is shared by %s and %s", method.Definition.WireID, previous, method.Definition.Key)
			}
			seenWire[method.Definition.WireID] = method.Definition.Key.String()
			target := wire.Bindings.definition(method.Target)
			if target == nil || target.Key.Category != semantic.CategoryFunction {
				return nil, fmt.Errorf("gen: client RPC overlay %q method %s has no canonical function binding for %s", source.Name, method.Definition.Key, method.Target)
			}
			declaration, converters, err := buildLayerClientRPCOverlayMethod(emitter, g.schemaSet.CanonicalLayer, source.Name, method, target)
			if err != nil {
				return nil, err
			}
			for _, converter := range converters {
				usedConverters[converter] = struct{}{}
			}
			hex := fmt.Sprintf("%08x", method.Definition.WireID)
			overlay.Methods = append(overlay.Methods, layerClientRPCOverlayMethodSource{
				WireID:      method.Definition.WireID,
				Semantic:    method.Target.QName,
				AdaptName:   "layerAdaptClientRPCOverlay" + pascal(source.Name) + "_" + hex,
				Declaration: declaration,
			})
		}
		sort.Slice(overlay.Methods, func(i, j int) bool { return overlay.Methods[i].WireID < overlay.Methods[j].WireID })
		overlay.MethodCount = len(overlay.Methods)
		model.Overlays = append(model.Overlays, overlay)
	}
	sort.Slice(model.Overlays, func(i, j int) bool { return model.Overlays[i].Name < model.Overlays[j].Name })
	for converter := range usedConverters {
		helper, err := layerClientRPCOverlayConverterHelper(converter)
		if err != nil {
			return nil, err
		}
		model.Helpers = append(model.Helpers, helper)
	}
	sort.Strings(model.Helpers)
	return model, nil
}

func buildLayerClientRPCOverlayMethod(emitter *layerCodecEmitter, layer int, overlayName string, method *semantic.ClientRPCMethod, target *layerDefinitionBinding) (string, []string, error) {
	if len(method.Definition.GenericParams) != 0 {
		return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s has unsupported generic parameters", overlayName, method.Definition.Key)
	}
	adaptName := "layerAdaptClientRPCOverlay" + pascal(overlayName) + "_" + fmt.Sprintf("%08x", method.Definition.WireID)
	sourceByName := make(map[string]*semantic.FieldShape, len(method.Definition.Fields))
	for index := range method.Definition.Fields {
		field := &method.Definition.Fields[index]
		sourceByName[field.Name] = field
	}
	droppedSource := make(map[string]struct{}, len(method.Drops))
	for _, sourceName := range method.Drops {
		field := sourceByName[sourceName]
		if field == nil || field.Kind != semantic.FieldValue {
			return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s drops missing or non-value source field %q", overlayName, method.Definition.Key, sourceName)
		}
		droppedSource[sourceName] = struct{}{}
	}
	for targetName, sourceName := range method.Renames {
		if target.FieldByName[targetName] == nil {
			return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s rename targets missing canonical field %q", overlayName, method.Definition.Key, targetName)
		}
		if sourceByName[sourceName] == nil {
			return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s rename reads missing source field %q", overlayName, method.Definition.Key, sourceName)
		}
	}

	mappings := make(map[string]layerClientRPCOverlayFieldMapping)
	usedSource := make(map[string]string)
	usedConverters := make([]string, 0, len(method.Converters))
	for index := range target.Fields {
		targetField := &target.Fields[index]
		if targetField.Semantic.Kind == semantic.FieldFlagsWord {
			continue
		}
		sourceName := targetField.Semantic.Name
		if renamed, ok := method.Renames[targetField.Semantic.Name]; ok {
			sourceName = renamed
		}
		sourceField := sourceByName[sourceName]
		converter := method.Converters[targetField.Semantic.Name]
		if sourceField == nil {
			if converter != "" {
				return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s converter for %q has no source field", overlayName, method.Definition.Key, targetField.Semantic.Name)
			}
			if targetField.Semantic.Condition == nil && !layerClientRPCOverlayCanDefault(targetField.Semantic.Type) {
				return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s cannot default new required canonical field %q of type %s", overlayName, method.Definition.Key, targetField.Semantic.Name, targetField.Semantic.Type.String())
			}
			continue
		}
		if sourceField.Kind != semantic.FieldValue {
			return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s maps canonical value %q from flags field %q", overlayName, method.Definition.Key, targetField.Semantic.Name, sourceName)
		}
		if previous, duplicate := usedSource[sourceName]; duplicate {
			return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s source field %q maps to both %q and %q", overlayName, method.Definition.Key, sourceName, previous, targetField.Semantic.Name)
		}
		usedSource[sourceName] = targetField.Semantic.Name
		if converter == "" && !sourceField.Type.Equal(targetField.Semantic.Type) {
			return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s field %q changes %s to %s without an explicit converter", overlayName, method.Definition.Key, targetField.Semantic.Name, sourceField.Type.String(), targetField.Semantic.Type.String())
		}
		if converter != "" {
			if err := validateLayerClientRPCOverlayConverter(converter, sourceField.Type, targetField.Semantic.Type); err != nil {
				return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s field %q: %w", overlayName, method.Definition.Key, targetField.Semantic.Name, err)
			}
			usedConverters = append(usedConverters, converter)
		}
		if sourceField.Condition != nil && targetField.Semantic.Condition == nil {
			return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s optional source field %q cannot populate required canonical field %q", overlayName, method.Definition.Key, sourceName, targetField.Semantic.Name)
		}
		mappings[sourceName] = layerClientRPCOverlayFieldMapping{
			Source:    sourceField,
			Target:    targetField,
			Converter: converter,
		}
	}
	for _, sourceField := range method.Definition.Fields {
		if sourceField.Kind == semantic.FieldFlagsWord {
			continue
		}
		if _, dropped := droppedSource[sourceField.Name]; dropped {
			continue
		}
		if _, ok := usedSource[sourceField.Name]; !ok {
			return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s source field %q is not mapped to canonical", overlayName, method.Definition.Key, sourceField.Name)
		}
	}
	for targetField := range method.Converters {
		if target.FieldByName[targetField] == nil {
			return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s converter targets missing canonical field %q", overlayName, method.Definition.Key, targetField)
		}
	}

	emitter.temp = 0
	var body strings.Builder
	fmt.Fprintf(&body, "func %s(profile LayerProfile, b *bin.Buffer, state *layerCodecState) (*bin.Buffer, error) {\n", adaptName)
	fmt.Fprintf(&body, "\tif b == nil || state == nil { return nil, &LayerCodecError{Operation: \"adapt client RPC overlay\", Profile: profile, WireID: 0x%08x, Reason: \"nil buffer or codec state\"} }\n", method.Definition.WireID)
	fmt.Fprintf(&body, "\tif err := b.ConsumeID(0x%08x); err != nil { return nil, fmt.Errorf(\"consume private method %s: %%w\", err) }\n", method.Definition.WireID, method.Definition.Key.QName)
	fmt.Fprintf(&body, "\tvalue := &%s{}\n", target.Structure.Name)
	for index := range method.Definition.Fields {
		field := &method.Definition.Fields[index]
		if field.Kind == semantic.FieldFlagsWord {
			mask := layerClientRPCOverlayFlagMask(method.Definition, field.Name)
			fmt.Fprintf(&body, "\tvar wireFlags%d bin.Fields\n\tif err := wireFlags%d.Decode(b); err != nil { return nil, fmt.Errorf(\"decode private flags %s: %%w\", err) }\n", field.Ordinal, field.Ordinal, field.Name)
			fmt.Fprintf(&body, "\tif unknown := uint32(wireFlags%d) &^ 0x%08x; unknown != 0 { return nil, &LayerCodecError{Operation: \"adapt client RPC overlay\", Profile: profile, WireID: 0x%08x, Reason: fmt.Sprintf(\"unsupported private flag bits %%#x\", unknown)} }\n", field.Ordinal, mask, method.Definition.WireID)
			continue
		}
		mapping := mappings[field.Name]
		plan, err := emitter.values.Compile(layer, &field.Type)
		if err != nil {
			return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s field %q: %w", overlayName, method.Definition.Key, field.Name, err)
		}
		goType, err := layerCodecGoType(plan)
		if err != nil {
			return "", nil, err
		}
		local := fmt.Sprintf("sourceField%d", field.Ordinal)
		present := "true"
		if field.Condition != nil {
			flagsOrdinal, ok := layerCodecFlagOrdinal(method.Definition, field.Condition.Word)
			if !ok {
				return "", nil, fmt.Errorf("gen: client RPC overlay %q method %s field %q references missing flags", overlayName, method.Definition.Key, field.Name)
			}
			present = fmt.Sprintf("wireFlags%d.Has(%d)", flagsOrdinal, field.Condition.Bit)
		}
		mapping.SourceLocal = local
		mapping.SourcePresent = present
		mappings[field.Name] = mapping
		if field.Condition != nil && field.Condition.PresenceOnly {
			fmt.Fprintf(&body, "\t%s := %s\n", local, present)
			if _, dropped := droppedSource[field.Name]; dropped {
				fmt.Fprintf(&body, "\t_ = %s // explicitly consumed audited private field %s\n", local, field.Name)
			}
			continue
		}
		fmt.Fprintf(&body, "\tvar %s %s\n", local, goType)
		decode, err := emitter.emitDecodeValue(plan, local, "b", "state", "client overlay field "+field.Name)
		if err != nil {
			return "", nil, err
		}
		if field.Condition != nil {
			fmt.Fprintf(&body, "\tif %s {\n%s\t}\n", present, indentLayerCodec(decode, "\t\t"))
		} else {
			body.WriteString(indentLayerCodec(decode, "\t"))
		}
		if _, dropped := droppedSource[field.Name]; dropped {
			fmt.Fprintf(&body, "\t_ = %s // explicitly consumed audited private field %s\n", local, field.Name)
		}
	}
	for index := range target.Fields {
		targetField := &target.Fields[index]
		if targetField.Semantic.Kind == semantic.FieldFlagsWord {
			continue
		}
		sourceName := targetField.Semantic.Name
		if renamed, ok := method.Renames[targetField.Semantic.Name]; ok {
			sourceName = renamed
		}
		mapping, ok := mappings[sourceName]
		if !ok {
			continue
		}
		var assignment string
		if mapping.Converter == "" {
			assignment = fmt.Sprintf("value.%s = %s\n", targetField.Go.Name, mapping.SourceLocal)
		} else {
			assignment = layerClientRPCOverlayConverterAssignment(mapping.Converter, "value."+targetField.Go.Name, mapping.SourceLocal)
		}
		if targetField.Semantic.Condition != nil {
			flagsField := target.FieldByName[targetField.Semantic.Condition.Word]
			if flagsField == nil || flagsField.Semantic.Kind != semantic.FieldFlagsWord {
				return "", nil, fmt.Errorf("gen: client RPC overlay %q target %s field %q has missing flags binding", overlayName, target.Key, targetField.Semantic.Name)
			}
			assignment += fmt.Sprintf("value.%s.Set(%d)\n", flagsField.Go.Name, targetField.Semantic.Condition.Bit)
		}
		if mapping.Source.Condition != nil {
			fmt.Fprintf(&body, "\tif %s {\n%s\t}\n", mapping.SourcePresent, indentLayerCodec(assignment, "\t\t"))
		} else {
			body.WriteString(indentLayerCodec(assignment, "\t"))
		}
	}
	fmt.Fprintf(&body, "\tif b.Len() != 0 { return nil, &LayerCodecError{Operation: \"adapt client RPC overlay\", Profile: profile, Semantic: %s, WireID: 0x%08x, Reason: fmt.Sprintf(\"%%d trailing private method bytes\", b.Len())} }\n", layerSemanticConstant(target.Key), method.Definition.WireID)
	body.WriteString("\tvar canonical bin.Buffer\n")
	fmt.Fprintf(&body, "\tif err := value.Encode(&canonical); err != nil { return nil, &LayerCodecError{Operation: \"encode canonical client RPC overlay\", Profile: profile, Semantic: %s, WireID: 0x%08x, Reason: err.Error(), Cause: err} }\n", layerSemanticConstant(target.Key), method.Definition.WireID)
	body.WriteString("\treturn &canonical, nil\n}\n")
	return body.String(), usedConverters, nil
}

func layerClientRPCOverlayFlagMask(definition *semantic.Definition, word string) uint32 {
	var mask uint32
	for _, field := range definition.Fields {
		if field.Condition != nil && field.Condition.Word == word {
			mask |= 1 << field.Condition.Bit
		}
	}
	return mask
}

func layerClientRPCOverlayCanDefault(ref semantic.TypeRef) bool {
	switch ref.Kind {
	case semantic.TypePrimitive, semantic.TypeVector:
		return true
	default:
		return false
	}
}

func validateLayerClientRPCOverlayConverter(name string, source, target semantic.TypeRef) error {
	wantSource, wantTarget := "", ""
	switch name {
	case "vector_int_to_input_message_id":
		wantSource, wantTarget = "Vector<int>", "Vector<InputMessage>"
	case "long_to_input_user":
		wantSource, wantTarget = "long", "InputUser"
	case "input_channel_to_input_peer":
		wantSource, wantTarget = "InputChannel", "InputPeer"
	case "input_theme_settings_to_vector":
		wantSource, wantTarget = "InputThemeSettings", "Vector<InputThemeSettings>"
	default:
		return fmt.Errorf("unknown converter %q", name)
	}
	if source.String() != wantSource || target.String() != wantTarget {
		return fmt.Errorf("converter %q requires %s -> %s, got %s -> %s", name, wantSource, wantTarget, source.String(), target.String())
	}
	return nil
}

func layerClientRPCOverlayConverterAssignment(name, target, source string) string {
	switch name {
	case "vector_int_to_input_message_id":
		return fmt.Sprintf("%s = layerClientRPCVectorIntToInputMessageID(%s)\n", target, source)
	case "long_to_input_user":
		return fmt.Sprintf("%s = &InputUser{UserID: %s}\n", target, source)
	case "input_channel_to_input_peer":
		return fmt.Sprintf("converted, err := layerClientRPCInputChannelToInputPeer(%s)\nif err != nil { return nil, err }\n%s = converted\n", source, target)
	case "input_theme_settings_to_vector":
		return fmt.Sprintf("%s = []InputThemeSettings{%s}\n", target, source)
	default:
		panic("unvalidated client RPC overlay converter " + name)
	}
}

func layerClientRPCOverlayConverterHelper(name string) (string, error) {
	switch name {
	case "vector_int_to_input_message_id":
		return `func layerClientRPCVectorIntToInputMessageID(source []int) []InputMessageClass {
	if source == nil { return nil }
	result := make([]InputMessageClass, len(source))
	for index, id := range source { result[index] = &InputMessageID{ID: id} }
	return result
}`, nil
	case "long_to_input_user":
		return "", nil
	case "input_theme_settings_to_vector":
		return "", nil
	case "input_channel_to_input_peer":
		return `func layerClientRPCInputChannelToInputPeer(source InputChannelClass) (InputPeerClass, error) {
	switch value := source.(type) {
	case *InputChannelEmpty:
		return &InputPeerEmpty{}, nil
	case *InputChannel:
		return &InputPeerChannel{ChannelID: value.ChannelID, AccessHash: value.AccessHash}, nil
	case *InputChannelFromMessage:
		return &InputPeerChannelFromMessage{Peer: value.Peer, MsgID: value.MsgID, ChannelID: value.ChannelID}, nil
	default:
		return nil, fmt.Errorf("client RPC overlay: unsupported InputChannel %T", source)
	}
}`, nil
	default:
		return "", fmt.Errorf("gen: unknown client RPC overlay converter helper %q", name)
	}
}
