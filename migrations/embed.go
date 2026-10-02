package migrations

import "embed"

// FS contains the SQL migrations in the standalone binary.
//
//go:embed *.sql
var FS embed.FS
