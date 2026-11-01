// Package tdesktop centralizes Telegram Desktop compatibility logic: target version/layer
// records, client patch notes, startup RPC ordering, compatibility-matrix helpers, and
// TDesktop-specific stubs and feature flags.
//
// Compatibility code is only allowed in this package; it must not be scattered into
// business handlers. Android/iOS will get their own internal/compat/android|ios later.
// Current baseline: TDesktop dev 9caf32dffc (v6.8.4+15), Layer 225.
//
// TDesktop compatibility logic is centralized here to keep it from spreading into the
// business services.
package tdesktop
