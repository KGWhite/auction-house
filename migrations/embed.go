// Package migrations embeds the goose SQL migrations so that tests and tools
// can apply the schema without shelling out to the goose CLI.
package migrations

import "embed"

// FS holds every *.sql migration file.
//
//go:embed *.sql
var FS embed.FS
