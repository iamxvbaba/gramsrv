// Package loadtest houses the load-testing baseline for the message module.
//
// It holds only env-gated load cases (real PostgreSQL + Redis) and is not imported
// by production code. Goals and baseline methodology are in the "Load Baseline"
// section of docs/message-module.md.
//
// How to run (PowerShell):
//
//	$env:TELESRV_TEST_POSTGRES_DSN = "postgres://telesrv:telesrv@localhost:5432/telesrv?sslmode=disable"
//	$env:TELESRV_TEST_REDIS_ADDR   = "localhost:6399"
//	go test ./internal/loadtest/ -run TestMessageSendBaseline -v -count=1
//
// When the two environment variables above are unset the cases Skip outright, so
// they have no effect on a default `go test ./...`.
package loadtest
