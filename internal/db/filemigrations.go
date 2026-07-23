package db

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"

	"github.com/deploykit/backend/migrations"
)

// numberedMigrationRe matches the numbered, idempotent migration files (e.g.
// "049_tracker_delivery_auto_completion.sql"). It deliberately excludes
// ALL_MIGRATIONS.sql, which is a from-scratch bootstrap script (plain CREATE
// TABLE, no IF NOT EXISTS) meant for provisioning a brand-new database, not
// for repeated execution against an already-migrated one.
var numberedMigrationRe = regexp.MustCompile(`^[0-9]{3}_.*\.sql$`)

// RunFileMigrations applies every embedded numbered migration file, in order,
// on every startup. Each file is written with IF NOT EXISTS / DO-block guards
// so re-running already-applied migrations is a no-op — the same idiom the
// hand-written Migrate* functions in internal/api/handlers already use. This
// closes the gap where a migration file is added to the repo but never
// actually reaches the production schema, since the binary now carries and
// applies its own migrations instead of depending on a manual psql step.
func (d *DB) RunFileMigrations(ctx context.Context) error {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return fmt.Errorf("failed to read embedded migrations: %w", err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && numberedMigrationRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		sqlBytes, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("failed to read migration %s: %w", name, err)
		}
		if _, err := d.pool.Exec(ctx, string(sqlBytes)); err != nil {
			log.Printf("⚠ migration %s warning: %v", name, err)
			continue
		}
	}
	return nil
}
