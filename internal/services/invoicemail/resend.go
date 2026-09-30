package invoicemail

// Rider-facing invoice access (post-ride invoices, phase 3): RenderPDF for
// on-demand download and Resend for a rider-requested email. Both reuse the
// phase 2 loader and message builder unchanged. Callers check ownership
// first — nothing here knows who is asking.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/mail"
	"github.com/deploykit/backend/internal/services/bookinginvoice"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ResendPerBooking and ResendPerRider cap rider-requested resends per
	// rolling 24h window. Failed sends count too, so an outage can't be
	// hammered through the endpoint.
	ResendPerBooking = 3
	ResendPerRider   = 10
	resendWindow     = 24 * time.Hour
	// resendWindowSQL is resendWindow for the queries; keep them in step.
	resendWindowSQL = "INTERVAL '24 hours'"
)

var (
	// ErrNoInvoice: the booking has no invoice number (not completed,
	// zero-fare, completed before phase 1, or numbering failed).
	ErrNoInvoice = errors.New("booking has no invoice")
	// ErrEmailDisabled: INVOICE_EMAIL_ENABLED is off or Resend isn't
	// configured.
	ErrEmailDisabled = errors.New("invoice email disabled")
)

// NotSendableError is a do-not-send rule (sendSkipReason) blocking a resend.
// Reason never includes the address.
type NotSendableError struct{ Reason string }

func (e *NotSendableError) Error() string { return "invoice not sendable: " + e.Reason }

// RateLimitError is a resend refused by the per-booking or per-rider cap.
type RateLimitError struct {
	Scope      string // "booking" | "rider"
	RetryAfter time.Time
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("invoice resend limit reached (%s), retry after %s", e.Scope, e.RetryAfter.Format(time.RFC3339))
}

// RenderPDF regenerates a booking's invoice PDF from the stored booking, the
// same way the email path does. Returns ErrNoInvoice when the booking has no
// invoice number. Not gated by INVOICE_EMAIL_ENABLED, which is about email.
func RenderPDF(ctx context.Context, cfg *config.Config, bookingID string) (pdf []byte, filename string, err error) {
	be, err := loadBookingEmail(ctx, db.GetDB().GetPool(), bookingID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ErrNoInvoice
		}
		return nil, "", err
	}
	pdf, err = bookinginvoice.Generate(be.Invoice, cfg.InvoiceIssuer)
	if err != nil {
		return nil, "", err
	}
	return pdf, attachmentName(be.Invoice, cfg.InvoiceIssuer), nil
}

// Resend emails a booking's invoice to its rider on the rider's explicit
// request, regardless of whether the automatic send already happened or was
// skipped for a reason other than the do-not-send rules (which still apply).
//
// It is kept apart from the automatic send-once path: it never reads or
// writes invoice_email_attempts, _claimed_at, _skipped_at or
// invoice_last_error, so it can't make the retry sweeper fire. The one
// shared write is setting invoice_email_sent_at when it is still NULL after
// a successful resend — the rider now has the invoice, so a pending
// automatic retry would only be a duplicate.
//
// Returns the masked recipient address on success.
func Resend(ctx context.Context, cfg *config.Config, bookingID, riderID string) (string, error) {
	if !Enabled() || cfg == nil || !mail.IsConfigured(cfg) {
		return "", ErrEmailDisabled
	}
	pool := db.GetDB().GetPool()

	be, err := loadBookingEmail(ctx, pool, bookingID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNoInvoice
		}
		return "", err
	}
	// Checked before claiming a slot, so a blocked booking doesn't use one up.
	if reason := sendSkipReason(be); reason != "" {
		return "", &NotSendableError{Reason: reason}
	}

	claimedAt, err := claimResend(ctx, pool, bookingID, riderID)
	if err != nil {
		return "", err
	}

	msg, err := Compose(be.Invoice, cfg.InvoiceIssuer, be.RiderEmail)
	if err != nil {
		log.Printf("invoice resend: booking %s generate pdf failed: %s", bookingID, shortError(err, be.RiderEmail))
		return "", err
	}
	// A key of its own per resend: reusing invoice-email/<id> within 24h of
	// the automatic send would make Resend either replay the cached response
	// without sending or reject it as a conflict.
	msg.IdempotencyKey = resendIdempotencyKey(bookingID, claimedAt)

	if err := mail.Send(cfg, msg); err != nil {
		log.Printf("invoice resend: booking %s send failed: %s", bookingID, shortError(err, be.RiderEmail))
		return "", err
	}

	if _, err := pool.Exec(ctx, `
		UPDATE bookings SET invoice_email_sent_at = NOW()
		WHERE id = $1 AND invoice_email_sent_at IS NULL
	`, bookingID); err != nil {
		log.Printf("invoice resend: booking %s sent but failed to record sent_at: %s", bookingID, shortError(err, ""))
	}
	log.Printf("invoice resend: booking %s sent", bookingID)
	return maskEmail(be.RiderEmail), nil
}

