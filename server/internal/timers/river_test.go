package timers

import (
	"testing"

	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivermigrate"
)

// riverSchemaVersion is the last River migration copied into goose (00380_river_queue.sql).
const riverSchemaVersion = 8

// TestRiverMigrationsCurrent fails when a River upgrade ships a migration the goose migrations
// do not have yet: copy it into a new goose migration and raise riverSchemaVersion.
func TestRiverMigrationsCurrent(t *testing.T) {
	m, err := rivermigrate.New(riverdatabasesql.New(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	all := m.AllVersions()
	if got := all[len(all)-1].Version; got != riverSchemaVersion {
		t.Fatalf("River's schema is at version %d, the goose migrations at %d", got, riverSchemaVersion)
	}
}
