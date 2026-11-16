// Package migrations holds the goose SQL migrations, embedded into atctl.
//
// 00001 is the schema of the retired Python v1 platform written with IF NOT
// EXISTS (a no-op on the production database, the starting tables on an empty
// one). Later migrations build v2 on top; 00120 drops what only v1 used
// (docs/v2/data-model.md §0).
package migrations

import "embed"

// FS contains every *.sql migration at its root.
//
//go:embed *.sql
var FS embed.FS
