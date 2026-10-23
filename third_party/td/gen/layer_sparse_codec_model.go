package gen

import (
	"fmt"
	"go/scanner"
	"go/token"
	"regexp"
	"sort"
	"strings"
)

// buildLayerSparseCodecModel reuses the already policy-validated typed emitter
// but collapses every universally direct wire to a canonical bare-method call.
// Only dirty closures retain field-level conversion source.
func (g *Generator) buildLayerSparseCodecModel(pkg string) (*layerCodecModel, error) {
	model, err := g.buildLayerCodecModel(pkg)
	if err != nil {
		return nil, err
	}
	model.Sparse = true
	execution, err := g.buildLayerExecutionModel()
	if err != nil {
		return nil, err
	}
	bindings, err := g.buildLayerBindings()
	if err != nil {
		return nil, err
	}
	dirty := make(map[uint32]struct{})
	dirtySemantics := make(map[string]layerExecutionRoute)
	dirtyFamilies := make(map[string]struct{})
	for _, route := range execution.Routes {
		// Generic envelopes, unavailable routes and historical-only method
		// adapters belong to RPC admission, not the object converter closure.
		if route.Mode == layerExecutionRewrite || route.Mode == layerExecutionPolicy {
			binding := bindings.definition(route.Key)
			if binding != nil && len(binding.Definition.GenericParams) != 0 {
				continue
			}
			dirty[route.WireID] = struct{}{}
			dirtySemantics[route.Key.String()] = route
		}
	}
	// Reviewed historical constructors are ordinary typed codecs whose wire IDs
	// do not appear as canonical semantic routes. They must retain their static
	// adapter bodies in the sparse closure; treating them as canonical-direct
	// would call the target type's incompatible EncodeBare/DecodeBare methods.
	for index := range model.Wires {
		if model.Wires[index].HistoricalOnly {
			dirty[model.Wires[index].WireID] = struct{}{}
		}
	}

	canonicalNames := make(map[string]struct{})
	for _, binding := range bindings.Definitions {
		if binding != nil && binding.Structure != nil {
			canonicalNames[binding.Structure.Name] = struct{}{}
		}
	}
	for _, class := range bindings.Classes {
		if class == nil {
			continue
		}
		if class.Interface != nil {
			canonicalNames[class.Interface.Name] = struct{}{}
		}
		if class.Backend.Name != "" {
			canonicalNames[class.Backend.Name] = struct{}{}
		}
	}

	for index := range model.Wires {
		wire := &model.Wires[index]
		if _, isDirty := dirty[wire.WireID]; !isDirty && !wire.ProfileOnly {
			wire.SparseDirect = true
			var layers []int
			for _, profile := range wire.Profiles {
				if profile.EncodeReject == "" && profile.DecodeReject == "" {
					layers = append(layers, profile.Layer)
				}
			}
			sort.Ints(layers)
			if len(layers) == 0 {
				return nil, fmt.Errorf("gen: sparse direct wire %#08x has no accepted profile", wire.WireID)
			}
			wire.ProfileGroups = []layerCodecProfileBody{{
				Layers:    layers,
				Preflight: "return 1, nil\n",
				Encode:    "return value.EncodeBare(b)\n",
				Decode:    "if err := value.DecodeBare(b); err != nil { return nil, err }\nreturn value, nil\n",
			}}
			wire.Encodable = true
			wire.Decodable = true
		}
		wire.CanonicalType = qualifyLayerSparseIdentifiers(wire.CanonicalType, canonicalNames)
		for profile := range wire.ProfileGroups {
			body := &wire.ProfileGroups[profile]
			body.Preflight = qualifyLayerSparseIdentifiers(body.Preflight, canonicalNames)
			body.Encode = qualifyLayerSparseIdentifiers(body.Encode, canonicalNames)
			body.Decode = qualifyLayerSparseIdentifiers(body.Decode, canonicalNames)
		}
	}
	for id := range dirty {
		wire := findLayerSparseWire(model.Wires, id)
		if wire == nil || wire.ProfileOnly {
			return nil, fmt.Errorf("gen: sparse dirty wire %#08x has no canonical codec", id)
		}
		model.SparseDecode = append(model.SparseDecode, layerSparseDecodeEntry{
			WireID: id, Hex: fmt.Sprintf("%08x", id), Decode: wire.DecodeName,
		})
	}
	sort.Slice(model.SparseDecode, func(i, j int) bool { return model.SparseDecode[i].WireID < model.SparseDecode[j].WireID })
	semanticKeys := make([]string, 0, len(dirtySemantics))
	for key := range dirtySemantics {
		semanticKeys = append(semanticKeys, key)
	}
	sort.Strings(semanticKeys)
	for _, key := range semanticKeys {
		route := dirtySemantics[key]
		binding := bindings.definition(route.Key)
		if binding == nil || binding.Structure == nil {
			return nil, fmt.Errorf("gen: sparse dirty semantic %s has no canonical binding", route.Key)
		}
		model.SparseEncode = append(model.SparseEncode, layerSparseEncodeEntry{
			Semantic: strings.TrimPrefix(layerSemanticConstant(route.Key), "Layer"),
			GoType:   "tg." + binding.Structure.Name,
			Family:   binding.Structure.Name,
		})
		dirtyFamilies[binding.Structure.Name] = struct{}{}
	}
	resultSources, err := g.buildLayerSparseResults(model, canonicalNames)
	if err != nil {
		return nil, err
	}
	overlay, err := g.buildLayerClientRPCOverlaySourceModel(pkg)
	if err != nil {
		return nil, fmt.Errorf("gen: sparse client RPC overlay: %w", err)
	}
	var overlaySources []string
	for index := range overlay.Helpers {
		overlay.Helpers[index] = qualifyLayerSparseIdentifiers(overlay.Helpers[index], canonicalNames)
		overlaySources = append(overlaySources, overlay.Helpers[index])
	}
	for overlayIndex := range overlay.Overlays {
		for methodIndex := range overlay.Overlays[overlayIndex].Methods {
			method := &overlay.Overlays[overlayIndex].Methods[methodIndex]
			method.Declaration = qualifyLayerSparseIdentifiers(method.Declaration, canonicalNames)
			overlaySources = append(overlaySources, method.Declaration)
		}
	}
	model.SparseOverlay = overlay
	rootSources := append(resultSources, overlaySources...)
	if err := pruneLayerSparseClosure(model, dirty, dirtyFamilies, rootSources); err != nil {
		return nil, err
	}
	for index := range model.FamilyDeclarations {
		model.FamilyDeclarations[index] = qualifyLayerSparseIdentifiers(model.FamilyDeclarations[index], canonicalNames)
	}
	for index := range model.ClassDeclarations {
		model.ClassDeclarations[index] = qualifyLayerSparseIdentifiers(model.ClassDeclarations[index], canonicalNames)
	}
	for index := range model.DynamicDeclarations {
		model.DynamicDeclarations[index] = qualifyLayerSparseIdentifiers(model.DynamicDeclarations[index], canonicalNames)
	}
	for index := range model.Hooks {
		model.Hooks[index].Signature = qualifyLayerSparseIdentifiers(model.Hooks[index].Signature, canonicalNames)
	}
	sort.Slice(model.Hooks, func(i, j int) bool { return model.Hooks[i].Name < model.Hooks[j].Name })
	model.Declarations = nil
	model.WireBuckets = nil
	const bucketCount = 64
	model.WireBuckets = make([]layerCodecWireBucket, bucketCount)
	for index := range model.WireBuckets {
		model.WireBuckets[index].Index = index
	}
	for _, wire := range model.Wires {
		bucket := int(wire.WireID % bucketCount)
		model.WireBuckets[bucket].Wires = append(model.WireBuckets[bucket].Wires, wire)
	}
	return model, nil
}

