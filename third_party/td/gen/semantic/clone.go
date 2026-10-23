package semantic

import (
	"fmt"
	"reflect"

	"github.com/gotd/tl"
)

func cloneSchema(source *tl.Schema) *tl.Schema {
	if source == nil {
		return nil
	}
	result := &tl.Schema{
		Layer: source.Layer,
	}
	result.Classes = append(result.Classes, source.Classes...)
	result.Definitions = make([]tl.SchemaDefinition, len(source.Definitions))
	for i, schemaDef := range source.Definitions {
		result.Definitions[i] = cloneSchemaDefinition(schemaDef)
	}
	return result
}

func cloneSchemaDefinition(source tl.SchemaDefinition) tl.SchemaDefinition {
	result := source
	result.Annotations = append([]tl.Annotation(nil), source.Annotations...)
	result.Definition.Namespace = append([]string(nil), source.Definition.Namespace...)
	result.Definition.GenericParams = append([]string(nil), source.Definition.GenericParams...)
	result.Definition.Type = cloneTLType(source.Definition.Type)
	result.Definition.Params = make([]tl.Parameter, len(source.Definition.Params))
	for i, parameter := range source.Definition.Params {
		result.Definition.Params[i] = parameter
		result.Definition.Params[i].Type = cloneTLType(parameter.Type)
		if parameter.Flag != nil {
			flag := *parameter.Flag
			result.Definition.Params[i].Flag = &flag
		}
	}
	return result
}

func cloneTLType(source tl.Type) tl.Type {
	result := source
	result.Namespace = append([]string(nil), source.Namespace...)
	if source.GenericArg != nil {
		arg := cloneTLType(*source.GenericArg)
		result.GenericArg = &arg
	}
	return result
}

func mergeSchema(target, overlay *tl.Schema) error {
	ids := make(map[uint32]tl.SchemaDefinition, len(target.Definitions))
	for _, definition := range target.Definitions {
		ids[definition.Definition.ID] = definition
	}
	for _, definition := range overlay.Definitions {
		if existing, exists := ids[definition.Definition.ID]; exists {
			if compatibleOverlayDefinition(existing, definition) {
				continue
			}
			return fmt.Errorf(
				"E_OVERLAY_COLLISION: wire ID %#08x target %s:%s conflicts with overlay %s:%s",
				definition.Definition.ID,
				existing.Category,
				qualifyDefinition(existing.Definition),
				definition.Category,
				qualifyDefinition(definition.Definition),
			)
		}
		target.Definitions = append(target.Definitions, cloneSchemaDefinition(definition))
		ids[definition.Definition.ID] = definition
	}

	classes := make(map[string]struct{}, len(target.Classes))
	for _, class := range target.Classes {
		classes[class.Name] = struct{}{}
	}
	for _, class := range overlay.Classes {
		if _, exists := classes[class.Name]; exists {
			continue
		}
		target.Classes = append(target.Classes, class)
		classes[class.Name] = struct{}{}
	}
	if target.Layer == 0 {
		target.Layer = overlay.Layer
	}
	return nil
}

func compatibleOverlayDefinition(target, overlay tl.SchemaDefinition) bool {
	if target.Category != overlay.Category {
		return false
	}
	if qualifyDefinition(target.Definition) != qualifyDefinition(overlay.Definition) {
		return false
	}
	// Annotations are documentation rather than declaration payload. The full
	// parsed Definition includes the explicit ID, generic parameters, ordered
	// fields, flags, bare/boxed TypeRefs, result type, and base marker.
	return reflect.DeepEqual(target.Definition, overlay.Definition)
}
