// Package invoicemail emails a completed booking's invoice PDF to the rider
// (post-ride invoices, phase 2). The PDF comes from bookinginvoice, unchanged;
// this package only adds loading the booking, deciding whether to send, and
// sending via internal/mail with retry bookkeeping on the bookings row.
//
// Flow: UpdateBookingStatus assigns the invoice number, then calls Enqueue,
// which makes the first attempt in a goroutine. StartRetrySweeper retries
// failed attempts with backoff. Every attempt first claims the booking with
// one guarded UPDATE (claim), so the initial send and a sweeper retry can
// never both send.
//
// Everything is off unless INVOICE_EMAIL_ENABLED is "true" or "1", read
// fresh on every call (same pattern as RIDER_ONLINE_PAYMENTS_ENABLED) so it
// can be flipped in Railway without a redeploy.
package invoicemail

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/mail"
	"github.com/deploykit/backend/internal/services/bookinginvoice"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxAttempts is how many sends (initial + retries) a booking gets.
const MaxAttempts = 5

// retryBackoff[n-1] is the wait after the nth attempt before the next one:
// retries at +5m, +30m, +2h, +12h — all well inside the sweeper's 7-day window.
var retryBackoff = []time.Duration{5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour}

// backoffCaseSQL is retryBackoff as a SQL CASE on invoice_email_attempts,
// shared by claim and the sweeper's candidate scan so both agree on when a
// retry is due. Keep in step with retryBackoff (TestBackoffSQLMatches).
const backoffCaseSQL = `CASE COALESCE(invoice_email_attempts, 0)
	WHEN 1 THEN INTERVAL '5 minutes'
	WHEN 2 THEN INTERVAL '30 minutes'
	WHEN 3 THEN INTERVAL '2 hours'
	ELSE INTERVAL '12 hours' END`

// Enabled reports whether INVOICE_EMAIL_ENABLED is on. Default off.
func Enabled() bool {
	v := strings.TrimSpace(os.Getenv("INVOICE_EMAIL_ENABLED"))
	return v == "true" || v == "1"
}

// Enqueue makes the first send attempt for a just-invoiced booking in the
// background, never blocking the completion response. No-op when the flag
// is off or Resend isn't configured.
func Enqueue(cfg *config.Config, bookingID string) {
	if !Enabled() || cfg == nil || !mail.IsConfigured(cfg) {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("invoice email: booking %s recovered from panic: %v", bookingID, r)
			}
		}()
		Send(context.Background(), cfg, bookingID)
	}()
}

// Outcome is what one Send call did.
type Outcome int

const (
	OutcomeNotClaimed Outcome = iota // already sent/skipped, retries exhausted, or not due yet
	OutcomeSent
	OutcomeSkipped // never to be emailed; recorded, not retried
	OutcomeFailed  // recorded; the sweeper retries if attempts remain
)

func (o Outcome) String() string {
	return [...]string{"not claimed", "sent", "skipped", "failed"}[o]
}

// Send claims and sends one booking's invoice email, recording the result on
// the bookings row. Callers gate on Enabled (Enqueue and the sweeper do).
func Send(ctx context.Context, cfg *config.Config, bookingID string) Outcome {
	return send(ctx, cfg, bookingID, "")
}

// SendRedirected is Send — same claim, do-not-send rules (applied to the
// rider's real address) and recording — but delivers to `to` instead of the
// rider. Only for cmd/invoice_email_send -record; ignores the flag.
func SendRedirected(ctx context.Context, cfg *config.Config, bookingID, to string) Outcome {
	return send(ctx, cfg, bookingID, to)
}

// Preview builds a booking's email addressed to `to` without claiming or
// recording anything. skip is the do-not-send reason the real path would
// apply to the rider's actual address ("" when it would send). Only for
// cmd/invoice_email_send.
func Preview(ctx context.Context, cfg *config.Config, bookingID, to string) (msg mail.Message, skip string, err error) {
	be, err := loadBookingEmail(ctx, db.GetDB().GetPool(), bookingID)
	if err != nil {
		return mail.Message{}, "", err
	}
	if skip = sendSkipReason(be); skip != "" {
		return mail.Message{}, skip, nil
	}
	msg, err = Compose(be.Invoice, cfg.InvoiceIssuer, to)
	return msg, "", err
}

