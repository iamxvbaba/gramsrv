// Package store defines storage interfaces and protocol-layer DTOs, with no concrete implementations.
//
// Layout: the main package holds only interfaces (AuthKeyStore / UserStore / AuthorizationStore /
// CodeStore / UpdateStateStore / UpdateEventStore, etc.) and protocol DTOs; the three backend
// implementations each live in their own symmetric subpackage:
//   - store/memory     -- in-memory implementation, test double and local fallback
//   - store/postgres   -- PostgreSQL (pgx + sqlc-generated queries + golang-migrate migrations)
//   - store/redisstore -- Redis (go-redis)
//
// Type boundaries: interface signatures fall into two kinds --
//   - protocol artifacts use store's own DTOs: AuthKeyData, PhoneCode (they do not depend on tg.* and are not business entities);
//   - business entities use domain directly: UserStore / AuthorizationStore / MessageStore / UpdateEventStore
//     pass domain.User / domain.Authorization / domain.Message / domain.UpdateEvent.
package store
