// Package deploy provides embedded access to deployment-time resources
// (migration scripts).
package deploy

import "embed"

// Migrations is the embedded set of golang-migrate migration scripts
// (deploy/migrations/*.sql). It is executed at startup by store/postgres's
// migration runner, avoiding a runtime dependency on external file paths.
//
//go:embed migrations/*.sql
var Migrations embed.FS
