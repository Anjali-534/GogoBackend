package invoicemail

import (
	"context"
	"log"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/mail"
)

const (
	sweepInterval = time.Minute
	sweepBatch    = 50
	// retryWindow bounds how old a completion the sweeper still retries.
	retryWindow = "7 days"
)

// StartRetrySweeper ticks every minute and retries invoice emails whose
// earlier attempt failed. Same shape as handlers.StartBookingExpirySweeper:
// start once with `go invoicemail.StartRetrySweeper(cfg)` from main.go; a
// panic in one tick is recovered. Each tick is a no-op while
// INVOICE_EMAIL_ENABLED is off.
func StartRetrySweeper(cfg *config.Config) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		sweepRetries(cfg)
	}
}

func sweepRetries(cfg *config.Config) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("invoice email sweeper: recovered from panic: %v", r)
		}
	}()
	if !Enabled() || !mail.IsConfigured(cfg) {
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Only bookings with at least one attempt: a booking invoiced while the
	// flag was off never had its initial send, so turning the flag on
	// doesn't mail out the last 7 days' backlog. claim re-checks all of
	// this atomically; this scan just picks candidates.
	rows, err := pool.Query(ctx, `
		SELECT id::text FROM bookings
		WHERE invoice_number IS NOT NULL
		  AND invoice_email_sent_at IS NULL
		  AND invoice_email_skipped_at IS NULL
		  AND invoice_email_attempts >= 1
		  AND invoice_email_attempts < $1
		  AND completed_at >= NOW() - INTERVAL '`+retryWindow+`'
		  AND invoice_email_claimed_at <= NOW() - (`+backoffCaseSQL+`)
		ORDER BY invoice_email_claimed_at
		LIMIT $2
	`, MaxAttempts, sweepBatch)
	if err != nil {
		log.Printf("invoice email sweeper: query error: %s", shortError(err, ""))
		return
	}
	var due []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			due = append(due, id)
		}
	}
	rows.Close()

	for _, id := range due {
		retryOne(ctx, cfg, id)
	}
}

// retryOne isolates a panic to one booking so the rest of the batch runs.
func retryOne(ctx context.Context, cfg *config.Config, bookingID string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("invoice email sweeper: booking %s recovered from panic: %v", bookingID, r)
		}
	}()
	Send(ctx, cfg, bookingID)
}
