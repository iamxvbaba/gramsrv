package gen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// BuildLayerPolicyTemplate returns a deterministic document containing only
// unresolved non-mechanical decisions. Empty actions are intentional: the
// document is a review artifact and cannot accidentally approve a behavior.
func BuildLayerPolicyTemplate(report LayerObligationReport) LayerPolicyDocument {
	unresolved := report.Unresolved()
	sort.Slice(unresolved, func(i, j int) bool { return unresolved[i].Key < unresolved[j].Key })
	document := LayerPolicyDocument{
		Version: LayerPolicyVersion,
		Entries: make([]LayerObligationPolicyEntry, 0, len(unresolved)),
	}
	for _, obligation := range unresolved {
		document.Entries = append(document.Entries, LayerObligationPolicyEntry{
			Key: obligation.Key,
			Resolution: LayerObligationResolution{
				Note: formatLayerPolicyTemplateNote(obligation),
			},
		})
	}
	return document
}

// MarshalLayerPolicyTemplate renders a stable, reviewable JSON policy
// skeleton. Feeding an unedited skeleton back into gotdgen fails closed because
// every action is empty.
func MarshalLayerPolicyTemplate(report LayerObligationReport) ([]byte, error) {
	data, err := json.MarshalIndent(BuildLayerPolicyTemplate(report), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("gen: marshal layer policy template: %w", err)
	}
	return append(data, '\n'), nil
}

func formatLayerPolicyTemplateNote(obligation LayerObligation) string {
	var parts []string
	parts = append(parts,
		fmt.Sprintf("kind=%s", obligation.Kind),
		fmt.Sprintf("layer=%d", obligation.Layer),
		fmt.Sprintf("direction=%s", obligation.Direction),
	)
	if obligation.Semantic.QName != "" {
		parts = append(parts, "semantic="+obligation.Semantic.String())
	}
	if obligation.WireID != 0 || obligation.OtherWireID != 0 {
		parts = append(parts, fmt.Sprintf("wire=%#08x->%#08x", obligation.WireID, obligation.OtherWireID))
	}
	if obligation.Field != "" || obligation.OtherField != "" {
		parts = append(parts, fmt.Sprintf("field=%s->%s", obligation.Field, obligation.OtherField))
	}
	if obligation.SourceType != "" || obligation.TargetType != "" {
		parts = append(parts, fmt.Sprintf("type=%s->%s", obligation.SourceType, obligation.TargetType))
	}
	if obligation.FlagWord != "" {
		parts = append(parts, fmt.Sprintf("flag=%s.%d", obligation.FlagWord, obligation.FlagBit))
	}
	if len(obligation.Fields) != 0 {
		parts = append(parts, "fields="+strings.Join(obligation.Fields, ","))
	}
	parts = append(parts,
		"source_shape="+obligation.SourceShape.String(),
		"target_shape="+obligation.TargetShape.String(),
	)
	return strings.Join(parts, "; ")
}
