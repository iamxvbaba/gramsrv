package gen

import (
	"fmt"
	"go/scanner"
	"go/token"
	"strings"
)

// Policy-obligation comments differ by profile even when the emitted operation
// is identical. Compare Go tokens, preserving every identifier and literal, so
// comments and formatting cannot prevent sharing or erase a semantic change.
func layerCodecSourceKey(source string) string {
	fs := token.NewFileSet()
	file := fs.AddFile("codec-body", -1, len(source))
	var lexer scanner.Scanner
	lexer.Init(file, []byte(source), nil, 0)
	var key strings.Builder
	for {
		_, kind, literal := lexer.Scan()
		if kind == token.EOF {
			break
		}
		fmt.Fprintf(&key, "%d:%q;", kind, literal)
	}
	return key.String()
}

type layerCodecOperation uint8

const (
	layerCodecPreflightBody layerCodecOperation = iota
	layerCodecEncodeBody
	layerCodecDecodeBody
	layerCodecEncodeCheck
	layerCodecDecodeCheck
)

// operationGroups shares only the source and rejection used by this operation.
// ProfileGroups remains the complete graph used by sparse closure discovery.
func (w layerCodecWire) operationGroups(operation layerCodecOperation) []layerCodecProfileBody {
	type key struct{ source, rejection string }
	indices := make(map[key]int)
	var groups []layerCodecProfileBody
	for _, profile := range w.ProfileGroups {
		var k key
		switch operation {
		case layerCodecPreflightBody:
			k = key{profile.Preflight, profile.EncodeReject}
		case layerCodecEncodeBody:
			k = key{profile.Encode, profile.EncodeReject}
		case layerCodecDecodeBody:
			k = key{profile.Decode, profile.DecodeReject}
		case layerCodecEncodeCheck:
			k.rejection = profile.EncodeReject
		case layerCodecDecodeCheck:
			k.rejection = profile.DecodeReject
		default:
			panic("invalid layer codec operation")
		}
		if k.rejection != "" {
			k.source = "" // Rejected bodies are never emitted or executed.
		}
		k.source = layerCodecSourceKey(k.source)
		if index, ok := indices[k]; ok {
			groups[index].Layers = append(groups[index].Layers, profile.Layers...)
			continue
		}
		indices[k] = len(groups)
		profile.Layers = append([]int(nil), profile.Layers...)
		groups = append(groups, profile)
	}
	return groups
}

func (w layerCodecWire) PreflightGroups() []layerCodecProfileBody {
	return w.operationGroups(layerCodecPreflightBody)
}

func (w layerCodecWire) EncodeGroups() []layerCodecProfileBody {
	return w.operationGroups(layerCodecEncodeBody)
}

func (w layerCodecWire) DecodeGroups() []layerCodecProfileBody {
	return w.operationGroups(layerCodecDecodeBody)
}

func (w layerCodecWire) EncodeChecks() []layerCodecProfileBody {
	return w.operationGroups(layerCodecEncodeCheck)
}

func (w layerCodecWire) DecodeChecks() []layerCodecProfileBody {
	return w.operationGroups(layerCodecDecodeCheck)
}

type layerCodecSourceGroup struct {
	layers []int
	source string
}

// Each source is one operation's complete case body, including error policy.
func writeLayerCodecGroupedCases(out *strings.Builder, indent string, cases []layerCodecSourceGroup) {
	indices := make(map[string]int)
	var groups []layerCodecSourceGroup
	for _, current := range cases {
		key := layerCodecSourceKey(current.source)
		if index, ok := indices[key]; ok {
			groups[index].layers = append(groups[index].layers, current.layers...)
			continue
		}
		indices[key] = len(groups)
		current.layers = append([]int(nil), current.layers...)
		groups = append(groups, current)
	}
	for _, group := range groups {
		writeLayerCodecCaseLabel(out, indent, group.layers)
		out.WriteString(indentLayerCodec(group.source, indent+"\t"))
	}
}

// Class encoders dispatch on canonical Go types; only decoders consume CRCs.
// Keep the wire-specific groups intact for decoding historical constructors.
func groupLayerCodecClassEncoders(profiles []layerCodecClassProfileGroup) []layerCodecClassProfileGroup {
	indices := make(map[string]int)
	var groups []layerCodecClassProfileGroup
	for _, profile := range profiles {
		var signature strings.Builder
		seen := make(map[string]bool)
		for _, constructor := range profile.Constructors {
			if !seen[constructor.GoType] {
				signature.WriteString(constructor.GoType)
				signature.WriteByte(';')
				seen[constructor.GoType] = true
			}
		}
		key := signature.String()
		if index, ok := indices[key]; ok {
			groups[index].Layers = append(groups[index].Layers, profile.Layers...)
			continue
		}
		indices[key] = len(groups)
		profile.Layers = append([]int(nil), profile.Layers...)
		groups = append(groups, profile)
	}
	return groups
}
