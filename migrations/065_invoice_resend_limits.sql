-- Post-ride invoices, phase 3: rate limits for the rider-requested invoice
-- resend (POST /gogoo/bookings/:id/invoice/resend, invoicemail.Resend).
-- All columns nullable, no backfill — existing bookings have never been
-- resent.
--
--   invoice_resend_count             resends claimed in the current window
--   invoice_resend_window_started_at when the current 24h window began; a
--                                    claim after it has lapsed starts a new
--                                    window with the count back at 1
--
-- Same shape as otp_attempts / otp_locked_until: a counter on the bookings
-- row, bumped by one guarded UPDATE that only matches while the booking is
-- under its limit (3 per booking per 24h), so concurrent taps can't both get
-- the last slot. The per-rider cap (10 per rider per 24h) is summed from
-- these same columns across the rider's bookings, under a lock on the rider
-- row, so it needs no column of its own.
--
-- Deliberately separate from the invoice_email_* columns (migration 064):
-- a resend never touches the automatic send's attempts, claim or skip state,
-- so it can't feed the retry sweeper.
ALTER TABLE bookings
  ADD COLUMN IF NOT EXISTS invoice_resend_count             INT DEFAULT 0,
  ADD COLUMN IF NOT EXISTS invoice_resend_window_started_at TIMESTAMPTZ;

-- The per-rider cap's sum. Partial: only bookings that were ever resent.
CREATE INDEX IF NOT EXISTS idx_bookings_invoice_resend_rider
  ON bookings (rider_id, invoice_resend_window_started_at)
  WHERE invoice_resend_window_started_at IS NOT NULL;
