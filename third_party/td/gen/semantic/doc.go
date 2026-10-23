// Package semantic builds a layer-aware, Go-backend-independent model of TL
// schemas.
//
// The package deliberately keeps wire identity (layer and constructor ID),
// wire shape, and canonical Go bindings as separate concerns. Code generators
// can therefore compare multiple historical schemas without changing the
// canonical tg package or relying on constructor IDs as shape identifiers.
package semantic
