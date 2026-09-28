package handlers

import (
	"context"
	"log"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
)

// StartBookingExpirySweeper ticks every 15s and auto-cancels any booking
// still 'searching' once its search window (cfg.SearchTimeoutSeconds,
// measured from updated_at — see ListPendingBookings for why that column,
// not requested_at) has elapsed. This is a backstop, not the primary
// defense: ListPendingBookings' own updated_at cutoff already hides a
// stale request from every driver's feed the instant the window elapses,
// even if this sweeper is late or down. Meant to be started once with
// `go handlers.StartBookingExpirySweeper(cfg)` from main.go; a panic in one
// tick is recovered so it never kills the sweep for everyone else.
func StartBookingExpirySweeper(cfg *config.Config) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		sweepExpiredBookings(cfg.SearchTimeoutSeconds)
	}
}

func sweepExpiredBookings(timeoutSeconds int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("booking expiry sweeper: recovered from panic: %v", r)
		}
	}()

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Single guarded UPDATE ... WHERE status='searching' — identical race
	// discipline to AcceptBooking's own searching->accepted guard, so an
	// accept landing in the same instant always wins outright (RowsAffected
	// would be 0 here for that row) rather than racing this sweeper.
	// Deliberately NOT routed through UpdateBookingStatus: that path
	// computes a rider cancellation fee, refunds any wallet payment, and
	// auto-blocks the driver on repeated cancels — none of which apply to
	// a system-timed-out search (nobody was ever charged; no driver ever
	// accepted, so there is no driver to charge against a cancel-count).
	rows, err := pool.Query(ctx, `
        UPDATE bookings
        SET status='cancelled', cancelled_at=NOW(), cancelled_by='system', cancel_reason='no_driver_found'
        WHERE status='searching'
          AND updated_at <= NOW() - ($1 || ' seconds')::interval
        RETURNING id, rider_id
    `, timeoutSeconds)
	if err != nil {
		log.Printf("booking expiry sweeper: query error: %v", err)
		return
	}

	type expired struct{ bookingID, riderID string }
	var out []expired
	for rows.Next() {
		var e expired
		if rows.Scan(&e.bookingID, &e.riderID) == nil {
			out = append(out, expired{e.bookingID, e.riderID})
		}
	}
	rows.Close()

	for _, e := range out {
		log.Printf("booking expiry sweeper: expired booking %s (no driver found)", e.bookingID)
		notifyRiderNoDriverFound(e.riderID, e.bookingID)
	}
}
