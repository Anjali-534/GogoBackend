package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deploykit/backend/migrations"
)

// TestTrackerDriverStopFlow covers the drive page's reached / delivered
// path against a real Postgres: a multi-stop trip token resolving to its
// current active stop (previously the event and signature endpoints only
// looked in tracker_orders and 404'd for trip links), 'reached' and
// 'delivery_claimed' each being recorded once per stop, the
// reached / delivery-claimed progress a reloaded drive page resumes from,
// the audit columns and order-level flags from migration 069, and that
// migration's reached_at / delivery_claimed_at backfill.
//
// Needs TEST_DATABASE_URL, same as TestEnsureRiderProfile. Runs inside a
// throwaway schema that is dropped afterwards.
func TestTrackerDriverStopFlow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — run against a scratch Postgres to exercise the tracker driver stop flow")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	schema := fmt.Sprintf("tracker_arrival_test_%d", time.Now().UnixNano())
	mustExec(t, conn, `CREATE SCHEMA `+schema)
	defer conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	mustExec(t, conn, `SET search_path TO `+schema)

	// Pre-069 shape of the columns these code paths touch.
	mustExec(t, conn, `CREATE TABLE tracker_trips (
		id UUID PRIMARY KEY, overall_status TEXT NOT NULL, driver_tracking_token TEXT UNIQUE)`)
	mustExec(t, conn, `CREATE TABLE tracker_orders (
		id UUID PRIMARY KEY, company_id UUID NOT NULL, status TEXT NOT NULL,
		dispatch_to_lat DOUBLE PRECISION, dispatch_to_lng DOUBLE PRECISION,
		trip_id UUID REFERENCES tracker_trips(id), stop_sequence INT DEFAULT 1,
		driver_tracking_token TEXT UNIQUE, signature_url TEXT, updated_at TIMESTAMPTZ DEFAULT NOW())`)
	mustExec(t, conn, `CREATE TABLE tracker_order_events (
		id UUID PRIMARY KEY, order_id UUID NOT NULL REFERENCES tracker_orders(id) ON DELETE CASCADE,
		status TEXT NOT NULL, note TEXT, location TEXT, reported_by TEXT NOT NULL DEFAULT 'company',
		event_kind TEXT, created_at TIMESTAMPTZ DEFAULT NOW())`)

	const (
		companyID = "00000000-0000-4000-8000-00000000c001"
		tripID    = "00000000-0000-4000-8000-00000000a001"
		stop1     = "00000000-0000-4000-8000-00000000b001"
		stop2     = "00000000-0000-4000-8000-00000000b002"
		single    = "00000000-0000-4000-8000-00000000b003"
		legacy    = "00000000-0000-4000-8000-00000000b004"
		tripTok   = "trip-token"
		orderTok  = "order-token"
	)
	mustExec(t, conn, `INSERT INTO tracker_trips VALUES ($1, 'in_transit', $2)`, tripID, tripTok)
	// Stops inserted out of order to prove ORDER BY stop_sequence picks them.
	mustExec(t, conn, `INSERT INTO tracker_orders (id, company_id, status, dispatch_to_lat, dispatch_to_lng, trip_id, stop_sequence)
		VALUES ($1,$2,'in_transit',28.70,77.10,$3,2)`, stop2, companyID, tripID)
	mustExec(t, conn, `INSERT INTO tracker_orders (id, company_id, status, dispatch_to_lat, dispatch_to_lng, trip_id, stop_sequence)
		VALUES ($1,$2,'in_transit',28.6315,77.2167,$3,1)`, stop1, companyID, tripID)
	mustExec(t, conn, `INSERT INTO tracker_orders (id, company_id, status, dispatch_to_lat, dispatch_to_lng, driver_tracking_token)
		VALUES ($1,$2,'in_transit',28.5,77.3,$3)`, single, companyID, orderTok)
	// An order that already had 'reached' + 'delivery_claimed' events and a
	// signature before migration 069.
	mustExec(t, conn, `INSERT INTO tracker_orders (id, company_id, status, signature_url) VALUES ($1,$2,'in_transit','https://example.com/sig.png')`, legacy, companyID)
	mustExec(t, conn, `INSERT INTO tracker_order_events (id, order_id, status, reported_by, event_kind)
		VALUES (gen_random_uuid(), $1, 'in_transit', 'driver', 'reached'),
		       (gen_random_uuid(), $1, 'in_transit', 'driver', 'delivery_claimed')`, legacy)

	sql069, err := migrations.FS.ReadFile("069_tracker_arrival_audit.sql")
	if err != nil {
		t.Fatalf("read migration 069: %v", err)
	}
	mustExec(t, conn, string(sql069))
	mustExec(t, conn, string(sql069)) // must be re-runnable

	if p, err := loadTrackerStopProgress(ctx, conn, legacy); err != nil || !p.Reached || !p.DeliveryClaimed {
		t.Fatalf("legacy order not backfilled: progress=%+v err=%v", p, err)
	}

	resolve := func(token string) (trackerDriverStop, string) {
		t.Helper()
		stop, msg, err := resolveTrackerDriverStop(ctx, conn, token)
		if err != nil {
			t.Fatalf("resolve %q: %v", token, err)
		}
		return stop, msg
	}
	record := func(stop trackerDriverStop, req trackerDriverEventRequest) bool {
		t.Helper()
		a, rej := assessTrackerDriverArrival(req, stop, testArrivalCfg)
		if rej != nil {
			t.Fatalf("assess %+v: rejected %+v", req, rej)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		already, err := recordTrackerDriverEvent(ctx, tx, stop, req, a)
		if err != nil {
			t.Fatalf("record %+v: %v", req, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return already
	}
	progress := func(orderID string) trackerStopProgress {
		t.Helper()
		p, err := loadTrackerStopProgress(ctx, conn, orderID)
		if err != nil {
			t.Fatalf("progress(%s): %v", orderID, err)
		}
		return p
	}
	reached := func(orderID string) bool { t.Helper(); return progress(orderID).Reached }
	countEvents := func(orderID, kind string) int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM tracker_order_events WHERE order_id=$1 AND event_kind=$2`, orderID, kind).Scan(&n); err != nil {
			t.Fatalf("count events: %v", err)
		}
		return n
	}

	// 1. Trip token resolves to stop 1 (lowest stop_sequence).
	s, msg := resolve(tripTok)
	if msg != "" || s.OrderID != stop1 || s.TripID != tripID || s.CompanyID != companyID {
		t.Fatalf("trip token resolved to %+v msg=%q, want stop1", s, msg)
	}

	// 2. 'reached' inside the radius is recorded once; a repeat is a no-op.
	inside := trackerDriverEventRequest{Kind: "reached", Lat: f64(28.6315 + 200/111195.0), Lng: f64(77.2167), AccuracyM: f64(15)}
	if record(s, inside) {
		t.Fatal("first reached reported already=true")
	}
	if !reached(stop1) || reached(stop2) {
		t.Fatalf("reached flags wrong: stop1=%v stop2=%v", reached(stop1), reached(stop2))
	}
	if !record(s, inside) {
		t.Fatal("second reached should report already=true")
	}
	if n := countEvents(stop1, "reached"); n != 1 {
		t.Fatalf("stop1 has %d reached events, want 1", n)
	}
	var lat, acc, dist *float64
	var outside, noLoc bool
	if err := conn.QueryRow(ctx, `SELECT lat, accuracy_m, distance_m, outside_radius, no_location
		FROM tracker_order_events WHERE order_id=$1 AND event_kind='reached'`, stop1).Scan(&lat, &acc, &dist, &outside, &noLoc); err != nil {
		t.Fatalf("read reached event: %v", err)
	}
	if lat == nil || acc == nil || *acc != 15 || dist == nil || *dist < 195 || *dist > 205 || outside || noLoc {
		t.Fatalf("reached event audit wrong: lat=%v acc=%v dist=%v outside=%v noLoc=%v", lat, acc, dist, outside, noLoc)
	}

	// 3. delivery_claimed with no coordinates: accepted, order flagged
	// no_location. Without a signature yet it doesn't count as claimed for
	// the drive page (the driver still has to sign).
	if record(s, trackerDriverEventRequest{Kind: "delivery_claimed"}) {
		t.Fatal("first delivery_claimed reported already=true")
	}
	if p := progress(stop1); !p.Reached || p.DeliveryClaimed {
		t.Fatalf("after claim without signature: %+v, want reached only", p)
	}

	// 3b. A second delivery_claimed for the same stop does nothing — no
	// event row, and its (far, outside-radius) position doesn't touch the
	// order's flags.
	if !record(s, trackerDriverEventRequest{Kind: "delivery_claimed", Lat: f64(28.6315 + 2000/111195.0), Lng: f64(77.2167)}) {
		t.Fatal("second delivery_claimed should report already=true")
	}
	if n := countEvents(stop1, "delivery_claimed"); n != 1 {
		t.Fatalf("stop1 has %d delivery_claimed events, want 1", n)
	}

	// 3c. Signature on file → the drive page resumes in "Delivery claimed".
	mustExec(t, conn, `UPDATE tracker_orders SET signature_url='https://example.com/s1.png' WHERE id=$1`, stop1)
	if p := progress(stop1); !p.Reached || !p.DeliveryClaimed {
		t.Fatalf("after claim + signature: %+v, want both", p)
	}

	var orderOutside, orderNoLoc bool
	if err := conn.QueryRow(ctx, `SELECT arrived_outside_radius, arrival_no_location FROM tracker_orders WHERE id=$1`, stop1).Scan(&orderOutside, &orderNoLoc); err != nil {
		t.Fatalf("read stop1 flags: %v", err)
	}
	if orderOutside || !orderNoLoc {
		t.Fatalf("stop1 flags: outside=%v noLocation=%v, want false/true", orderOutside, orderNoLoc)
	}

	// 4. Stop 1 delivered → the same trip token now resolves to stop 2,
	// which has not been reached yet.
	mustExec(t, conn, `UPDATE tracker_orders SET status='delivered' WHERE id=$1`, stop1)
	s, msg = resolve(tripTok)
	if msg != "" || s.OrderID != stop2 {
		t.Fatalf("after stop1 delivered, trip token resolved to %+v msg=%q, want stop2", s, msg)
	}
	if p := progress(stop2); p.Reached || p.DeliveryClaimed {
		t.Fatalf("stop2 progress = %+v, want nothing yet", p)
	}

	// 5. Stop 2 via the outside-radius override: 700 m out, flagged.
	far := trackerDriverEventRequest{Kind: "reached", Lat: f64(28.70 + 700/111195.0), Lng: f64(77.10), AccuracyM: f64(20)}
	if _, rej := assessTrackerDriverArrival(far, s, testArrivalCfg); rej == nil {
		t.Fatal("700 m reached without override should be rejected")
	}
	far.Override, far.Reason = true, "Drop pin is on the wrong side of the campus"
	record(s, far)
	var distM *float64
	var reason *string
	if err := conn.QueryRow(ctx, `SELECT arrived_outside_radius, arrival_distance_m, arrival_override_reason FROM tracker_orders WHERE id=$1`, stop2).
		Scan(&orderOutside, &distM, &reason); err != nil {
		t.Fatalf("read stop2 flags: %v", err)
	}
	if !orderOutside || distM == nil || *distM < 690 || *distM > 710 || reason == nil || *reason != far.Reason {
		t.Fatalf("stop2 audit: outside=%v dist=%v reason=%v", orderOutside, distM, reason)
	}
	if !reached(stop2) {
		t.Fatal("stop2 should be reached after override")
	}

	// 6. All stops delivered → trip token has nothing left to deliver.
	mustExec(t, conn, `UPDATE tracker_orders SET status='delivered' WHERE id=$1`, stop2)
	if _, msg = resolve(tripTok); msg == "" {
		t.Fatal("trip with no remaining stops should return a terminal message")
	}
	mustExec(t, conn, `UPDATE tracker_trips SET overall_status='completed' WHERE id=$1`, tripID)
	if _, msg = resolve(tripTok); msg == "" {
		t.Fatal("completed trip should return a terminal message")
	}

	// 7. Single-order token still resolves the old way.
	s, msg = resolve(orderTok)
	if msg != "" || s.OrderID != single || s.TripID != "" {
		t.Fatalf("order token resolved to %+v msg=%q", s, msg)
	}

	// 8. Unknown token.
	if _, _, err := resolveTrackerDriverStop(ctx, conn, "nope"); !errors.Is(err, errTrackerLinkNotFound) {
		t.Fatalf("unknown token err = %v, want errTrackerLinkNotFound", err)
	}
}
