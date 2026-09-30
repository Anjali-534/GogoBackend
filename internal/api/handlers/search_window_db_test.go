package handlers

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestSearchWindowQueries runs the expiry sweeper's UPDATE and the driver
// feed's SELECT — the exact SQL constants production uses — through pgx
// against a real Postgres, with the timeout as a Go int like
// cfg.SearchTimeoutSeconds.
//
// It exists because of a bug unit tests can't see: both queries once used
// ($1 || ' seconds')::interval, which makes Postgres type $1 as text, and
// pgx v5 refuses to encode a Go int as text — so every call failed with
// "cannot find encode plan" before reaching the database. The sweeper
// never expired anything and the driver feed returned 500 on every poll.
//
// Needs TEST_DATABASE_URL (e.g. postgres://postgres:pw@localhost:5432/postgres).
// Everything happens inside a throwaway schema that is dropped afterwards,
// so it never touches existing tables.
func TestSearchWindowQueries(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — run against a scratch Postgres to exercise the sweeper and driver-feed SQL")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	schema := fmt.Sprintf("search_window_test_%d", time.Now().UnixNano())
	mustExec(t, conn, `CREATE SCHEMA `+schema)
	defer conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	mustExec(t, conn, `SET search_path TO `+schema)

	// Just the columns the two queries touch, with production's types.
	mustExec(t, conn, `CREATE TABLE users (id UUID PRIMARY KEY, name TEXT)`)
	mustExec(t, conn, `CREATE TABLE riders (id UUID PRIMARY KEY, user_id UUID REFERENCES users(id))`)
	mustExec(t, conn, `CREATE TABLE service_types (id UUID PRIMARY KEY, name TEXT)`)
	mustExec(t, conn, `CREATE TABLE bookings (
		id UUID PRIMARY KEY, rider_id UUID REFERENCES riders(id), service_type_id UUID REFERENCES service_types(id),
		status TEXT,
		pickup_lat DOUBLE PRECISION, pickup_lng DOUBLE PRECISION, pickup_address TEXT,
		drop_lat DOUBLE PRECISION, drop_lng DOUBLE PRECISION, drop_address TEXT,
		estimated_fare DECIMAL(10,2), distance_km DECIMAL(10,2),
		requested_at TIMESTAMPTZ DEFAULT NOW(), updated_at TIMESTAMPTZ DEFAULT NOW(),
		cancelled_at TIMESTAMPTZ, cancelled_by TEXT, cancel_reason TEXT)`)

	const (
		userID    = "00000000-0000-4000-8000-000000000001"
		riderID   = "00000000-0000-4000-8000-000000000002"
		serviceID = "00000000-0000-4000-8000-000000000003"
		stale     = "00000000-0000-4000-8000-0000000000a1" // searching, window long gone
		fresh     = "00000000-0000-4000-8000-0000000000a2" // searching, inside the window
		accepted  = "00000000-0000-4000-8000-0000000000a3" // old but not searching
	)
	mustExec(t, conn, `INSERT INTO users VALUES ($1, 'Rider')`, userID)
	mustExec(t, conn, `INSERT INTO riders VALUES ($1, $2)`, riderID, userID)
	mustExec(t, conn, `INSERT INTO service_types VALUES ($1, 'Cab')`, serviceID)
	for _, b := range []struct {
		id, status string
		age        string
	}{{stale, "searching", "10 minutes"}, {fresh, "searching", "30 seconds"}, {accepted, "accepted", "10 minutes"}} {
		mustExec(t, conn, `INSERT INTO bookings (id, rider_id, service_type_id, status, pickup_address, drop_address, estimated_fare, distance_km, updated_at)
			VALUES ($1, $2, $3, $4, 'A', 'B', 100, 5, NOW() - $5::interval)`, b.id, riderID, serviceID, b.status, b.age)
	}

	timeoutSeconds := 180 // a Go int, exactly like cfg.SearchTimeoutSeconds

	// Driver feed: only the booking still inside its window.
	feed := queryIDs(t, conn, pendingBookingsSQL, timeoutSeconds)
	if len(feed) != 1 || feed[0] != fresh {
		t.Fatalf("pendingBookingsSQL returned %v, want only %s", feed, fresh)
	}

	// Sweeper: expires only the stale searching booking.
	expired := queryIDs(t, conn, expireSearchingBookingsSQL, timeoutSeconds)
	if len(expired) != 1 || expired[0] != stale {
		t.Fatalf("expireSearchingBookingsSQL expired %v, want only %s", expired, stale)
	}
	wantState := map[string][3]string{
		stale:    {"cancelled", "system", "no_driver_found"},
		fresh:    {"searching", "", ""},
		accepted: {"accepted", "", ""},
	}
	for id, want := range wantState {
		var got [3]string
		if err := conn.QueryRow(ctx, `SELECT status, COALESCE(cancelled_by,''), COALESCE(cancel_reason,'') FROM bookings WHERE id = $1`, id).
			Scan(&got[0], &got[1], &got[2]); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if got != want {
			t.Errorf("booking %s = %v, want %v", id, got, want)
		}
	}

	// A second sweep finds nothing left to expire.
	if again := queryIDs(t, conn, expireSearchingBookingsSQL, timeoutSeconds); len(again) != 0 {
		t.Errorf("second sweep expired %v, want none", again)
	}
}

func mustExec(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// queryIDs runs sql and returns the first column of every row, sorted. The
// query error is fatal — that is the failure this test guards against.
func queryIDs(t *testing.T, conn *pgx.Conn, sql string, args ...any) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		if b, ok := vals[0].([16]byte); ok { // UUID columns come back as raw bytes
			ids = append(ids, fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
		} else {
			ids = append(ids, fmt.Sprint(vals[0]))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query failed: %v", err)
	}
	sort.Strings(ids)
	return ids
}