// claimResend takes one resend slot, or returns a *RateLimitError. The rider
// row is locked for the transaction so two resends of different bookings
// can't both pass the per-rider sum; the per-booking limit is the guarded
// UPDATE itself (same discipline as claim and the ride-OTP counter).
func claimResend(ctx context.Context, pool *pgxpool.Pool, bookingID, riderID string) (time.Time, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT 1 FROM riders WHERE id = $1 FOR UPDATE`, riderID); err != nil {
		return time.Time{}, err
	}

	// A booking's count only counts while its window is open. Resending a
	// booking whose own window has lapsed resets that booking's count, so
	// its old count is excluded here too.
	var riderUsed int
	var riderFreesAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(invoice_resend_count), 0)::int,
		       MIN(invoice_resend_window_started_at) + `+resendWindowSQL+`
		FROM bookings
		WHERE rider_id = $1
		  AND invoice_resend_window_started_at > NOW() - `+resendWindowSQL+`
	`, riderID).Scan(&riderUsed, &riderFreesAt); err != nil {
		return time.Time{}, err
	}
	if riderUsed >= ResendPerRider {
		e := &RateLimitError{Scope: "rider"}
		if riderFreesAt != nil {
			e.RetryAfter = *riderFreesAt
		}
		return time.Time{}, e
	}

	var claimedAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE bookings
		SET invoice_resend_count = CASE
		        WHEN invoice_resend_window_started_at IS NULL
		          OR invoice_resend_window_started_at <= NOW() - `+resendWindowSQL+`
		        THEN 1 ELSE COALESCE(invoice_resend_count, 0) + 1 END,
		    invoice_resend_window_started_at = CASE
		        WHEN invoice_resend_window_started_at IS NULL
		          OR invoice_resend_window_started_at <= NOW() - `+resendWindowSQL+`
		        THEN NOW() ELSE invoice_resend_window_started_at END
		WHERE id = $1
		  AND invoice_number IS NOT NULL
		  AND (invoice_resend_window_started_at IS NULL
		       OR invoice_resend_window_started_at <= NOW() - `+resendWindowSQL+`
		       OR COALESCE(invoice_resend_count, 0) < $2)
		RETURNING NOW()
	`, bookingID, ResendPerBooking).Scan(&claimedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		e := &RateLimitError{Scope: "booking"}
		var windowStart *time.Time
		if tx.QueryRow(ctx, `SELECT invoice_resend_window_started_at FROM bookings WHERE id = $1`, bookingID).Scan(&windowStart) == nil && windowStart != nil {
			e.RetryAfter = windowStart.Add(resendWindow)
		}
		return time.Time{}, e
	}
	if err != nil {
		return time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return time.Time{}, err
	}
	return claimedAt, nil
}

// resendIdempotencyKey is unique per claimed resend: the claim time from the
// guarded UPDATE, to the microsecond (Postgres' resolution).
func resendIdempotencyKey(bookingID string, claimedAt time.Time) string {
	return fmt.Sprintf("invoice-resend/%s/%d", bookingID, claimedAt.UnixMicro())
}

// maskEmail shows enough of the address for the rider to recognise it:
// "anjali@gmail.com" -> "a•••@gmail.com".
func maskEmail(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at < 1 {
		return "•••"
	}
	return addr[:1] + "•••" + addr[at:]
}