func findLayerSparseWire(wires []layerCodecWire, id uint32) *layerCodecWire {
	for index := range wires {
		if wires[index].WireID == id {
			return &wires[index]
		}
	}
	return nil
}

var (
	layerSparseWireReference     = regexp.MustCompile(`\blayer(Preflight|Encode|Decode)Wire([0-9a-f]{8})`)
	layerSparseClassReference    = regexp.MustCompile(`\b(layer(?:Project|Preflight|Encode|Decode)Class[A-Za-z0-9_]+)\b`)
	layerSparseFamilyReference   = regexp.MustCompile(`\b(layer(?:Project|Preflight|Encode)Family[A-Za-z0-9_]+)\b`)
	layerSparseClassDeclaration  = regexp.MustCompile(`\bfunc\s+(layer(?:Project|Preflight|Encode|Decode)Class[A-Za-z0-9_]+)\s*\(`)
	layerSparseFamilyDeclaration = regexp.MustCompile(`\bfunc\s+(layer(?:Project|Preflight|Encode)Family[A-Za-z0-9_]+)\s*\(`)
)

type layerSparseReferenceSet struct {
	wires    map[uint32]struct{}
	encodes  map[uint32]struct{}
	decodes  map[uint32]struct{}
	classes  map[string]struct{}
	families map[string]struct{}
	dynamic  bool
}

