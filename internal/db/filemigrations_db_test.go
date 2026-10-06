package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"github.com/deploykit/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests run the schema_migrations ledger runner against a real
// Postgres. Needs TEST_DATABASE_URL (e.g.
// postgres://postgres:pw@localhost:5432/postgres) for a role that may
// CREATE DATABASE: each test works in its own throwaway database, dropped
// afterwards, because migrations create objects in the default schema.

func newTestDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — run against a scratch Postgres to exercise the migration runner")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	name := fmt.Sprintf("migrations_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		admin.Close(ctx)
		t.Fatalf("create database: %v", err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}
	t.Cleanup(func() {
		pool.Close()
		admin.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`) //nolint:errcheck
		admin.Close(context.Background())
	})
	return pool
}

func mustRun(t *testing.T, pool *pgxpool.Pool, fsys fstest.MapFS) MigrationReport {
	t.Helper()
	rep, err := runFileMigrations(context.Background(), pool, fsys)
	if err != nil {
		t.Fatalf("runFileMigrations: %v", err)
	}
	return rep
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// TestFileMigrationsLedger covers the ledger mechanics: each file runs once,
// a failing file is rolled back, left unrecorded and retried, and an edited
// file is reported but not re-run.
func TestFileMigrationsLedger(t *testing.T) {
	pool := newTestDatabase(t)
	fsys := fstest.MapFS{
		"001_create.sql":     {Data: []byte(`CREATE TABLE t (x INT);`)},
		"002_broken.sql":     {Data: []byte(`INSERT INTO t VALUES (99); SELECT * FROM no_such_table;`)},
		"003_insert.sql":     {Data: []byte(`INSERT INTO t VALUES (1);`)}, // not idempotent: must run once
		"ALL_MIGRATIONS.sql": {Data: []byte(`SELECT * FROM never_run;`)},
	}

	rep := mustRun(t, pool, fsys)
	if want := []string{"001_create.sql", "003_insert.sql"}; !reflect.DeepEqual(rep.Applied, want) {
		t.Fatalf("boot 1 applied %v, want %v", rep.Applied, want)
	}
	if want := []string{"002_broken.sql"}; !reflect.DeepEqual(rep.Failed, want) {
		t.Fatalf("boot 1 failed %v, want %v", rep.Failed, want)
	}
	if n := count(t, pool, `SELECT count(*) FROM t WHERE x = 99`); n != 0 {
		t.Fatalf("failed migration's earlier statement was not rolled back")
	}

	for boot := 2; boot <= 3; boot++ {
		rep = mustRun(t, pool, fsys)
		if len(rep.Applied) != 0 || rep.AlreadyApplied != 2 || !reflect.DeepEqual(rep.Failed, []string{"002_broken.sql"}) {
			t.Fatalf("boot %d: %+v, want nothing applied, 2 already applied, 002 retried and failing", boot, rep)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM t`); n != 1 {
		t.Fatalf("t has %d rows after 3 boots, want 1 (003 must run exactly once)", n)
	}

	fsys["002_broken.sql"] = &fstest.MapFile{Data: []byte(`INSERT INTO t VALUES (2);`)}
	fsys["001_create.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE t (x INT); -- edited`)}
	rep = mustRun(t, pool, fsys)
	if !reflect.DeepEqual(rep.Applied, []string{"002_broken.sql"}) || len(rep.Failed) != 0 {
		t.Fatalf("after fixing 002: %+v, want only 002 applied", rep)
	}
	if !reflect.DeepEqual(rep.Changed, []string{"001_create.sql"}) {
		t.Fatalf("edited 001 not reported: %+v", rep)
	}
	if n := count(t, pool, `SELECT count(*) FROM schema_migrations`); n != 3 {
		t.Fatalf("schema_migrations has %d rows, want 3", n)
	}
}

type driverRow struct{ ID, VehicleType, VehicleCategory string }
type serviceTypeRow struct {
	ID, Slug, Category string
	BaseFare           float64
}

func snapshot(t *testing.T, pool *pgxpool.Pool) ([]driverRow, []serviceTypeRow, int) {
	t.Helper()
	ctx := context.Background()
	var drivers []driverRow
	rows, err := pool.Query(ctx, `SELECT id::text, vehicle_type, COALESCE(vehicle_category,'') FROM drivers ORDER BY id`)
	if err != nil {
		t.Fatalf("snapshot drivers: %v", err)
	}
	for rows.Next() {
		var d driverRow
		if err := rows.Scan(&d.ID, &d.VehicleType, &d.VehicleCategory); err != nil {
			t.Fatalf("scan driver: %v", err)
		}
		drivers = append(drivers, d)
	}
	rows.Close()
	var sts []serviceTypeRow
	rows, err = pool.Query(ctx, `SELECT id::text, slug, COALESCE(category,''), base_fare::float8 FROM service_types ORDER BY slug`)
	if err != nil {
		t.Fatalf("snapshot service_types: %v", err)
	}
	for rows.Next() {
		var s serviceTypeRow
		if err := rows.Scan(&s.ID, &s.Slug, &s.Category, &s.BaseFare); err != nil {
			t.Fatalf("scan service_type: %v", err)
		}
		sts = append(sts, s)
	}
	rows.Close()
	return drivers, sts, count(t, pool, `SELECT count(*) FROM driver_documents`)
}

// TestEmbeddedMigrationsRebootSafe runs the real embedded migrations through
// repeated boots, including the first boot of the ledger on a database that
// already has data (what production sees on deploy): drivers with truck and
// parcel vehicle types (which 008 used to remap to cab_4w), service_types
// referenced by a booking (which 003/008 used to DELETE), and a document row
// (which 006 used to DROP) must all come through unchanged.
func TestEmbeddedMigrationsRebootSafe(t *testing.T) {
	pool := newTestDatabase(t)
	ctx := context.Background()
	fsys := embeddedFS(t)

	// Boot 1 on an empty database: the guarded 003/008 blocks must take
	// their old-schema path, and the end state is the current catalogue.
	rep := mustRun(t, pool, fsys)
	t.Logf("boot 1 (empty database): %d applied, failed: %v", len(rep.Applied), rep.Failed)
	if len(rep.Failed) != 0 {
		t.Fatalf("migrations failed on an empty database: %v", rep.Failed)
	}
	for slug, cat := range map[string]string{"truck_city_14ft": "truck", "parcel_2w": "parcel", "cab_4w": "cab"} {
		if n := count(t, pool, `SELECT count(*) FROM service_types WHERE slug=$1 AND category=$2`, slug, cat); n != 1 {
			t.Fatalf("service_types %s/%s missing after boot 1", slug, cat)
		}
	}

	// Production-like data.
	const (
		userTruck  = "00000000-0000-4000-8000-000000000001"
		userParcel = "00000000-0000-4000-8000-000000000002"
		userRider  = "00000000-0000-4000-8000-000000000003"
		truck      = "00000000-0000-4000-8000-0000000000d1"
		parcel     = "00000000-0000-4000-8000-0000000000d2"
		rider      = "00000000-0000-4000-8000-0000000000e1"
	)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO users (id, email, name, password_hash) VALUES
		($1, 'truck@example.com', 'Truck Driver', 'x'), ($2, 'parcel@example.com', 'Parcel Driver', 'x'), ($3, 'rider@example.com', 'Rider', 'x')`,
		userTruck, userParcel, userRider)
	exec(`INSERT INTO drivers (id, user_id, license_number, vehicle_type, vehicle_category, vehicle_number, vehicle_model, vehicle_color)
		VALUES ($1, $2, 'DL-1', 'truck_city_14ft', 'truck', 'DL01AB1234', 'Tata 1109', 'White'),
		       ($3, $4, 'DL-2', 'parcel_2w', 'parcel', 'DL02CD5678', 'Activa', 'Black')`,
		truck, userTruck, parcel, userParcel)
	exec(`INSERT INTO riders (id, user_id) VALUES ($1, $2)`, rider, userRider)
	exec(`INSERT INTO bookings (rider_id, service_type_id, pickup_lat, pickup_lng, pickup_address, drop_lat, drop_lng, drop_address)
		SELECT $1, id, 28.6, 77.2, 'A', 28.5, 77.1, 'B' FROM service_types WHERE slug = 'truck_city_14ft'`, rider)
	exec(`INSERT INTO driver_documents (driver_id, doc_type, file_url, status) VALUES ($1, 'police_clearance', 'https://res.cloudinary.com/x', 'approved')`, truck)
	exec(`UPDATE service_types SET base_fare = 555 WHERE slug = 'truck_city_14ft'`) // an admin fare edit

	wantDrivers, wantSTs, wantDocs := snapshot(t, pool)

	// Boot 2 simulates the first deploy of the ledger to production: no
	// schema_migrations yet, so every file runs again once.
	exec(`DROP TABLE schema_migrations`)
	rep = mustRun(t, pool, fsys)
	t.Logf("boot 2 (ledger bootstrap over existing data): %d applied, baselined %v, failed: %v", len(rep.Applied), rep.Baselined, rep.Failed)
	// 001/002/004 aren't idempotent; they are baselined (recorded without
	// running) because their objects all exist. Any failure here is new.
	if len(rep.Failed) != 0 {
		t.Fatalf("bootstrap failures %v, want none", rep.Failed)
	}
	if !reflect.DeepEqual(rep.Baselined, baselineFiles) {
		t.Fatalf("bootstrap baselined %v, want %v", rep.Baselined, baselineFiles)
	}

	for boot := 3; boot <= 5; boot++ {
		rep = mustRun(t, pool, fsys)
		if len(rep.Applied) != 0 || len(rep.Baselined) != 0 || len(rep.Failed) != 0 {
			t.Fatalf("boot %d: applied %v, baselined %v, failed %v — want nothing", boot, rep.Applied, rep.Baselined, rep.Failed)
		}
	}

	gotDrivers, gotSTs, gotDocs := snapshot(t, pool)
	if !reflect.DeepEqual(gotDrivers, wantDrivers) {
		t.Fatalf("drivers changed across reboots:\n got  %+v\n want %+v", gotDrivers, wantDrivers)
	}
	if !reflect.DeepEqual(gotSTs, wantSTs) {
		t.Fatalf("service_types changed across reboots:\n got  %+v\n want %+v", gotSTs, wantSTs)
	}
	if gotDocs != wantDocs {
		t.Fatalf("driver_documents rows: got %d, want %d", gotDocs, wantDocs)
	}
	if n := count(t, pool, `SELECT count(*) FROM bookings`); n != 1 {
		t.Fatalf("bookings: got %d, want 1", n)
	}

	// 005 used to swap in a doc_type CHECK without police_clearance; it only
	// failed while a PCC row existed. Bootstrap again with no PCC rows: PCC
	// uploads must still be accepted and the vehicle_type CHECK still there.
	exec(`DELETE FROM driver_documents WHERE doc_type = 'police_clearance'`)
	exec(`DROP TABLE schema_migrations`)
	mustRun(t, pool, fsys)
	mustRun(t, pool, fsys)
	exec(`INSERT INTO driver_documents (driver_id, doc_type, file_url) VALUES ($1, 'police_clearance', 'https://res.cloudinary.com/y')`, parcel)
	if n := count(t, pool, `SELECT count(*) FROM pg_constraint WHERE conname = 'drivers_vehicle_type_check' AND pg_get_constraintdef(oid) LIKE '%parcel_2w%'`); n != 1 {
		t.Fatalf("drivers_vehicle_type_check (with parcel_2w) missing after a bootstrap with no PCC rows")
	}
	if gotDrivers, _, _ := snapshot(t, pool); !reflect.DeepEqual(gotDrivers, wantDrivers) {
		t.Fatalf("drivers changed:\n got  %+v\n want %+v", gotDrivers, wantDrivers)
	}
}

// baselineFiles are baselineMigrations' keys in run order.
var baselineFiles = []string{"001_init.sql", "002_gogoo.sql", "004_driver_documents.sql"}

// embeddedFS copies the real embedded migrations into a MapFS.
func embeddedFS(t *testing.T) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := migrations.FS.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		fsys[e.Name()] = &fstest.MapFile{Data: b}
	}
	return fsys
}

// preLedgerDatabase fully migrates a fresh database, then drops the ledger:
// the state production was in when the ledger first shipped — every object
// present, nothing recorded.
func preLedgerDatabase(t *testing.T) (*pgxpool.Pool, fstest.MapFS) {
	t.Helper()
	pool := newTestDatabase(t)
	fsys := embeddedFS(t)
	if rep := mustRun(t, pool, fsys); len(rep.Failed) != 0 {
		t.Fatalf("migrations failed on an empty database: %v", rep.Failed)
	}
	if _, err := pool.Exec(context.Background(), `DROP TABLE schema_migrations`); err != nil {
		t.Fatalf("drop schema_migrations: %v", err)
	}
	return pool, fsys
}

// TestBaselinePreLedgerDatabase: with every object present, 001/002/004 are
// recorded with their real checksums without running — so 002's legacy
// service_types seed never reaches the catalogue and existing rows are
// untouched.
func TestBaselinePreLedgerDatabase(t *testing.T) {
	pool, fsys := preLedgerDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO service_types (name, slug, vehicle_type, base_fare, per_km_rate, per_min_rate)
		VALUES ('Sentinel', 'baseline_sentinel', 'cab', 1, 1, 1)`); err != nil {
		t.Fatalf("insert sentinel: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE service_types SET base_fare = 777 WHERE slug = 'cab_4w'`); err != nil {
		t.Fatalf("edit fare: %v", err)
	}
	wantDrivers, wantSTs, wantDocs := snapshot(t, pool)

	rep := mustRun(t, pool, fsys)
	if !reflect.DeepEqual(rep.Baselined, baselineFiles) {
		t.Fatalf("baselined %v, want %v", rep.Baselined, baselineFiles)
	}
	if len(rep.Failed) != 0 {
		t.Fatalf("failed %v, want none", rep.Failed)
	}
	for _, name := range baselineFiles {
		for _, applied := range rep.Applied {
			if applied == name {
				t.Fatalf("%s was run, want it only recorded", name)
			}
		}
	}

	if n := count(t, pool, `SELECT count(*) FROM service_types WHERE slug IN ('bike','auto','mini','sedan','suv','xl')`); n != 0 {
		t.Fatalf("002's legacy seed ran: %d legacy service_types rows", n)
	}
	gotDrivers, gotSTs, gotDocs := snapshot(t, pool)
	if !reflect.DeepEqual(gotSTs, wantSTs) {
		t.Fatalf("service_types changed:\n got  %+v\n want %+v", gotSTs, wantSTs)
	}
	if !reflect.DeepEqual(gotDrivers, wantDrivers) || gotDocs != wantDocs {
		t.Fatalf("drivers/driver_documents changed")
	}

	for _, name := range baselineFiles {
		sum := sha256.Sum256(fsys[name].Data)
		if n := count(t, pool, `SELECT count(*) FROM schema_migrations WHERE filename = $1 AND checksum = $2`,
			name, hex.EncodeToString(sum[:])); n != 1 {
			t.Fatalf("%s not recorded with its checksum", name)
		}
	}
}

// TestBaselineFreshDatabase: on an empty database nothing exists yet, so
// 001/002/004 are run and recorded normally, never baselined.
func TestBaselineFreshDatabase(t *testing.T) {
	pool := newTestDatabase(t)
	rep := mustRun(t, pool, embeddedFS(t))
	if len(rep.Baselined) != 0 {
		t.Fatalf("baselined %v on an empty database, want none", rep.Baselined)
	}
	if len(rep.Failed) != 0 {
		t.Fatalf("failed %v, want none", rep.Failed)
	}
	applied := map[string]bool{}
	for _, name := range rep.Applied {
		applied[name] = true
	}
	for _, name := range baselineFiles {
		if !applied[name] {
			t.Fatalf("%s not applied on an empty database (applied: %v)", name, rep.Applied)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM schema_migrations WHERE filename = ANY($1)`, baselineFiles); n != 3 {
		t.Fatalf("%d of the baseline files recorded, want 3", n)
	}
}

// TestBaselineSkippedWhenObjectMissing: one missing object means no baseline
// record; the file runs, fails and stays unrecorded (retried next boot),
// while the files whose objects are complete are still baselined.
func TestBaselineSkippedWhenObjectMissing(t *testing.T) {
	pool, fsys := preLedgerDatabase(t)
	if _, err := pool.Exec(context.Background(), `DROP INDEX idx_project_members_user_id`); err != nil {
		t.Fatalf("drop index: %v", err)
	}

	for boot := 1; boot <= 2; boot++ {
		rep := mustRun(t, pool, fsys)
		if !reflect.DeepEqual(rep.Failed, []string{"001_init.sql"}) {
			t.Fatalf("boot %d failed %v, want only 001_init.sql", boot, rep.Failed)
		}
		if n := count(t, pool, `SELECT count(*) FROM schema_migrations WHERE filename = '001_init.sql'`); n != 0 {
			t.Fatalf("boot %d: 001_init.sql recorded despite a missing object", boot)
		}
		if boot == 1 && !reflect.DeepEqual(rep.Baselined, []string{"002_gogoo.sql", "004_driver_documents.sql"}) {
			t.Fatalf("boot 1 baselined %v, want 002 and 004", rep.Baselined)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM pg_class WHERE relname = 'idx_project_members_user_id'`); n != 0 {
		t.Fatalf("001 partially applied: the dropped index came back despite the failure")
	}
}

// TestBaselineSecondBoot: once baselined, the next boot finds every file
// already applied and does nothing.
func TestBaselineSecondBoot(t *testing.T) {
	pool, fsys := preLedgerDatabase(t)
	if rep := mustRun(t, pool, fsys); !reflect.DeepEqual(rep.Baselined, baselineFiles) || len(rep.Failed) != 0 {
		t.Fatalf("first boot: baselined %v failed %v", rep.Baselined, rep.Failed)
	}

	numbered := 0
	for name := range fsys {
		if numberedMigrationRe.MatchString(name) {
			numbered++
		}
	}
	rep := mustRun(t, pool, fsys)
	if len(rep.Applied) != 0 || len(rep.Baselined) != 0 || len(rep.Failed) != 0 || len(rep.Changed) != 0 {
		t.Fatalf("second boot: %+v, want nothing applied, baselined, failed or changed", rep)
	}
	if rep.AlreadyApplied != numbered {
		t.Fatalf("second boot: %d already applied, want all %d", rep.AlreadyApplied, numbered)
	}
}

// TestBaselineBroadcastNotificationsTable: production's notifications is the
// broadcast table (title/body/target_* columns, no user_id), not 001's
// per-user one, so 001's idx_notifications_user_id can't exist there. 001
// must still be baselined rather than retried and failing on every boot.
func TestBaselineBroadcastNotificationsTable(t *testing.T) {
	pool, fsys := preLedgerDatabase(t)
	ctx := context.Background()
	// Production's exact column set.
	for _, sql := range []string{
		`DROP TABLE notifications CASCADE`,
		`CREATE TABLE notifications (
			id UUID PRIMARY KEY, title TEXT NOT NULL DEFAULT '', body TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT 'general', target_audience TEXT NOT NULL DEFAULT 'all',
			coupon_code TEXT, link_url TEXT, is_active BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), target_user_id UUID, target_type TEXT,
			target_id UUID, target_category TEXT, target_user_ids UUID[], target_hospital_id UUID,
			target_hospital_ids UUID[])`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM pg_class WHERE relname = 'idx_notifications_user_id'`); n != 0 {
		t.Fatalf("precondition: idx_notifications_user_id still exists")
	}

	rep := mustRun(t, pool, fsys)
	if !reflect.DeepEqual(rep.Baselined, baselineFiles) || len(rep.Failed) != 0 {
		t.Fatalf("baselined %v failed %v, want all three baselined and none failed", rep.Baselined, rep.Failed)
	}
	if n := count(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'notifications' AND column_name = 'user_id'`); n != 0 {
		t.Fatalf("001 ran: notifications gained user_id")
	}
	if rep = mustRun(t, pool, fsys); len(rep.Applied)+len(rep.Baselined)+len(rep.Failed) != 0 {
		t.Fatalf("second boot: %+v, want nothing", rep)
	}
}
