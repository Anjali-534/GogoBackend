package handlers

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestMigrateNotificationsTargetColumns checks that migrateNotifications
// (MigrateNotifications' DDL) gives every database production's
// target_type/target_id columns — added to prod by hand, in no repo before
// this — and is a no-op where they already exist.
//
// Needs TEST_DATABASE_URL, same as TestSearchWindowQueries. Each case runs in
// its own throwaway schema that is dropped afterwards.
func TestMigrateNotificationsTargetColumns(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — run against a scratch Postgres to exercise MigrateNotifications")
	}
	ctx := context.Background()

	inSchema := func(t *testing.T) (*pgx.Conn, string) {
		t.Helper()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		schema := fmt.Sprintf("notif_migrate_test_%d", time.Now().UnixNano())
		mustExec(t, conn, `CREATE SCHEMA `+schema)
		t.Cleanup(func() {
			conn.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) //nolint:errcheck
			conn.Close(context.Background())
		})
		mustExec(t, conn, `SET search_path TO `+schema)
		return conn, schema
	}

	// columns returns notifications' columns as "name type nullable default".
	columns := func(t *testing.T, conn *pgx.Conn, schema string) []string {
		t.Helper()
		rows, err := conn.Query(ctx, `
			SELECT column_name || ' ' || data_type || ' ' || is_nullable || ' ' || COALESCE(column_default, '-')
			FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = 'notifications'
			ORDER BY column_name`, schema)
		if err != nil {
			t.Fatalf("read columns: %v", err)
		}
		defer rows.Close()
		var cols []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatalf("scan column: %v", err)
			}
			cols = append(cols, c)
		}
		return cols
	}

	// Production's definition: nullable, no default.
	wantTarget := []string{"target_id uuid YES -", "target_type text YES -"}
	targetCols := func(t *testing.T, conn *pgx.Conn, schema string) []string {
		t.Helper()
		var got []string
		for _, c := range columns(t, conn, schema) {
			if len(c) > 9 && (c[:10] == "target_id " || (len(c) > 11 && c[:12] == "target_type ")) {
				got = append(got, c)
			}
		}
		return got
	}

	run := func(t *testing.T, conn *pgx.Conn, boot string) {
		t.Helper()
		if err := migrateNotifications(ctx, conn); err != nil {
			t.Fatalf("%s: migrateNotifications: %v", boot, err)
		}
	}

	t.Run("fresh database, no notifications table", func(t *testing.T) {
		conn, schema := inSchema(t)
		run(t, conn, "first boot")
		if got := targetCols(t, conn, schema); !reflect.DeepEqual(got, wantTarget) {
			t.Fatalf("target columns %v, want %v", got, wantTarget)
		}
	})

	// The real fresh-boot state: file migrations run first, so 001's
	// per-user notifications table is already there.
	t.Run("fresh database after file migrations (001's table)", func(t *testing.T) {
		conn, schema := inSchema(t)
		mustExec(t, conn, `CREATE TABLE notifications (
			id UUID PRIMARY KEY, user_id UUID NOT NULL, project_id UUID, type TEXT NOT NULL,
			title TEXT NOT NULL, message TEXT, is_read BOOLEAN DEFAULT FALSE, metadata JSONB,
			created_at TIMESTAMPTZ DEFAULT NOW())`)
		run(t, conn, "first boot")
		if got := targetCols(t, conn, schema); !reflect.DeepEqual(got, wantTarget) {
			t.Fatalf("target columns %v, want %v", got, wantTarget)
		}
	})

	t.Run("prod-shaped table is a no-op, second boot too", func(t *testing.T) {
		conn, schema := inSchema(t)
		mustExec(t, conn, `CREATE TABLE notifications (
			id UUID PRIMARY KEY, title TEXT NOT NULL DEFAULT '', body TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT 'general', target_audience TEXT NOT NULL DEFAULT 'all',
			coupon_code TEXT, link_url TEXT, is_active BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), target_user_id UUID, target_type TEXT,
			target_id UUID, target_category TEXT, target_user_ids UUID[], target_hospital_id UUID,
			target_hospital_ids UUID[])`)
		const rowID, targetID = "00000000-0000-4000-8000-0000000000c1", "00000000-0000-4000-8000-0000000000c2"
		mustExec(t, conn, `INSERT INTO notifications (id, title, target_type, target_id) VALUES ($1, 'Hi', 'booking', $2)`, rowID, targetID)
		before := columns(t, conn, schema)

		run(t, conn, "first boot")
		if after := columns(t, conn, schema); !reflect.DeepEqual(after, before) {
			t.Fatalf("columns changed on a prod-shaped table:\n got  %v\n want %v", after, before)
		}
		run(t, conn, "second boot")
		if after := columns(t, conn, schema); !reflect.DeepEqual(after, before) {
			t.Fatalf("columns changed on second boot:\n got  %v\n want %v", after, before)
		}

		var gotType, gotID string
		if err := conn.QueryRow(ctx, `SELECT target_type, target_id::text FROM notifications WHERE id = $1`, rowID).
			Scan(&gotType, &gotID); err != nil {
			t.Fatalf("read row: %v", err)
		}
		if gotType != "booking" || gotID != targetID {
			t.Fatalf("existing row changed: target_type=%q target_id=%q", gotType, gotID)
		}
	})
}
