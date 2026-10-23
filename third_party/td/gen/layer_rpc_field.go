package gen

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"

	"github.com/iamxvbaba/td/gen/semantic"
)

type layerRPCAdmissionMetricKind uint8

const (
	layerRPCAdmissionMetricInvalid layerRPCAdmissionMetricKind = iota
	layerRPCAdmissionMetricVectorLength
	layerRPCAdmissionMetricBytesLength
	layerRPCAdmissionMetricInt32
)

func (k layerRPCAdmissionMetricKind) String() string {
	switch k {
	case layerRPCAdmissionMetricVectorLength:
		return "vector-length"
	case layerRPCAdmissionMetricBytesLength:
		return "bytes-length"
	case layerRPCAdmissionMetricInt32:
		return "int32"
	default:
		return "invalid"
	}
}

type layerRPCAdmissionCoverageStatus uint8

const (
	layerRPCAdmissionCoverageInvalid layerRPCAdmissionCoverageStatus = iota
	layerRPCAdmissionCoverageObservable
	layerRPCAdmissionCoverageMethodUnavailable
	layerRPCAdmissionCoverageUnmapped
	layerRPCAdmissionCoverageIncompatible
	layerRPCAdmissionCoverageAdapterUnproven
)

func (s layerRPCAdmissionCoverageStatus) String() string {
	switch s {
	case layerRPCAdmissionCoverageObservable:
		return "observable"
	case layerRPCAdmissionCoverageMethodUnavailable:
		return "method-unavailable"
	case layerRPCAdmissionCoverageUnmapped:
		return "unmapped"
	case layerRPCAdmissionCoverageIncompatible:
		return "incompatible"
	case layerRPCAdmissionCoverageAdapterUnproven:
		return "adapter-unproven"
	default:
		return "invalid"
	}
}

type layerRPCAdmissionFieldUse struct {
	Index        int
	ID           uint64
	Constant     string
	Metric       layerRPCAdmissionMetricKind
	MinWireSize  int
	ProfileIndex int
}

type layerRPCAdmissionFieldCoverage struct {
	Layer          int
	WireID         uint32
	ProfileField   string
	Status         layerRPCAdmissionCoverageStatus
	StatusConstant string
	Reason         string
}

type layerRPCAdmissionFieldPlan struct {
	Index          int
	ID             uint64
	Constant       string
	Method         semantic.SemanticKey
	MethodConstant string
	CanonicalField string
	Metric         layerRPCAdmissionMetricKind
	MetricConstant string
	Complete       bool
	Failure        string
	Coverage       []layerRPCAdmissionFieldCoverage
}