func newLayerSparseReferenceSet() *layerSparseReferenceSet {
	return &layerSparseReferenceSet{
		wires: make(map[uint32]struct{}), encodes: make(map[uint32]struct{}), decodes: make(map[uint32]struct{}),
		classes: make(map[string]struct{}), families: make(map[string]struct{}),
	}
}

func (r *layerSparseReferenceSet) collect(source string) error {
	for _, match := range layerSparseWireReference.FindAllStringSubmatch(source, -1) {
		var id uint32
		if _, err := fmt.Sscanf(match[2], "%08x", &id); err != nil {
			return fmt.Errorf("gen: parse sparse wire reference %q: %w", match[2], err)
		}
		r.wires[id] = struct{}{}
		switch match[1] {
		case "Encode":
			r.encodes[id] = struct{}{}
		case "Decode":
			r.decodes[id] = struct{}{}
		}
	}
	for _, match := range layerSparseClassReference.FindAllStringSubmatch(source, -1) {
		r.classes[match[1]] = struct{}{}
	}
	for _, match := range layerSparseFamilyReference.FindAllStringSubmatch(source, -1) {
		r.families[match[1]] = struct{}{}
	}
	if strings.Contains(source, "layerDecodeDynamicObject") || strings.Contains(source, "layerEncodeDynamicObject") || strings.Contains(source, "layerPreflightDynamicObject") {
		r.dynamic = true
	}
	return nil
}

