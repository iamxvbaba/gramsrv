// Package store 定义存储接口与协议层 DTO，不含具体实现。
//
// 布局：主包只放接口（AuthKeyStore / UserStore / AuthorizationStore /
// CodeStore / UpdateStateStore / UpdateEventStore 等）与协议 DTO；三种后端实现各自独立成对称子包：
//   - store/memory     —— 内存实现，测试替身与本地兜底
//   - store/postgres   —— PostgreSQL（pgx + sqlc 生成查询 + golang-migrate 迁移）
//   - store/redisstore —— Redis（go-redis）
//
// 类型边界：接口签名分两类——
//   - 协议产物用 store 自有 DTO：AuthKeyData、PhoneCode（不依赖 tg.*，也非业务实体）；
//   - 业务实体直接用 domain：UserStore / AuthorizationStore / MessageStore / UpdateEventStore
//     收发 domain.User / domain.Authorization / domain.Message / domain.UpdateEvent。
package store
