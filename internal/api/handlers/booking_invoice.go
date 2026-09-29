package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/deploykit/backend/internal/dateutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// assignBookingInvoiceNumber gives a completed booking its invoice number,
// BGR/<FY>/<NNNNN> (e.g. BGR/2627/00001), numbered gap-free per Indian
// financial year via nextSerialNumber. Returns "" with no error when the
// booking doesn't qualify.
//
// Called once, right after the in_progress -> completed transition. It's
// idempotent and never regenerates a number: the UPDATE only matches a
// completed booking whose invoice_number is still NULL, and when it matches
// nothing the tx rolls back — since numbers come from a COUNT, not a
// sequence, that rollback doesn't burn one. Zero-fare bookings (e.g. a free
// ambulance waived before completion) get no number.
//
// Deliberately separate from the completion UPDATE: a numbering failure is
// logged by the caller and never blocks completion or wallet settlement.
func assignBookingInvoiceNumber(ctx context.Context, pool *pgxpool.Pool, bookingID string) (string, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	// One instant for both the FY in the number and invoice_issued_at, so a
	// completion straddling midnight 1 April can't pair a new-FY date with
	// an old-FY number.
	issuedAt := time.Now()
	fy := dateutil.FinancialYearCode(issuedAt)
	number, err := nextSerialNumber(ctx, tx,
		"booking_invoice_number_"+fy,
		`SELECT COUNT(*) FROM bookings WHERE invoice_number LIKE $1`,
		fmt.Sprintf("BGR/%s/", fy))
	if err != nil {
		return "", err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE bookings SET invoice_number = $1, invoice_issued_at = $2
		WHERE id = $3 AND status = 'completed' AND invoice_number IS NULL
		  AND COALESCE(final_fare, 0) > 0
	`, number, issuedAt, bookingID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", nil
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return number, nil
}