func pruneLayerSparseClosure(model *layerCodecModel, roots map[uint32]struct{}, rootFamilies map[string]struct{}, rootSources []string) error {
	if model == nil {
		return fmt.Errorf("gen: nil sparse codec model")
	}
	wires := make(map[uint32]*layerCodecWire, len(model.Wires))
	for index := range model.Wires {
		wires[model.Wires[index].WireID] = &model.Wires[index]
	}
	classes := make(map[string]string, len(model.ClassDeclarations))
	for _, declaration := range model.ClassDeclarations {
		if strings.Contains(declaration, "func layerPreflight") {
			continue
		}
		match := layerSparseClassDeclaration.FindStringSubmatch(declaration)
		if len(match) == 2 {
			classes[match[1]] = declaration
		}
	}
	families := make(map[string]string, len(model.FamilyDeclarations))
	for _, declaration := range model.FamilyDeclarations {
		if strings.Contains(declaration, "func layerPreflight") {
			continue
		}
		match := layerSparseFamilyDeclaration.FindStringSubmatch(declaration)
		if len(match) == 2 {
			families[match[1]] = declaration
		}
	}

	references := newLayerSparseReferenceSet()
	for id := range roots {
		references.wires[id] = struct{}{}
	}
	for suffix := range rootFamilies {
		references.families["layerEncodeFamily"+suffix] = struct{}{}
	}
	for _, source := range rootSources {
		if err := references.collect(source); err != nil {
			return err
		}
	}
	processedWires := make(map[uint32]struct{})
	processedClasses := make(map[string]struct{})
	processedFamilies := make(map[string]struct{})
	for {
		progress := false
		for id := range references.wires {
			if _, ok := processedWires[id]; ok {
				continue
			}
			wire := wires[id]
			if wire == nil {
				return fmt.Errorf("gen: sparse closure references absent wire %#08x", id)
			}
			processedWires[id] = struct{}{}
			progress = true
			if wire.SparseDirect || wire.ProfileOnly {
				continue
			}
			for _, body := range wire.ProfileGroups {
				if err := references.collect(body.Encode + body.Decode); err != nil {
					return err
				}
			}
		}
		for name := range references.classes {
			if _, ok := processedClasses[name]; ok {
				continue
			}
			declaration, ok := classes[name]
			if !ok {
				return fmt.Errorf("gen: sparse closure references absent class function %s", name)
			}
			processedClasses[name] = struct{}{}
			progress = true
			if err := references.collect(declaration); err != nil {
				return err
			}
		}
		for name := range references.families {
			if _, ok := processedFamilies[name]; ok {
				continue
			}
			declaration, ok := families[name]
			if !ok {
				return fmt.Errorf("gen: sparse closure references absent family function %s", name)
			}
			processedFamilies[name] = struct{}{}
			progress = true
			if err := references.collect(declaration); err != nil {
				return err
			}
		}
		if !progress {
			break
		}
	}
	if references.dynamic {
		return fmt.Errorf("gen: E_SPARSE_DYNAMIC_CLOSURE: ordinary object converter reached generic Object; wrappers must be lowered by RPC admission")
	}

	keptWires := make([]layerCodecWire, 0, len(processedWires))
	for _, wire := range model.Wires {
		if _, ok := processedWires[wire.WireID]; ok {
			if wire.SparseDirect {
				_, wire.SparseEncode = references.encodes[wire.WireID]
				_, wire.SparseDecode = references.decodes[wire.WireID]
				if !wire.SparseEncode && !wire.SparseDecode {
					return fmt.Errorf("gen: sparse direct wire %#08x has no operation reference", wire.WireID)
				}
			}
			keptWires = append(keptWires, wire)
		}
	}
	model.Wires = keptWires
	model.ClassDeclarations = model.ClassDeclarations[:0]
	classNames := make([]string, 0, len(processedClasses))
	for name := range processedClasses {
		classNames = append(classNames, name)
	}
	sort.Strings(classNames)
	for _, name := range classNames {
		model.ClassDeclarations = append(model.ClassDeclarations, classes[name])
	}
	model.FamilyDeclarations = model.FamilyDeclarations[:0]
	familyNames := make([]string, 0, len(processedFamilies))
	for name := range processedFamilies {
		familyNames = append(familyNames, name)
	}
	sort.Strings(familyNames)
	for _, name := range familyNames {
		model.FamilyDeclarations = append(model.FamilyDeclarations, families[name])
	}
	model.DynamicDeclarations = nil
	return nil
}

// qualifyLayerSparseIdentifiers rewrites only Go identifier tokens. Generated
// error strings and selector fields are left byte-for-byte unchanged.
func qualifyLayerSparseIdentifiers(source string, names map[string]struct{}) string {
	if source == "" || len(names) == 0 {
		return source
	}
	fileSet := token.NewFileSet()
	file := fileSet.AddFile("fragment.go", -1, len(source))
	var lexer scanner.Scanner
	lexer.Init(file, []byte(source), nil, scanner.ScanComments)
	last := 0
	var out strings.Builder
	for {
		position, kind, literal := lexer.Scan()
		if kind == token.EOF {
			break
		}
		if kind != token.IDENT {
			continue
		}
		if _, ok := names[literal]; !ok {
			continue
		}
		offset := fileSet.Position(position).Offset
		previous := offset - 1
		for previous >= 0 && (source[previous] == ' ' || source[previous] == '\t' || source[previous] == '\r' || source[previous] == '\n') {
			previous--
		}
		if previous >= 0 && source[previous] == '.' {
			continue
		}
		out.WriteString(source[last:offset])
		out.WriteString("tg.")
		out.WriteString(literal)
		last = offset + len(literal)
	}
	if last == 0 {
		return source
	}
	out.WriteString(source[last:])
	return out.String()
}
