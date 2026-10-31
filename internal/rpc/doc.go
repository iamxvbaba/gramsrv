// Package rpc is the RPC layer routed by semantic method: it wraps
// tlprofile.Dispatcher, converts iamxvbaba/td/tg types to internal domain
// command/query at the handler boundary, unifies the mapping from tgerr.Error to
// rpc_error, injects auth_key_id/session_id/user_id/layer/device/language
// context, and routes unknown RPCs into the compatibility trace (not silently
// swallowed, recorded in docs/compatibility-matrix.md).
package rpc
