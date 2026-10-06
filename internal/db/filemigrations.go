package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"regexp"
	"sort"
	"strings"

	"github.com/deploykit/backend/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// numberedMigrationRe matches the numbered migration files (e.g.
// "049_tracker_delivery_auto_completion.sql"). It deliberately excludes
// ALL_MIGRATIONS.sql, which is a from-scratch bootstrap script (plain CREATE
// TABLE, no IF NOT EXISTS) meant for provisioning a brand-new database, not
// for repeated execution against an already-migrated one.
var numberedMigrationRe = regexp.MustCompile(`^[0-9]{3}_.*\.sql$`)

// migrationLockKey is an arbitrary pg_advisory_lock key, held while
// migrations run so two instances booting at once (e.g. overlapping Railway
// deploys) never apply the same file concurrently.
const migrationLockKey int64 = 7404202610020001

// MigrationReport is the outcome of one RunFileMigrations pass.
type MigrationReport struct {
	Applied        []string // ran and recorded in schema_migrations this boot
	AlreadyApplied int      // already recorded, not run
	Failed         []string // errored and rolled back; not recorded, retried next boot
	Changed        []string // recorded, but the file's content changed since — not re-run
}

// RunFileMigrations applies every embedded numbered migration file that
// isn't yet recorded in the schema_migrations ledger, in filename order,
// each in its own transaction together with its ledger row — so each file
// runs exactly once. It used to run every file on every boot, which turned
// any non-idempotent statement (006's DROP TABLE driver_documents, 042's
// DROP TABLE promo_codes) into data loss on each deploy.
//
// On a database without the ledger (the first boot of this code) every file
// runs once more, exactly like every earlier boot did, and the ones that
// succeed are recorded. Files that fail are logged, left unrecorded and
// retried on the next boot, so a file that has been failing silently on
// every boot shows up in the returned error instead of being marked applied.
//
// A recorded file is never re-run, even if edited: schema changes go in a
// new numbered file.
func (d *DB) RunFileMigrations(ctx context.Context) error {
	rep, err := runFileMigrations(ctx, d.pool, migrations.FS)
	if err != nil {
		return err
	}
	log.Printf("migrations: %d applied this boot, %d already applied, %d failed",
		len(rep.Applied), rep.AlreadyApplied, len(rep.Failed))
	for _, name := range rep.Changed {
		log.Printf("⚠ migration %s changed after it was applied — not re-run; put schema changes in a new file", name)
	}
	if len(rep.Failed) > 0 {
		return fmt.Errorf("%d migration(s) failed and are not recorded (retried next boot): %s",
			len(rep.Failed), strings.Join(rep.Failed, ", "))
	}
	return nil
}

func runFileMigrations(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) (MigrationReport, error) {
	var rep MigrationReport

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return rep, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return rep, fmt.Errorf("take migration lock: %w", err)
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey) //nolint:errcheck — released with the session anyway

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   TEXT PRIMARY KEY,
			checksum   TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return rep, fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[string]string{}
	rows, err := conn.Query(ctx, `SELECT filename, checksum FROM schema_migrations`)
	if err != nil {
		return rep, fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var name, sum string
		if err := rows.Scan(&name, &sum); err != nil {
			rows.Close()
			return rep, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[name] = sum
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("read schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return rep, fmt.Errorf("failed to read embedded migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && numberedMigrationRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		sqlBytes, err := fs.ReadFile(fsys, name)
		if err != nil {
			return rep, fmt.Errorf("failed to read migration %s: %w", name, err)
		}
		sum := sha256.Sum256(sqlBytes)
		checksum := hex.EncodeToString(sum[:])

		if prev, ok := applied[name]; ok {
			rep.AlreadyApplied++
			if prev != checksum {
				rep.Changed = append(rep.Changed, name)
			}
			continue
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return rep, fmt.Errorf("begin %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			log.Printf("⚠ migration %s failed (not recorded, retried next boot): %v", name, err)
			rep.Failed = append(rep.Failed, name)
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)`, name, checksum); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return rep, fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			log.Printf("⚠ migration %s failed at commit (not recorded, retried next boot): %v", name, err)
			rep.Failed = append(rep.Failed, name)
			continue
		}
		rep.Applied = append(rep.Applied, name)
	}
	return rep, nil
}
