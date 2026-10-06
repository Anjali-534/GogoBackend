package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestEnsureRiderProfile covers the "Could not identify your account" bug:
// a users row with no riders row (here, a driver-only account) could log in
// to user-app but GET /gogoo/rider/profile 404'd and POST /gogoo/bookings
// 400'd, because both looked the rider up by user_id and nothing ever
// created the row. It runs the same helpers those handlers call
// (loadRiderProfile, resolveBookingRider) against a real Postgres.
//
// Needs TEST_DATABASE_URL, same as TestSearchWindowQueries. Everything
// happens inside a throwaway schema that is dropped afterwards.
func TestEnsureRiderProfile(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — run against a scratch Postgres to exercise rider-profile creation")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	schema := fmt.Sprintf("rider_profile_test_%d", time.Now().UnixNano())
	mustExec(t, conn, `CREATE SCHEMA `+schema)
	defer conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	mustExec(t, conn, `SET search_path TO `+schema)

	// Production's constraints on the columns involved: riders.user_id is
	// UNIQUE (002), phone nullable (059), referral_code UNIQUE
	// (MigrateReferrals).
	mustExec(t, conn, `CREATE TABLE users (id UUID PRIMARY KEY, email TEXT UNIQUE, name TEXT)`)
	mustExec(t, conn, `CREATE TABLE drivers (id UUID PRIMARY KEY, user_id UUID UNIQUE REFERENCES users(id))`)
	mustExec(t, conn, `CREATE TABLE riders (
		id UUID PRIMARY KEY, user_id UUID NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
		phone TEXT, rating DECIMAL(3,2) DEFAULT 5.00, total_rides INT DEFAULT 0,
		wallet_balance DECIMAL(10,2) DEFAULT 0, outstanding_cancellation_fee DECIMAL(10,2) DEFAULT 0,
		referral_code TEXT UNIQUE)`)
	mustExec(t, conn, `CREATE TABLE service_types (id UUID PRIMARY KEY, name TEXT)`)
	mustExec(t, conn, `CREATE TABLE bookings (
		id UUID PRIMARY KEY, rider_id UUID NOT NULL REFERENCES riders(id), service_type_id UUID REFERENCES service_types(id),
		status TEXT, pickup_address TEXT, drop_address TEXT, estimated_fare DECIMAL(10,2))`)

	const (
		driverUserID = "00000000-0000-4000-8000-0000000000d1"
		driverID     = "00000000-0000-4000-8000-0000000000d2"
		panelUserID  = "00000000-0000-4000-8000-0000000000e1"
		serviceID    = "00000000-0000-4000-8000-000000000003"
	)
	mustExec(t, conn, `INSERT INTO users VALUES ($1, 'driver@example.com', 'Driver Only')`, driverUserID)
	mustExec(t, conn, `INSERT INTO drivers VALUES ($1, $2)`, driverID, driverUserID)
	mustExec(t, conn, `INSERT INTO service_types VALUES ($1, 'Cab')`, serviceID)

	riderCount := func(userID string) int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM riders WHERE user_id=$1`, userID).Scan(&n); err != nil {
			t.Fatalf("count riders: %v", err)
		}
		return n
	}

	if n := riderCount(driverUserID); n != 0 {
		t.Fatalf("precondition: driver-only user has %d riders rows, want 0", n)
	}

	// 1. GET /gogoo/rider/profile for the driver-only user creates the row.
	p, err := loadRiderProfile(ctx, conn, driverUserID, "")
	if err != nil {
		t.Fatalf("loadRiderProfile (first call): %v", err)
	}
	if p.RiderID == "" {
		t.Fatal("loadRiderProfile returned an empty rider_id")
	}
	if n := riderCount(driverUserID); n != 1 {
		t.Fatalf("after first profile call: %d riders rows, want 1", n)
	}
	var phoneIsNull bool
	var code string
	if err := conn.QueryRow(ctx, `SELECT phone IS NULL, COALESCE(referral_code,'') FROM riders WHERE user_id=$1`, driverUserID).
		Scan(&phoneIsNull, &code); err != nil {
		t.Fatalf("read created rider: %v", err)
	}
	if !phoneIsNull || !strings.HasPrefix(code, "GU") {
		t.Errorf("created rider: phone NULL=%v referral_code=%q, want NULL phone and a GU code", phoneIsNull, code)
	}

	// 2. A second call reuses the row instead of creating another.
	p2, err := loadRiderProfile(ctx, conn, driverUserID, "")
	if err != nil {
		t.Fatalf("loadRiderProfile (second call): %v", err)
	}
	if p2.RiderID != p.RiderID {
		t.Errorf("second call returned rider %s, want %s", p2.RiderID, p.RiderID)
	}
	again, err := ensureRiderProfile(ctx, conn, driverUserID)
	if err != nil || again != p.RiderID {
		t.Errorf("ensureRiderProfile again = %q, %v; want %s", again, err, p.RiderID)
	}
	if n := riderCount(driverUserID); n != 1 {
		t.Fatalf("after repeat calls: %d riders rows, want 1", n)
	}

	// 3. CreateBooking resolves the same rider for this user's token, and a
	// booking row referencing it can be inserted (bookings.rider_id FK).
	bookingRider, err := resolveBookingRider(ctx, conn, driverUserID, "")
	if err != nil {
		t.Fatalf("resolveBookingRider: %v", err)
	}
	if bookingRider != p.RiderID {
		t.Fatalf("resolveBookingRider = %s, want %s", bookingRider, p.RiderID)
	}
	mustExec(t, conn, `INSERT INTO bookings VALUES ('00000000-0000-4000-8000-0000000000b1', $1, $2, 'searching', 'A', 'B', 100)`,
		bookingRider, serviceID)

	// A first booking with no prior profile call also works: CreateBooking
	// alone creates the row.
	const freshUserID = "00000000-0000-4000-8000-0000000000f1"
	mustExec(t, conn, `INSERT INTO users VALUES ($1, 'fresh@example.com', 'No Profile Call')`, freshUserID)
	freshRider, err := resolveBookingRider(ctx, conn, freshUserID, "")
	if err != nil || freshRider == "" {
		t.Fatalf("resolveBookingRider for a user with no riders row = %q, %v", freshRider, err)
	}
	if n := riderCount(freshUserID); n != 1 {
		t.Fatalf("after booking-only path: %d riders rows, want 1", n)
	}

	// Panel tokens never get a riders row created on their behalf.
	if _, err := loadRiderProfile(ctx, conn, panelUserID, "cab"); !errors.Is(err, errNoRiderProfile) {
		t.Errorf("loadRiderProfile for panel token: err = %v, want errNoRiderProfile", err)
	}
	if _, err := resolveBookingRider(ctx, conn, panelUserID, "cab"); !errors.Is(err, errNoRiderProfile) {
		t.Errorf("resolveBookingRider for panel token: err = %v, want errNoRiderProfile", err)
	}
	if n := riderCount(panelUserID); n != 0 {
		t.Errorf("panel token: %d riders rows created, want 0", n)
	}
}
