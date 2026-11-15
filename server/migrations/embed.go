// Package migrations holds the goose SQL migrations, embedded into atctl.
//
// 00001 is the v1 schema written with IF NOT EXISTS, so it is a no-op on the
// existing production database and builds the v1 tables on an empty one. Every
// later migration only adds tables, columns with defaults and indexes
// (docs/v2/data-model.md §0).
package migrations

import "embed"

// FS contains every *.sql migration at its root.
//
//go:embed *.sql
var FS embed.FS
