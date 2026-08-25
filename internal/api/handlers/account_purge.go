package handlers

import (
	"context"
	"log"
	"time"

	"github.com/deploykit/backend/internal/db"
)

// StartAccountPurge ticks daily and hard-deletes the location-history trail
// belonging to riders who deleted their account 30+ days ago — the
// retention window promised in the privacy policy. Booking/payment rows
// themselves are never touched here; they stay for the GST-mandated 3-year
// window and were already PII-scrubbed at deletion time (see
// DeleteRiderAccount). Meant to be started once with
// `go handlers.StartAccountPurge()` from main.go, same idiom as
// StartScheduledDispatcher.
func StartAccountPurge() {
	purgeDueAccounts()
	purgeDueDriverAccounts()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		purgeDueAccounts()
		purgeDueDriverAccounts()
	}
}

func purgeDueAccounts() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("account purge: recovered from panic: %v", r)
		}
	}()

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT r.id, r.user_id, COALESCE(r.wallet_balance,0)
		FROM riders r
		JOIN users u ON u.id = r.user_id
		WHERE u.is_active = false
		  AND u.deleted_at IS NOT NULL
		  AND u.deleted_at <= NOW() - INTERVAL '30 days'
	`)
	if err != nil {
		log.Printf("account purge: query error: %v", err)
		return
	}

	type due struct {
		riderID, userID string
		walletBalance   float64
	}
	var toPurge []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.riderID, &d.userID, &d.walletBalance); err != nil {
			continue
		}
		toPurge = append(toPurge, d)
	}
	rows.Close()

	for _, d := range toPurge {
		// Defensive re-check: a balance can change between the initial delete
		// request and the 30-day mark (e.g. a delayed refund landing after
		// deletion). Never silently purge real money — leave the account
		// soft-deleted and unpurged until it's settled.
		if d.walletBalance != 0 {
			log.Printf("account purge: skipping rider %s — non-zero wallet balance %.2f, needs manual settlement", d.riderID, d.walletBalance)
			continue
		}

		ct, err := pool.Exec(ctx, `
			DELETE FROM location_history
			WHERE booking_id IN (SELECT id FROM bookings WHERE rider_id = $1)
		`, d.riderID)
		if err != nil {
			log.Printf("account purge: failed to purge location history for rider %s: %v", d.riderID, err)
			continue
		}
		log.Printf("account purge: purged %d location_history rows for rider %s (user %s)", ct.RowsAffected(), d.riderID, d.userID)
	}
}

// purgeDueDriverAccounts is purgeDueAccounts' driver-side counterpart —
// same 30-day window, same non-zero-balance re-check (deletion already
// required wallet_balance=0, but a late-settling ride payout could still
// land after deletion), same location_history-only scope. driver_documents,
// bank/UPI details, and earnings/ledger rows are never touched here — see
// DeleteDriverAccount for why.
func purgeDueDriverAccounts() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("account purge: recovered from panic (drivers): %v", r)
		}
	}()

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT d.id, d.user_id, COALESCE(d.wallet_balance,0)
		FROM drivers d
		JOIN users u ON u.id = d.user_id
		WHERE u.is_active = false
		  AND u.deleted_at IS NOT NULL
		  AND u.deleted_at <= NOW() - INTERVAL '30 days'
	`)
	if err != nil {
		log.Printf("account purge: driver query error: %v", err)
		return
	}

	type due struct {
		driverID, userID string
		walletBalance    float64
	}
	var toPurge []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.driverID, &d.userID, &d.walletBalance); err != nil {
			continue
		}
		toPurge = append(toPurge, d)
	}
	rows.Close()

	for _, d := range toPurge {
		if d.walletBalance != 0 {
			log.Printf("account purge: skipping driver %s — non-zero wallet balance %.2f, needs manual settlement", d.driverID, d.walletBalance)
			continue
		}

		ct, err := pool.Exec(ctx, `
			DELETE FROM location_history
			WHERE booking_id IN (SELECT id FROM bookings WHERE driver_id = $1)
		`, d.driverID)
		if err != nil {
			log.Printf("account purge: failed to purge location history for driver %s: %v", d.driverID, err)
			continue
		}
		log.Printf("account purge: purged %d location_history rows for driver %s (user %s)", ct.RowsAffected(), d.driverID, d.userID)
	}
}