// buildLayerRPCAdmissionFields assigns stable public field identities and a
// private dense callback index. Identity depends only on semantic method
// qname and canonical field name; profile, CRC, ordinal and byte offset are
// deliberately excluded.
func buildLayerRPCAdmissionFields(model *layerRPCModel) error {
	if model == nil {
		return fmt.Errorf("gen: nil layer RPC model for admission fields")
	}
	type candidate struct {
		method         *layerRPCMethodPlan
		canonicalIndex int
		field          *semantic.FieldShape
		metric         layerRPCAdmissionMetricKind
	}
	var candidates []candidate
	for methodIndex := range model.Methods {
		method := &model.Methods[methodIndex]
		canonical := method.profile(model.CanonicalLayer)
		if canonical == nil || canonical.Definition == nil || canonical.Wrapper != nil || !method.Handler {
			continue
		}
		for fieldIndex := range canonical.Definition.Fields {
			field := &canonical.Definition.Fields[fieldIndex]
			metric, ok := layerRPCAdmissionMetric(&field.Type, field.Kind)
			if !ok {
				continue
			}
			candidates = append(candidates, candidate{method: method, canonicalIndex: fieldIndex, field: field, metric: metric})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].method.Key.QName != candidates[j].method.Key.QName {
			return candidates[i].method.Key.QName < candidates[j].method.Key.QName
		}
		return candidates[i].field.Name < candidates[j].field.Name
	})

	seenIDs := make(map[uint64]string, len(candidates))
	seenConstants := make(map[string]string, len(candidates))
	model.AdmissionFields = make([]layerRPCAdmissionFieldPlan, 0, len(candidates))
	for index, candidate := range candidates {
		id := layerRPCAdmissionFieldStableID(candidate.method.Key.QName, candidate.field.Name)
		identity := candidate.method.Key.QName + "." + candidate.field.Name
		if id == 0 {
			return fmt.Errorf("gen: layer RPC field %s hashes to reserved zero", identity)
		}
		if previous, duplicate := seenIDs[id]; duplicate {
			return fmt.Errorf("gen: layer RPC field ID %#016x collides for %s and %s", id, previous, identity)
		}
		constant := layerRPCAdmissionFieldConstant(candidate.method.Key.QName, candidate.field.Name)
		if previous, duplicate := seenConstants[constant]; duplicate {
			return fmt.Errorf("gen: layer RPC field constant %s collides for %s and %s", constant, previous, identity)
		}
		seenIDs[id] = identity
		seenConstants[constant] = identity
		plan := layerRPCAdmissionFieldPlan{
			Index:          index,
			ID:             id,
			Constant:       constant,
			Method:         candidate.method.Key,
			MethodConstant: candidate.method.Constant,
			CanonicalField: candidate.field.Name,
			Metric:         candidate.metric,
			MetricConstant: layerRPCAdmissionMetricGoConstant(candidate.metric),
			Complete:       true,
		}
		for profileIndex := range candidate.method.Profiles {
			profile := &candidate.method.Profiles[profileIndex]
			coverage, use, err := layerRPCAdmissionFieldProfileCoverage(candidate.method, profile, candidate.canonicalIndex, candidate.field, candidate.metric)
			if err != nil {
				return fmt.Errorf("gen: layer RPC admission field %s profile %d: %w", identity, profile.Layer, err)
			}
			if source, hook, wireID, ok := layerRPCAdmissionUnprovenOldOnlyTarget(model, candidate.method.Key, profile.Layer); ok {
				coverage = layerRPCAdmissionFieldCoverage{
					Layer:  profile.Layer,
					WireID: wireID,
					Status: layerRPCAdmissionCoverageAdapterUnproven,
					Reason: fmt.Sprintf("historical-only method %s adapter %q targets this semantic method without a field metric proof", source, hook),
				}
				use = nil
			}
			coverage.StatusConstant = layerRPCAdmissionCoverageGoConstant(coverage.Status)
			plan.Coverage = append(plan.Coverage, coverage)
			if use != nil {
				use.Index = index
				use.ID = id
				use.Constant = constant
				if use.ProfileIndex < 0 || use.ProfileIndex >= len(profile.Fields) || profile.Fields[use.ProfileIndex].Name != coverage.ProfileField {
					return fmt.Errorf("gen: layer RPC admission field %s profile %d resolved stale profile field index %d", identity, profile.Layer, use.ProfileIndex)
				}
				profile.Fields[use.ProfileIndex].Admission = use
			}
			if coverage.Status != layerRPCAdmissionCoverageObservable && coverage.Status != layerRPCAdmissionCoverageMethodUnavailable {
				plan.Complete = false
				if plan.Failure == "" {
					plan.Failure = fmt.Sprintf("profile %d is %s: %s", profile.Layer, coverage.Status, coverage.Reason)
				}
			}
		}
		model.AdmissionFields = append(model.AdmissionFields, plan)
	}
	return nil
}

func layerRPCAdmissionUnprovenOldOnlyTarget(model *layerRPCModel, target semantic.SemanticKey, layer int) (semantic.SemanticKey, string, uint32, bool) {
	if model == nil {
		return semantic.SemanticKey{}, "", 0, false
	}
	for methodIndex := range model.Methods {
		method := &model.Methods[methodIndex]
		profile := method.profile(layer)
		if profile == nil || profile.Availability != LayerAvailabilityProfileOnly || profile.Request != layerRPCAdapter || profile.Definition == nil {
			continue
		}
		for obligationIndex := range profile.RequestObligations {
			obligation := &profile.RequestObligations[obligationIndex]
			if obligation.Kind != LayerObligationOldOnly ||
				(obligation.Resolution.Action != LayerResolveAlias && obligation.Resolution.Action != LayerResolveAdapter) {
				continue
			}
			resolved, err := parseLayerPolicySemanticTarget(obligation.Resolution.Target)
			if err == nil && resolved == target {
				return method.Key, obligation.Resolution.Hook, profile.WireID, true
			}
		}
	}
	return semantic.SemanticKey{}, "", 0, false
}

