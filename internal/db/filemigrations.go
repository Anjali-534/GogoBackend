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
	Baselined      []string // recorded without running: everything they create already exists
	AlreadyApplied int      // already recorded, not run
	Failed         []string // errored and rolled back; not recorded, retried next boot
	Changed        []string // recorded, but the file's content changed since — not re-run
}

// baselineSpec lists every object a migration file creates.
type baselineSpec struct {
	relations  []string    // tables and indexes, checked with to_regclass
	extensions []string    // checked in pg_extension
	columns    [][2]string // {table, column}, checked in information_schema.columns
}

// baselineMigrations are the founding files that predate the ledger and are
// not idempotent (plain CREATE TABLE / CREATE INDEX). On a database that
// already had them applied before the ledger existed they fail on their
// first statement and roll back on every boot, so they never get recorded.
// They can't be made idempotent instead: 002 also seeds six legacy
// service_types (bike, auto, mini, ...) that 003/008 replaced, and letting
// its remaining statements finally run would put those back in the live
// catalogue.
//
// So an unrecorded file listed here is recorded WITHOUT being run when every
// object it creates already exists — its effect is already in the database.
// If even one is missing it runs normally (a fresh database, where it
// succeeds; or a damaged one, where it fails loudly and stays unrecorded).
// 002's seed rows are deliberately not checked: 003/008 replace them.
var baselineMigrations = map[string]baselineSpec{
	"001_init.sql": {
		relations: []string{
			"users", "oauth_tokens", "projects", "project_members", "cloud_credentials", "clusters",
			"env_groups", "env_group_vars", "env_group_versions", "apps", "app_env_vars", "app_env_groups",
			"builds", "deployments", "deployment_events", "preview_environments", "managed_databases",
			"billing_usage", "notifications", "webhooks",
			"idx_apps_cluster_id", "idx_apps_project_id", "idx_builds_app_id", "idx_deployments_app_id",
			"idx_deployment_events_app_id", "idx_clusters_project_id",
			"idx_billing_usage_project_id", "idx_project_members_user_id",
			// idx_notifications_user_id (001's index on notifications.user_id)
			// is deliberately not checked. Production's notifications is not
			// 001's per-user table but the broadcast table MigrateNotifications
			// creates (title/body/target_* columns, no user_id), so the index
			// can't exist there and checking it would block the baseline
			// forever. Nothing reads notifications.user_id or relies on the
			// index — per-user read state lives in notification_reads — so the
			// table's existence is the only check that matters.
		},
		extensions: []string{"uuid-ossp", "pgcrypto"},
	},
	"002_gogoo.sql": {
		relations: []string{
			"drivers", "riders", "service_types", "bookings", "payments", "driver_earnings",
			"driver_documents", "location_history", "device_tokens", "promo_codes",
			"idx_bookings_rider_id", "idx_bookings_driver_id", "idx_bookings_status", "idx_bookings_created_at",
			"idx_drivers_is_online", "idx_payments_booking_id", "idx_driver_earnings_driver_id",
			"idx_location_history_driver_id",
		},
	},
	"004_driver_documents.sql": {
		// The driver_documents table itself isn't listed: 002 creates it too,
		// so it exists before 004 on a fresh database (which would wrongly
		// look like a partial state). Its two indexes can't exist without it.
		relations: []string{"idx_driver_documents_driver_id", "idx_driver_documents_status"},
		columns: [][2]string{
			{"drivers", "docs_submitted"}, {"drivers", "docs_verified_at"}, {"drivers", "rejection_reason"},
		},
	},
}

// missingBaselineObjects returns the objects in spec that don't exist.
func missingBaselineObjects(ctx context.Context, conn *pgxpool.Conn, spec baselineSpec) ([]string, error) {
	var missing []string
	collect := func(sql string, args ...any) error {
		var names []string
		if err := conn.QueryRow(ctx, sql, args...).Scan(&names); err != nil {
			return err
		}
		missing = append(missing, names...)
		return nil
	}
	if err := collect(
		`SELECT COALESCE(array_agg(n ORDER BY n), '{}') FROM unnest($1::text[]) AS n WHERE to_regclass(n) IS NULL`,
		spec.relations,
	); err != nil {
		return nil, fmt.Errorf("check relations: %w", err)
	}
	if err := collect(
		`SELECT COALESCE(array_agg(e ORDER BY e), '{}') FROM unnest($1::text[]) AS e
		 WHERE NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = e)`,
		spec.extensions,
	); err != nil {
		return nil, fmt.Errorf("check extensions: %w", err)
	}
	tables := make([]string, len(spec.columns))
	cols := make([]string, len(spec.columns))
	for i, tc := range spec.columns {
		tables[i], cols[i] = tc[0], tc[1]
	}
	if err := collect(
		`SELECT COALESCE(array_agg(c.t || '.' || c.col ORDER BY c.t, c.col), '{}')
		 FROM unnest($1::text[], $2::text[]) AS c(t, col)
		 WHERE NOT EXISTS (SELECT 1 FROM information_schema.columns
		                   WHERE table_schema = current_schema() AND table_name = c.t AND column_name = c.col)`,
		tables, cols,
	); err != nil {
		return nil, fmt.Errorf("check columns: %w", err)
	}
	return missing, nil
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
	log.Printf("migrations: %d applied this boot, %d baselined, %d already applied, %d failed",
		len(rep.Applied), len(rep.Baselined), rep.AlreadyApplied, len(rep.Failed))
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

		if spec, ok := baselineMigrations[name]; ok {
			missing, err := missingBaselineObjects(ctx, conn, spec)
			if err != nil {
				return rep, fmt.Errorf("baseline check %s: %w", name, err)
			}
			if len(missing) == 0 {
				if _, err := conn.Exec(ctx, `INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)`, name, checksum); err != nil {
					return rep, fmt.Errorf("record baseline %s: %w", name, err)
				}
				log.Printf("migrations: baselined %s — everything it creates already exists; recorded without running", name)
				rep.Baselined = append(rep.Baselined, name)
				continue
			}
			// Everything missing is just a fresh database; only a partial
			// state is worth flagging.
			if len(missing) < len(spec.relations)+len(spec.extensions)+len(spec.columns) {
				log.Printf("⚠ migrations: not baselining %s, missing %v — running it normally", name, missing)
			}
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
