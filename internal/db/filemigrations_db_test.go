package db

import (
	"context"
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
	t.Logf("boot 2 (ledger bootstrap over existing data): %d applied, failed: %v", len(rep.Applied), rep.Failed)
	bootstrapFailed := rep.Failed
	// Not idempotent, but they fail on their first conflicting statement and
	// roll back whole, so they stay unrecorded rather than being marked
	// applied. Any other failure here is new.
	if want := []string{"001_init.sql", "002_gogoo.sql", "004_driver_documents.sql"}; !reflect.DeepEqual(bootstrapFailed, want) {
		t.Fatalf("bootstrap failures %v, want exactly %v", bootstrapFailed, want)
	}

	for boot := 3; boot <= 5; boot++ {
		rep = mustRun(t, pool, fsys)
		if len(rep.Applied) != 0 || !reflect.DeepEqual(rep.Failed, bootstrapFailed) {
			t.Fatalf("boot %d: applied %v, failed %v — want nothing new, same failures as bootstrap %v", boot, rep.Applied, rep.Failed, bootstrapFailed)
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
