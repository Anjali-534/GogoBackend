package handlers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestActingRole pins the rule that the assigned driver wins: a caller who
// is both the rider and the driver of a booking (one Google account signed
// into both apps) used to resolve as "rider", so every driver-side status
// change on that ride came back 403.
func TestActingRole(t *testing.T) {
	cases := []struct {
		p    bookingParty
		want string
	}{
		{bookingParty{IsRider: true, IsDriver: true}, "driver"},
		{bookingParty{IsDriver: true}, "driver"},
		{bookingParty{IsRider: true}, "rider"},
		{bookingParty{}, ""},
	}
	for _, tc := range cases {
		if got := tc.p.actingRole(); got != tc.want {
			t.Errorf("%+v.actingRole() = %q, want %q", tc.p, got, tc.want)
		}
	}
}

// TestRideOwnershipQueries runs the production SQL behind self-ride
// rejection (selfRideSQL), the driver feed's own-booking exclusion
// (pendingBookingsSQL) and rider/driver resolution (bookingPartiesSQL)
// against a real Postgres.
//
// Needs TEST_DATABASE_URL, same as TestSearchWindowQueries. Everything
// happens inside a throwaway schema that is dropped afterwards.
func TestRideOwnershipQueries(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — run against a scratch Postgres to exercise the ride-ownership SQL")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	schema := fmt.Sprintf("ride_ownership_test_%d", time.Now().UnixNano())
	mustExec(t, conn, `CREATE SCHEMA `+schema)
	defer conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	mustExec(t, conn, `SET search_path TO `+schema)

	mustExec(t, conn, `CREATE TABLE users (id UUID PRIMARY KEY, name TEXT)`)
	mustExec(t, conn, `CREATE TABLE riders (id UUID PRIMARY KEY, user_id UUID REFERENCES users(id))`)
	mustExec(t, conn, `CREATE TABLE drivers (id UUID PRIMARY KEY, user_id UUID REFERENCES users(id))`)
	mustExec(t, conn, `CREATE TABLE service_types (id UUID PRIMARY KEY, name TEXT)`)
	mustExec(t, conn, `CREATE TABLE bookings (
		id UUID PRIMARY KEY, rider_id UUID REFERENCES riders(id), driver_id UUID REFERENCES drivers(id),
		service_type_id UUID REFERENCES service_types(id), status TEXT,
		pickup_lat DOUBLE PRECISION, pickup_lng DOUBLE PRECISION, pickup_address TEXT,
		drop_lat DOUBLE PRECISION, drop_lng DOUBLE PRECISION, drop_address TEXT,
		estimated_fare DECIMAL(10,2), distance_km DECIMAL(10,2),
		requested_at TIMESTAMPTZ DEFAULT NOW(), updated_at TIMESTAMPTZ DEFAULT NOW())`)

	const (
		bothUser    = "00000000-0000-4000-8000-000000000001" // rider AND driver (same Google account in both apps)
		riderUser   = "00000000-0000-4000-8000-000000000002" // rider only
		driverUser  = "00000000-0000-4000-8000-000000000003" // driver only
		strangerUsr = "00000000-0000-4000-8000-000000000004" // neither
		bothRider   = "00000000-0000-4000-8000-0000000000b1"
		bothDriver  = "00000000-0000-4000-8000-0000000000b2"
		otherRider  = "00000000-0000-4000-8000-0000000000b3"
		otherDriver = "00000000-0000-4000-8000-0000000000b4"
		serviceID   = "00000000-0000-4000-8000-0000000000c1"
		ownReq      = "00000000-0000-4000-8000-0000000000a1" // searching, requested by bothUser
		otherReq    = "00000000-0000-4000-8000-0000000000a2" // searching, requested by riderUser
		selfRide    = "00000000-0000-4000-8000-0000000000a3" // accepted, bothUser on both sides (legacy)
		normalRide  = "00000000-0000-4000-8000-0000000000a4" // accepted, riderUser + driverUser
	)
	for _, u := range []string{bothUser, riderUser, driverUser, strangerUsr} {
		mustExec(t, conn, `INSERT INTO users VALUES ($1, 'U')`, u)
	}
	mustExec(t, conn, `INSERT INTO riders VALUES ($1, $2), ($3, $4)`, bothRider, bothUser, otherRider, riderUser)
	mustExec(t, conn, `INSERT INTO drivers VALUES ($1, $2), ($3, $4)`, bothDriver, bothUser, otherDriver, driverUser)
	mustExec(t, conn, `INSERT INTO service_types VALUES ($1, 'Cab')`, serviceID)
	for _, b := range []struct {
		id, rider, status string
		driver            any
	}{
		{ownReq, bothRider, "searching", nil},
		{otherReq, otherRider, "searching", nil},
		{selfRide, bothRider, "accepted", bothDriver},
		{normalRide, otherRider, "accepted", otherDriver},
	} {
		mustExec(t, conn, `INSERT INTO bookings (id, rider_id, driver_id, service_type_id, status, pickup_address, drop_address, estimated_fare, distance_km)
			VALUES ($1, $2, $3, $4, $5, 'A', 'B', 100, 5)`, b.id, b.rider, b.driver, serviceID, b.status)
	}

	// Driver feed: a driver never sees their own ride request.
	if feed := queryIDs(t, conn, pendingBookingsSQL, 180, bothUser); len(feed) != 1 || feed[0] != otherReq {
		t.Errorf("feed for rider+driver user = %v, want only %s", feed, otherReq)
	}
	if feed := queryIDs(t, conn, pendingBookingsSQL, 180, driverUser); len(feed) != 2 {
		t.Errorf("feed for driver-only user = %v, want both requests", feed)
	}

	// Self-ride detection for AcceptBooking.
	for _, tc := range []struct {
		booking, user string
		want          bool
	}{
		{ownReq, bothUser, true},
		{otherReq, bothUser, false},
		{otherReq, driverUser, false},
	} {
		var got bool
		if err := conn.QueryRow(ctx, selfRideSQL, tc.booking, tc.user).Scan(&got); err != nil {
			t.Fatalf("selfRideSQL(%s, %s): %v", tc.booking, tc.user, err)
		}
		if got != tc.want {
			t.Errorf("selfRideSQL(%s, %s) = %v, want %v", tc.booking, tc.user, got, tc.want)
		}
	}

	// Party resolution: both flags reported independently; acting role is
	// driver whenever the caller is the assigned driver.
	for _, tc := range []struct {
		booking, user string
		want          bookingParty
		wantRole      string
	}{
		{selfRide, bothUser, bookingParty{IsRider: true, IsDriver: true, Status: "accepted"}, "driver"},
		{normalRide, driverUser, bookingParty{IsDriver: true, Status: "accepted"}, "driver"},
		{normalRide, riderUser, bookingParty{IsRider: true, Status: "accepted"}, "rider"},
		{normalRide, strangerUsr, bookingParty{Status: "accepted"}, ""},
		{ownReq, bothUser, bookingParty{IsRider: true, Status: "searching"}, "rider"}, // no driver assigned yet
	} {
		var got bookingParty
		if err := conn.QueryRow(ctx, bookingPartiesSQL, tc.booking, tc.user).Scan(&got.IsRider, &got.IsDriver, &got.Status); err != nil {
			t.Fatalf("bookingPartiesSQL(%s, %s): %v", tc.booking, tc.user, err)
		}
		if got != tc.want {
			t.Errorf("bookingPartiesSQL(%s, %s) = %+v, want %+v", tc.booking, tc.user, got, tc.want)
		}
		if role := got.actingRole(); role != tc.wantRole {
			t.Errorf("actingRole for (%s, %s) = %q, want %q", tc.booking, tc.user, role, tc.wantRole)
		}
	}
}
