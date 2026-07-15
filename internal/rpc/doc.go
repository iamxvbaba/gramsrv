// Package rpc 是按 TypeID 路由的 RPC 层：封装 tg.ServerDispatcher（或等价薄封装），
// 在 handler 边界把 iamxvbaba/td/tg 类型转换为内部 domain command/query，统一 tgerr.Error 到
// rpc_error 的映射，注入 auth_key_id/session_id/user_id/layer/设备/语言 等上下文，
// 并对未知 RPC 进入 compatibility trace（不静默吞掉，记入 docs/compatibility-matrix.md）。
package rpc