func layerRPCAdmissionCoverageGoConstant(status layerRPCAdmissionCoverageStatus) string {
	switch status {
	case layerRPCAdmissionCoverageObservable:
		return "LayerRPCAdmissionFieldObservable"
	case layerRPCAdmissionCoverageMethodUnavailable:
		return "LayerRPCAdmissionFieldMethodUnavailable"
	case layerRPCAdmissionCoverageUnmapped:
		return "LayerRPCAdmissionFieldUnmapped"
	case layerRPCAdmissionCoverageIncompatible:
		return "LayerRPCAdmissionFieldIncompatible"
	case layerRPCAdmissionCoverageAdapterUnproven:
		return "LayerRPCAdmissionFieldAdapterUnproven"
	default:
		return "LayerRPCAdmissionFieldCoverageInvalid"
	}
}

func layerRPCAdmissionFieldProfileCoverage(
	method *layerRPCMethodPlan,
	profile *layerRPCMethodProfile,
	canonicalIndex int,
	canonicalField *semantic.FieldShape,
	metric layerRPCAdmissionMetricKind,
) (layerRPCAdmissionFieldCoverage, *layerRPCAdmissionFieldUse, error) {
	coverage := layerRPCAdmissionFieldCoverage{Layer: profile.Layer, Status: layerRPCAdmissionCoverageMethodUnavailable}
	if !layerRPCAdmissionProfileRoutable(method, profile) {
		coverage.Reason = "semantic method has no admitted ordinary route"
		return coverage, nil, nil
	}
	coverage.WireID = profile.WireID
	if profile.Conversion == nil || profile.Definition == nil || len(profile.Conversion.Fields) != len(profile.Definition.Fields) {
		coverage.Status = layerRPCAdmissionCoverageUnmapped
		coverage.Reason = "profile field mapping is absent or stale"
		return coverage, nil, nil
	}
	profileIndex := -1
	for index, mapping := range profile.Conversion.Fields {
		if mapping.CanonicalOrdinal != canonicalIndex {
			continue
		}
		if profileIndex != -1 {
			coverage.Status = layerRPCAdmissionCoverageUnmapped
			coverage.Reason = "multiple profile fields map to one canonical metric"
			return coverage, nil, nil
		}
		profileIndex = index
	}
	if profileIndex < 0 || profileIndex >= len(profile.Definition.Fields) || profileIndex >= len(profile.Fields) {
		coverage.Status = layerRPCAdmissionCoverageUnmapped
		coverage.Reason = "canonical metric has no profile field"
		return coverage, nil, nil
	}
	field := &profile.Definition.Fields[profileIndex]
	coverage.ProfileField = field.Name
	if field.Kind != semantic.FieldValue || profile.Fields[profileIndex].Shape != field {
		coverage.Status = layerRPCAdmissionCoverageIncompatible
		coverage.Reason = "mapped profile field is not a value field"
		return coverage, nil, nil
	}
	profileMetric, ok := layerRPCAdmissionMetric(&field.Type, field.Kind)
	if !ok || profileMetric != metric || !field.Type.Equal(canonicalField.Type) {
		coverage.Status = layerRPCAdmissionCoverageIncompatible
		coverage.Reason = fmt.Sprintf("mapped field metric/type %s is not canonical %s", profileMetric, metric)
		return coverage, nil, nil
	}
	if (field.Condition == nil) != (canonicalField.Condition == nil) {
		coverage.Status = layerRPCAdmissionCoverageIncompatible
		coverage.Reason = "conditional presence does not match canonical field"
		return coverage, nil, nil
	}
	obligation, err := layerCodecFieldAdapter(profile.Conversion, LayerDirectionProfileToCanonical, field.Name, canonicalField.Name)
	if err != nil {
		return coverage, nil, err
	}
	if obligation != nil {
		switch obligation.Resolution.Action {
		case LayerResolveAdapter:
			coverage.Status = layerRPCAdmissionCoverageAdapterUnproven
			coverage.Reason = fmt.Sprintf("adapter %q has no metric-preservation proof", obligation.Resolution.Hook)
			return coverage, nil, nil
		case LayerResolveAlias:
			if !layerRPCAdmissionPureRenameAlias(obligation, canonicalField, field) {
				coverage.Status = layerRPCAdmissionCoverageAdapterUnproven
				coverage.Reason = fmt.Sprintf("alias hook %q is not backed by an exact pure-rename proof", obligation.Resolution.Hook)
				return coverage, nil, nil
			}
		}
	}
	for _, obligation := range profile.Conversion.BodyObligations() {
		if obligation.Kind != LayerObligationAtomicFlagGroup ||
			!layerCodecDirectionMatches(obligation.Direction, LayerDirectionProfileToCanonical) ||
			obligation.Resolution.Action != LayerResolveAdapter ||
			!containsLayerCodecString(obligation.Fields, canonicalField.Name) {
			continue
		}
		coverage.Status = layerRPCAdmissionCoverageAdapterUnproven
		coverage.Reason = fmt.Sprintf("atomic adapter %q has no metric-preservation proof", obligation.Resolution.Hook)
		return coverage, nil, nil
	}
	coverage.Status = layerRPCAdmissionCoverageObservable
	coverage.Reason = "profile decoder exposes the canonical metric"
	return coverage, &layerRPCAdmissionFieldUse{
		Metric:       metric,
		MinWireSize:  layerRPCAdmissionVectorElementMinimumWireSize(&field.Type),
		ProfileIndex: profileIndex,
	}, nil
}

