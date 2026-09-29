-- Post-ride invoices, phase 2: email the invoice PDF to the rider after
-- completion (internal/services/invoicemail). All columns nullable, no
-- backfill — existing bookings simply have no email state.
--
--   invoice_email_sent_at     set once Resend accepts the email; never cleared
--   invoice_email_attempts    claims made so far (initial send + retries); the
--                             sweeper gives up at 5
--   invoice_last_error        short, PII-free reason for the last failure or
--                             skip — never the email body, PDF or address
--   invoice_email_claimed_at  time of the latest claim. Doubles as the
--                             in-flight lease and as the last-attempt time the
--                             retry backoff is measured from
--   invoice_email_skipped_at  set when the booking must never be emailed
--                             (deleted account, synthetic tracker-company
--                             address, implausible address, fare waived to 0,
--                             idempotency conflict); the reason goes in
--                             invoice_last_error. Terminal, never retried
--
-- Every send first claims the row with one guarded UPDATE (see
-- invoicemail.claim) that only matches when sent_at and skipped_at are NULL,
-- attempts < 5 and the backoff since claimed_at has elapsed, so a retry
-- racing the initial send can't double-send.
ALTER TABLE bookings
  ADD COLUMN IF NOT EXISTS invoice_email_sent_at    TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS invoice_email_attempts   INT DEFAULT 0,
  ADD COLUMN IF NOT EXISTS invoice_last_error       TEXT,
  ADD COLUMN IF NOT EXISTS invoice_email_claimed_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS invoice_email_skipped_at TIMESTAMPTZ;

-- The retry sweeper's candidate scan (runs every minute). Partial on
-- attempts >= 1 because the sweeper only retries bookings whose initial send
-- was already attempted: bookings invoiced while INVOICE_EMAIL_ENABLED was
-- off stay out of the index instead of accumulating in it forever.
CREATE INDEX IF NOT EXISTS idx_bookings_invoice_email_retry
  ON bookings (invoice_email_claimed_at)
  WHERE invoice_number IS NOT NULL
    AND invoice_email_sent_at IS NULL
    AND invoice_email_skipped_at IS NULL
    AND invoice_email_attempts >= 1;
