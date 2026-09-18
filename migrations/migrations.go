package migrations

import "embed"

// FS contains the exact SQL files shipped with the binary.
//
//go:embed *.sql
var FS embed.FS