func layerRPCAdmissionPureRenameAlias(obligation *LayerObligation, canonical, profile *semantic.FieldShape) bool {
	return obligation != nil && canonical != nil && profile != nil &&
		obligation.Kind == LayerObligationAlias && obligation.Direction == LayerDirectionBoth &&
		obligation.Resolution.Action == LayerResolveAlias &&
		canonical.Name != profile.Name && obligation.Field == canonical.Name && obligation.OtherField == profile.Name &&
		obligation.SourceType == canonical.Type.String() && obligation.TargetType == profile.Type.String() &&
		aliasCompatible(*canonical, *profile)
}

func layerRPCAdmissionProfileRoutable(method *layerRPCMethodPlan, profile *layerRPCMethodProfile) bool {
	return method != nil && method.Handler && profile != nil && profile.Definition != nil &&
		profile.Availability != LayerAvailabilityProfileOnly && profile.Wrapper == nil &&
		profile.Request != layerRPCReject && profile.Request != layerRPCUnavailable &&
		profile.Result.Action != layerRPCReject && profile.Result.Action != layerRPCUnavailable
}

func layerRPCAdmissionMetric(ref *semantic.TypeRef, kind semantic.FieldKind) (layerRPCAdmissionMetricKind, bool) {
	if kind != semantic.FieldValue || ref == nil {
		return layerRPCAdmissionMetricInvalid, false
	}
	switch ref.Kind {
	case semantic.TypeVector:
		return layerRPCAdmissionMetricVectorLength, true
	case semantic.TypePrimitive:
		switch ref.QName {
		case "bytes", "Bytes", "string", "String":
			return layerRPCAdmissionMetricBytesLength, true
		case "int", "Int", "int32":
			return layerRPCAdmissionMetricInt32, true
		}
	}
	return layerRPCAdmissionMetricInvalid, false
}

func layerRPCAdmissionVectorElementMinimumWireSize(ref *semantic.TypeRef) int {
	if ref == nil || ref.Kind != semantic.TypeVector || ref.Arg == nil {
		return 0
	}
	return layerRPCAdmissionTypeMinimumWireSize(ref.Arg)
}

func layerRPCAdmissionTypeMinimumWireSize(ref *semantic.TypeRef) int {
	if ref == nil {
		return 0
	}
	switch ref.Kind {
	case semantic.TypePrimitive:
		switch ref.QName {
		case "int", "Int", "int32", "bool", "Bool", "true", "false", "True":
			return 4
		case "int53", "int64", "long", "Long", "double", "Double":
			return 8
		case "int128":
			return 16
		case "int256":
			return 32
		case "bytes", "Bytes", "string", "String":
			return 4
		case "Object":
			return 4
		}
	case semantic.TypeVector:
		if ref.QName == "Vector" && !ref.Bare && !ref.Percent {
			return 8
		}
		return 4
	case semantic.TypeNamed:
		if ref.Bare || ref.Percent {
			return 0
		}
		return 4
	case semantic.TypeGenericRef:
		return 4
	}
	return 0
}

func layerRPCAdmissionFieldStableID(qname, canonicalField string) uint64 {
	digest := sha256.Sum256([]byte("gotd.tl.layer-rpc-field.v1\x00" + qname + "\x00" + canonicalField))
	return binary.LittleEndian.Uint64(digest[:8])
}

func layerRPCAdmissionFieldConstant(qname, canonicalField string) string {
	parts := strings.Split(qname, ".")
	method := namespacedName(parts[len(parts)-1], parts[:len(parts)-1])
	return "LayerRPCField" + method + pascal(canonicalField)
}
