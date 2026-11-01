// Package memory provides in-memory implementations of the store interfaces,
// used as test doubles and a local-development fallback.
//
// It is symmetric with store/postgres and store/redisstore: the store root
// package only defines interfaces and DTOs; each of the three backends lives in
// its own package.
package memory
