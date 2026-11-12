package handlers

import (
	"context"
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

	// 'reached' with no order_id: the active-stop resolution, which the
	// claim/signature path no longer uses (see TestTrackerDriverNamedStop).
	resolve := func(token string) (trackerDriverStop, *trackerDriverError) {
		t.Helper()
		stop, e := resolveTrackerDriverTarget(ctx, conn, token, "", trackerActionReached)
		if e != nil && e.Status >= 500 {
			t.Fatalf("resolve %q: %v", token, e)
		}
		return stop, e
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
	s, e := resolve(tripTok)
	if e != nil || s.OrderID != stop1 || s.TripID != tripID || s.CompanyID != companyID {
		t.Fatalf("trip token resolved to %+v err=%v, want stop1", s, e)
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
	s, e = resolve(tripTok)
	if e != nil || s.OrderID != stop2 {
		t.Fatalf("after stop1 delivered, trip token resolved to %+v err=%v, want stop2", s, e)
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
	if _, e = resolve(tripTok); e == nil {
		t.Fatal("trip with no remaining stops should return a terminal message")
	}
	mustExec(t, conn, `UPDATE tracker_trips SET overall_status='completed' WHERE id=$1`, tripID)
	if _, e = resolve(tripTok); e == nil {
		t.Fatal("completed trip should return a terminal message")
	}

	// 7. Single-order token still resolves the old way.
	s, e = resolve(orderTok)
	if e != nil || s.OrderID != single || s.TripID != "" {
		t.Fatalf("order token resolved to %+v err=%v", s, e)
	}

	// 8. Unknown token.
	if _, e := resolve("nope"); e == nil || e.Status != 404 {
		t.Fatalf("unknown token err = %v, want 404", e)
	}
}

// TestTrackerDriverNamedStop covers the claim/signature path naming its
// stop (order_id) instead of re-resolving to the trip's current active
// stop: a consignee confirming receipt marks stop 1 delivered and advances
// the trip, and the driver's claim + signature, sent afterwards, must
// still land on stop 1, not stop 2. Also: delivered-but-unsigned orders
// still take a claim + signature, cancelled ones never do.
//
// Needs TEST_DATABASE_URL, same as TestTrackerDriverStopFlow.
func TestTrackerDriverNamedStop(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — run against a scratch Postgres to exercise the tracker signature path")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	schema := fmt.Sprintf("tracker_named_stop_test_%d", time.Now().UnixNano())
	mustExec(t, conn, `CREATE SCHEMA `+schema)
	defer conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	mustExec(t, conn, `SET search_path TO `+schema)
	mustExec(t, conn, `CREATE TABLE tracker_trips (
		id UUID PRIMARY KEY, overall_status TEXT NOT NULL, driver_tracking_token TEXT UNIQUE)`)
	mustExec(t, conn, `CREATE TABLE tracker_orders (
		id UUID PRIMARY KEY, company_id UUID NOT NULL, status TEXT NOT NULL,
		dispatch_to_lat DOUBLE PRECISION, dispatch_to_lng DOUBLE PRECISION,
		trip_id UUID REFERENCES tracker_trips(id), stop_sequence INT DEFAULT 1,
		driver_tracking_token TEXT UNIQUE, signature_url TEXT, received_confirmed_at TIMESTAMPTZ,
		updated_at TIMESTAMPTZ DEFAULT NOW())`)
	mustExec(t, conn, `CREATE TABLE tracker_order_events (
		id UUID PRIMARY KEY, order_id UUID NOT NULL REFERENCES tracker_orders(id) ON DELETE CASCADE,
		status TEXT NOT NULL, note TEXT, location TEXT, reported_by TEXT NOT NULL DEFAULT 'company',
		event_kind TEXT, created_at TIMESTAMPTZ DEFAULT NOW())`)
	sql069, err := migrations.FS.ReadFile("069_tracker_arrival_audit.sql")
	if err != nil {
		t.Fatalf("read migration 069: %v", err)
	}
	mustExec(t, conn, string(sql069))

	const (
		companyID = "00000000-0000-4000-8000-00000000c001"
		trip      = "00000000-0000-4000-8000-00000000a001"
		otherTrip = "00000000-0000-4000-8000-00000000a002"
		stop1     = "00000000-0000-4000-8000-00000000b001"
		stop2     = "00000000-0000-4000-8000-00000000b002"
		otherStop = "00000000-0000-4000-8000-00000000b003"
		single    = "00000000-0000-4000-8000-00000000b004"
		tripTok   = "trip-token"
		otherTok  = "other-trip-token"
		orderTok  = "order-token"
	)
	mustExec(t, conn, `INSERT INTO tracker_trips VALUES ($1,'in_transit',$2), ($3,'in_transit',$4)`, trip, tripTok, otherTrip, otherTok)
	mustExec(t, conn, `INSERT INTO tracker_orders (id, company_id, status, dispatch_to_lat, dispatch_to_lng, trip_id, stop_sequence) VALUES
		($1,$4,'in_transit',28.6315,77.2167,$5,1),
		($2,$4,'in_transit',28.7000,77.1000,$5,2),
		($3,$4,'in_transit',28.5000,77.3000,$6,1)`, stop1, stop2, otherStop, companyID, trip, otherTrip)
	mustExec(t, conn, `INSERT INTO tracker_orders (id, company_id, status, dispatch_to_lat, dispatch_to_lng, driver_tracking_token)
		VALUES ($1,$2,'in_transit',28.6,77.2,$3)`, single, companyID, orderTok)

	target := func(token, orderID string, action trackerDriverAction) (trackerDriverStop, *trackerDriverError) {
		t.Helper()
		s, e := resolveTrackerDriverTarget(ctx, conn, token, orderID, action)
		if e != nil && e.Status >= 500 {
			t.Fatalf("resolve(%s, %s): %v", token, orderID, e)
		}
		return s, e
	}
	wantErr := func(label string, e *trackerDriverError, status int, code string) {
		t.Helper()
		if e == nil || e.Status != status || (code != "" && e.Code != code) {
			t.Fatalf("%s: err = %+v, want %d %s", label, e, status, code)
		}
	}
	// What the drive page does on Delivered: claim, then upload, both
	// naming the stop it is showing.
	claimAndSign := func(token, orderID, url string) trackerDriverStop {
		t.Helper()
		s, e := target(token, orderID, trackerActionDeliver)
		if e != nil {
			t.Fatalf("claim target(%s, %s): %v", token, orderID, e)
		}
		req := trackerDriverEventRequest{Kind: "delivery_claimed", OrderID: orderID, Lat: s.ToLat, Lng: s.ToLng}
		a, rej := assessTrackerDriverArrival(req, s, testArrivalCfg)
		if rej != nil {
			t.Fatalf("claim assess: %+v", rej)
		}
		if _, err := recordTrackerDriverEvent(ctx, conn, s, req, a); err != nil {
			t.Fatalf("claim record: %v", err)
		}
		s2, e := target(token, orderID, trackerActionDeliver)
		if e != nil {
			t.Fatalf("signature target(%s, %s): %v", token, orderID, e)
		}
		if ok, err := saveTrackerDriverSignature(ctx, conn, s2.OrderID, url); err != nil || !ok {
			t.Fatalf("save signature on %s: ok=%v err=%v", s2.OrderID, ok, err)
		}
		return s2
	}
	type row struct {
		status    string
		signature *string
		claimed   bool
	}
	read := func(id string) row {
		t.Helper()
		var r row
		if err := conn.QueryRow(ctx, `SELECT status, signature_url, delivery_claimed_at IS NOT NULL FROM tracker_orders WHERE id=$1`, id).
			Scan(&r.status, &r.signature, &r.claimed); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return r
	}

	// 1. The consignee confirms receipt on stop 1, which marks it delivered
	// (what tryAutoCompleteDelivery does); the trip's active stop is now 2.
	mustExec(t, conn, `UPDATE tracker_orders SET status='delivered', received_confirmed_at=NOW() WHERE id=$1`, stop1)
	if s, e := target(tripTok, "", trackerActionReached); e != nil || s.OrderID != stop2 {
		t.Fatalf("precondition: active stop = %+v err=%v, want stop2", s, e)
	}

	// 2. The driver, still on stop 1's page, claims and signs: stop 1.
	if s := claimAndSign(tripTok, stop1, "https://example.com/stop1.png"); s.OrderID != stop1 {
		t.Fatalf("signature resolved to %s, want stop1", s.OrderID)
	}
	if r := read(stop1); r.signature == nil || *r.signature != "https://example.com/stop1.png" || !r.claimed {
		t.Fatalf("stop1 after signing: %+v", r)
	}
	if r := read(stop2); r.signature != nil || r.claimed {
		t.Fatalf("stop2 must be untouched: %+v", r)
	}

	// 3. Delivered + signed accepts nothing more.
	_, e := target(tripTok, stop1, trackerActionDeliver)
	wantErr("claim on signed delivered stop", e, 409, "already_signed")
	if ok, _ := saveTrackerDriverSignature(ctx, conn, stop1, "https://example.com/again.png"); ok {
		t.Fatal("second signature on a signed delivered order was saved")
	}
	_, e = target(tripTok, stop1, trackerActionReached)
	wantErr("reached on delivered stop", e, 400, "already_delivered")

	// 4. Trip token: claim/signature must name the stop, it must be this
	// trip's, and garbage ids are just "not on this link".
	_, e = target(tripTok, "", trackerActionDeliver)
	wantErr("trip claim without order_id", e, 400, "stop_id_required")
	_, e = target(tripTok, otherStop, trackerActionDeliver)
	wantErr("other trip's stop", e, 404, "stop_not_on_link")
	_, e = target(tripTok, "not-a-uuid", trackerActionDeliver)
	wantErr("malformed order_id", e, 404, "stop_not_on_link")

	// 5. Cancelled stop: rejected up front and in the UPDATE.
	mustExec(t, conn, `UPDATE tracker_orders SET status='cancelled' WHERE id=$1`, stop2)
	_, e = target(tripTok, stop2, trackerActionDeliver)
	wantErr("cancelled stop", e, 400, "cancelled")
	if ok, _ := saveTrackerDriverSignature(ctx, conn, stop2, "https://example.com/x.png"); ok {
		t.Fatal("signature saved on a cancelled order")
	}

	// 6. The last stop delivered by the consignee completes the trip; the
	// driver can still sign for it. A cancelled trip takes nothing.
	mustExec(t, conn, `UPDATE tracker_orders SET status='delivered', received_confirmed_at=NOW() WHERE id=$1`, otherStop)
	mustExec(t, conn, `UPDATE tracker_trips SET overall_status='completed' WHERE id=$1`, otherTrip)
	claimAndSign(otherTok, otherStop, "https://example.com/last.png")
	if r := read(otherStop); r.signature == nil {
		t.Fatalf("last stop of a completed trip not signed: %+v", r)
	}
	mustExec(t, conn, `UPDATE tracker_trips SET overall_status='cancelled' WHERE id=$1`, otherTrip)
	_, e = target(otherTok, otherStop, trackerActionDeliver)
	wantErr("cancelled trip", e, 400, "")

	// 7. Single-order token: order_id optional, must match if sent, and a
	// consignee-delivered unsigned order still takes the claim + signature.
	if _, e = target(orderTok, "", trackerActionDeliver); e != nil {
		t.Fatalf("single order without order_id: %v", e)
	}
	_, e = target(orderTok, stop1, trackerActionDeliver)
	wantErr("single token with another order's id", e, 404, "stop_not_on_link")
	mustExec(t, conn, `UPDATE tracker_orders SET status='delivered', received_confirmed_at=NOW() WHERE id=$1`, single)
	claimAndSign(orderTok, single, "https://example.com/single.png")
	if r := read(single); r.signature == nil || !r.claimed {
		t.Fatalf("consignee-delivered single order after signing: %+v", r)
	}
}

// TestTrackerDriverPageStop covers which stop the drive page shows and its
// signature_pending flag: a single order delivered by consignee receipt
// before the driver signed shows the signature step until signed; on a
// trip the most recently delivered stop wins over advancing while it is
// unsigned, then the page moves on, and older unsigned stops never hold
// it back.
//
// Needs TEST_DATABASE_URL, same as TestTrackerDriverStopFlow.
func TestTrackerDriverPageStop(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — run against a scratch Postgres to exercise the drive page stop selection")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	schema := fmt.Sprintf("tracker_page_stop_test_%d", time.Now().UnixNano())
	mustExec(t, conn, `CREATE SCHEMA `+schema)
	defer conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	mustExec(t, conn, `SET search_path TO `+schema)
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
	sql069, err := migrations.FS.ReadFile("069_tracker_arrival_audit.sql")
	if err != nil {
		t.Fatalf("read migration 069: %v", err)
	}
	mustExec(t, conn, string(sql069))

	const (
		companyID = "00000000-0000-4000-8000-00000000c001"
		trip      = "00000000-0000-4000-8000-00000000a001"
		stop1     = "00000000-0000-4000-8000-00000000b001"
		stop2     = "00000000-0000-4000-8000-00000000b002"
		stop3     = "00000000-0000-4000-8000-00000000b003"
		single    = "00000000-0000-4000-8000-00000000b004"
	)
	mustExec(t, conn, `INSERT INTO tracker_trips VALUES ($1,'in_transit','trip-token')`, trip)
	mustExec(t, conn, `INSERT INTO tracker_orders (id, company_id, status, trip_id, stop_sequence) VALUES
		($1,$4,'in_transit',$5,1), ($2,$4,'in_transit',$5,2), ($3,$4,'in_transit',$5,3)`, stop1, stop2, stop3, companyID, trip)
	mustExec(t, conn, `INSERT INTO tracker_orders (id, company_id, status, driver_tracking_token) VALUES ($1,$2,'in_transit','order-token')`, single, companyID)

	// deliver marks a stop delivered the way tryAutoCompleteDelivery does
	// (status + a 'delivered' event), at a given minute so "most recent"
	// is deterministic.
	base := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	deliver := func(id string, minute int) {
		t.Helper()
		mustExec(t, conn, `UPDATE tracker_orders SET status='delivered' WHERE id=$1`, id)
		mustExec(t, conn, `INSERT INTO tracker_order_events (id, order_id, status, reported_by, created_at)
			VALUES (gen_random_uuid(), $1, 'delivered', 'consignee', $2)`, id, base.Add(time.Duration(minute)*time.Minute))
	}
	sign := func(id string) {
		t.Helper()
		if ok, err := saveTrackerDriverSignature(ctx, conn, id, "https://example.com/"+id+".png"); err != nil || !ok {
			t.Fatalf("sign %s: ok=%v err=%v", id, ok, err)
		}
	}
	pending := func(id string) bool {
		t.Helper()
		p, err := loadTrackerStopProgress(ctx, conn, id)
		if err != nil {
			t.Fatalf("progress %s: %v", id, err)
		}
		return p.SignaturePending
	}
	page := func(tripStatus, want string, wantPending bool) {
		t.Helper()
		got, err := resolveTrackerTripPageStop(ctx, conn, trip, tripStatus)
		if err != nil {
			t.Fatalf("page stop: %v", err)
		}
		if got != want {
			t.Fatalf("page stop = %q, want %q", got, want)
		}
		if want != "" && pending(got) != wantPending {
			t.Fatalf("page stop %s signature_pending = %v, want %v", got, !wantPending, wantPending)
		}
	}

	// Single order.
	if pending(single) {
		t.Fatal("in-transit single order should not be signature_pending")
	}
	deliver(single, 0)
	if !pending(single) {
		t.Fatal("consignee-delivered unsigned single order should be signature_pending")
	}
	sign(single)
	if pending(single) {
		t.Fatal("single order still signature_pending after signing")
	}
	mustExec(t, conn, `UPDATE tracker_orders SET status='cancelled', signature_url=NULL WHERE id=$1`, single)
	if pending(single) {
		t.Fatal("cancelled order must not be signature_pending")
	}

	// Trip: starts on the active stop.
	page("in_transit", stop1, false)
	// Consignee confirms stop 1 before the driver signs: stop 1 stays.
	deliver(stop1, 1)
	page("in_transit", stop1, true)
	// Signed: moves on to stop 2.
	sign(stop1)
	page("in_transit", stop2, false)
	// Stop 2 delivered and signed; stop 1 somehow loses its signature —
	// an older unsigned stop doesn't pull the page back.
	deliver(stop2, 2)
	sign(stop2)
	mustExec(t, conn, `UPDATE tracker_orders SET signature_url=NULL WHERE id=$1`, stop1)
	page("in_transit", stop3, false)
	// Last stop delivered unsigned completes the trip; it still shows.
	deliver(stop3, 3)
	page("completed", stop3, true)
	sign(stop3)
	page("completed", "", false)
	// A cancelled trip offers no signature step.
	mustExec(t, conn, `UPDATE tracker_orders SET signature_url=NULL WHERE id=$1`, stop3)
	page("cancelled", "", false)
}
