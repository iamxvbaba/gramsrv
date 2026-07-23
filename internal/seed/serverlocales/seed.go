package serverlocales

import "embed"

// FS contains server-owned localized text catalogs.
//
//go:embed *.json
var FS embed.FS
