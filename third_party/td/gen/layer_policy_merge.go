package gen

import (
	"encoding/json"
	"fmt"
	"sort"
)

// LayerPolicyAuditDocument is the fail-safe migration view produced when a
// schema import changes obligation keys. Retained entries still match an exact
// generated obligation, stale entries are never copied to the merged policy,
// and New contains unresolved, deliberately unapproved skeleton entries.
type LayerPolicyAuditDocument struct {
	Version  int                          `json:"version"`
	Retained []LayerObligationPolicyEntry `json:"retained"`
	Stale    []LayerObligationPolicyEntry `json:"stale"`
	New      []LayerObligationPolicyEntry `json:"new"`
}

// AuditLayerPolicy partitions an existing strict policy without treating stale
// keys as approval. It still validates duplicates and every retained action.
func AuditLayerPolicy(report LayerObligationReport, policy LayerObligationPolicy) (LayerPolicyAuditDocument, error) {
	document := LayerPolicyAuditDocument{Version: LayerPolicyVersion}
	generated := make(map[LayerObligationKey]struct{}, len(report.Obligations))
	for _, obligation := range report.Obligations {
		if _, duplicate := generated[obligation.Key]; duplicate {
			return LayerPolicyAuditDocument{}, fmt.Errorf("gen: duplicate generated layer obligation key %q", obligation.Key)
		}
		generated[obligation.Key] = struct{}{}
	}

	seen := make(map[LayerObligationKey]struct{}, len(policy.Entries))
	for _, entry := range policy.Entries {
		if _, duplicate := seen[entry.Key]; duplicate {
			return LayerPolicyAuditDocument{}, fmt.Errorf("gen: E_DUPLICATE_LAYER_POLICY: key %q is repeated", entry.Key)
		}
		seen[entry.Key] = struct{}{}
		if _, retained := generated[entry.Key]; retained {
			document.Retained = append(document.Retained, entry)
		} else {
			document.Stale = append(document.Stale, entry)
		}
	}

	// Reuse the strict policy applier for retained-entry validation and for the
	// exact post-policy unresolved view. Stale entries never reach it.
	obligations := append([]LayerObligation(nil), report.Obligations...)
	applied, err := applyLayerObligationPolicy(obligations, LayerObligationPolicy{Entries: document.Retained})
	if err != nil {
		return LayerPolicyAuditDocument{}, err
	}
	for _, obligation := range applied.Unresolved() {
		document.New = append(document.New, LayerObligationPolicyEntry{
			Key: obligation.Key,
			Resolution: LayerObligationResolution{
				Note: formatLayerPolicyTemplateNote(obligation),
			},
		})
	}
	sortLayerPolicyEntries(document.Retained)
	sortLayerPolicyEntries(document.Stale)
	sortLayerPolicyEntries(document.New)
	return document, nil
}

// MergedPolicy drops stale entries, retains only exact reviewed decisions, and
// appends the unresolved skeleton. The result intentionally fails normal
// generation until every new action has been reviewed.
func (d LayerPolicyAuditDocument) MergedPolicy() LayerPolicyDocument {
	entries := make([]LayerObligationPolicyEntry, 0, len(d.Retained)+len(d.New))
	entries = append(entries, d.Retained...)
	entries = append(entries, d.New...)
	sortLayerPolicyEntries(entries)
	return LayerPolicyDocument{Version: LayerPolicyVersion, Entries: entries}
}

// MarshalLayerPolicyAudit renders a deterministic retained/stale/new report.
func MarshalLayerPolicyAudit(document LayerPolicyAuditDocument) ([]byte, error) {
	if document.Version != LayerPolicyVersion {
		return nil, fmt.Errorf("gen: unsupported layer policy audit version %d", document.Version)
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("gen: marshal layer policy audit: %w", err)
	}
	return append(data, '\n'), nil
}

// MarshalLayerPolicyDocument renders a deterministic strict policy document.
func MarshalLayerPolicyDocument(document LayerPolicyDocument) ([]byte, error) {
	if document.Version != LayerPolicyVersion {
		return nil, fmt.Errorf("gen: unsupported layer policy version %d", document.Version)
	}
	entries := append([]LayerObligationPolicyEntry(nil), document.Entries...)
	sortLayerPolicyEntries(entries)
	document.Entries = entries
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("gen: marshal layer policy: %w", err)
	}
	return append(data, '\n'), nil
}

func sortLayerPolicyEntries(entries []LayerObligationPolicyEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
}