func send(ctx context.Context, cfg *config.Config, bookingID, overrideTo string) Outcome {
	pool := db.GetDB().GetPool()

	claimed, attempt, err := claim(ctx, pool, bookingID)
	if err != nil {
		log.Printf("invoice email: booking %s claim failed: %s", bookingID, shortError(err, ""))
		return OutcomeFailed
	}
	if !claimed {
		return OutcomeNotClaimed
	}

	be, err := loadBookingEmail(ctx, pool, bookingID)
	if err != nil {
		return recordFailure(ctx, pool, bookingID, attempt, "load booking", err, "")
	}

	if reason := sendSkipReason(be); reason != "" {
		recordSkip(ctx, pool, bookingID, reason)
		return OutcomeSkipped
	}

	to := be.RiderEmail
	if overrideTo != "" {
		to = overrideTo
	}
	msg, err := Compose(be.Invoice, cfg.InvoiceIssuer, to)
	if err != nil {
		return recordFailure(ctx, pool, bookingID, attempt, "generate pdf", err, be.RiderEmail)
	}
	msg.IdempotencyKey = idempotencyKey(bookingID)

	if err := mail.Send(cfg, msg); err != nil {
		var apiErr *mail.APIError
		if errors.As(err, &apiErr) && apiErr.Name == "invalid_idempotent_request" {
			// This key was already used in the last 24h with a different
			// payload — i.e. an earlier attempt reached Resend and may well
			// have been delivered (its PDF's "Generated on" time differs,
			// hence the different payload). Stop rather than risk a second
			// copy.
			recordSkip(ctx, pool, bookingID, "idempotency conflict: an earlier attempt may have been delivered")
			return OutcomeSkipped
		}
		return recordFailure(ctx, pool, bookingID, attempt, "send", err, be.RiderEmail)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE bookings SET invoice_email_sent_at = NOW(), invoice_last_error = NULL
		WHERE id = $1 AND invoice_email_sent_at IS NULL
	`, bookingID); err != nil {
		// Resend accepted it; a retry would be caught by the idempotency key
		// within 24h, but log loudly — same as the statement mailer.
		log.Printf("invoice email: CRITICAL booking %s sent but failed to record sent_at: %s", bookingID, shortError(err, ""))
	}
	log.Printf("invoice email: booking %s sent (attempt %d)", bookingID, attempt)
	return OutcomeSent
}

// sendSkipReason is every do-not-send rule for a loaded booking.
func sendSkipReason(be *bookingEmail) string {
	if be.Invoice.TotalAmount <= 0 {
		// e.g. an ambulance fare waived to 0 after completion.
		return "fare is zero"
	}
	return skipReason(be.RiderEmail, be.UserDeleted)
}

// Compose renders the PDF with the phase 1 generator and builds the email.
// Exported for cmd/invoice_email_send.
func Compose(inv *bookinginvoice.Invoice, issuer bookinginvoice.Issuer, to string) (mail.Message, error) {
	pdf, err := bookinginvoice.Generate(inv, issuer)
	if err != nil {
		return mail.Message{}, err
	}
	return buildMessage(inv, issuer, to, pdf), nil
}

// claim is the guarded UPDATE every attempt goes through: it only matches an
// invoiced booking that is not yet sent or skipped, has attempts left, and
// either was never claimed or has waited out the backoff since its last
// claim. RowsAffected 0 means someone else holds it or it isn't due — the
// same race discipline as the completion guard in UpdateBookingStatus.
// Returns the attempt number this claim represents.
func claim(ctx context.Context, pool *pgxpool.Pool, bookingID string) (bool, int, error) {
	var attempt int
	err := pool.QueryRow(ctx, `
		UPDATE bookings
		SET invoice_email_claimed_at = NOW(),
		    invoice_email_attempts   = COALESCE(invoice_email_attempts, 0) + 1
		WHERE id = $1
		  AND invoice_number IS NOT NULL
		  AND invoice_email_sent_at IS NULL
		  AND invoice_email_skipped_at IS NULL
		  AND COALESCE(invoice_email_attempts, 0) < $2
		  AND (invoice_email_claimed_at IS NULL
		       OR invoice_email_claimed_at <= NOW() - (`+backoffCaseSQL+`))
		RETURNING invoice_email_attempts
	`, bookingID, MaxAttempts).Scan(&attempt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, 0, nil
		}
		return false, 0, err
	}
	return true, attempt, nil
}

func recordSkip(ctx context.Context, pool *pgxpool.Pool, bookingID, reason string) {
	if _, err := pool.Exec(ctx, `
		UPDATE bookings SET invoice_email_skipped_at = NOW(), invoice_last_error = $2
		WHERE id = $1
	`, bookingID, "skipped: "+reason); err != nil {
		log.Printf("invoice email: booking %s failed to record skip: %s", bookingID, shortError(err, ""))
	}
	log.Printf("invoice email: booking %s skipped: %s", bookingID, reason)
}

func recordFailure(ctx context.Context, pool *pgxpool.Pool, bookingID string, attempt int, stage string, err error, recipient string) Outcome {
	msg := stage + ": " + shortError(err, recipient)
	if _, dbErr := pool.Exec(ctx, `UPDATE bookings SET invoice_last_error = $2 WHERE id = $1`, bookingID, msg); dbErr != nil {
		log.Printf("invoice email: booking %s failed to record error: %s", bookingID, shortError(dbErr, ""))
	}
	if attempt >= MaxAttempts {
		log.Printf("invoice email: booking %s attempt %d/%d failed, giving up: %s", bookingID, attempt, MaxAttempts, msg)
	} else {
		log.Printf("invoice email: booking %s attempt %d/%d failed, will retry: %s", bookingID, attempt, MaxAttempts, msg)
	}
	return OutcomeFailed
}

// shortError is a log- and DB-safe summary: Resend API errors reduce to their
// status and error name (their bodies can echo the recipient), anything else
// is truncated to 200 chars with the recipient address redacted.
func shortError(err error, recipient string) string {
	var apiErr *mail.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Name != "" {
			return fmt.Sprintf("resend status %d %s", apiErr.StatusCode, apiErr.Name)
		}
		return fmt.Sprintf("resend status %d", apiErr.StatusCode)
	}
	s := err.Error()
	if recipient != "" {
		s = strings.ReplaceAll(s, recipient, "<recipient>")
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
