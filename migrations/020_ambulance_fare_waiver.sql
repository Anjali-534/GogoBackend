-- Free-ambulance staff-approval waiver: records who zeroed the fare and when.
-- Follows the same pattern as support_tickets.refund_processed_by/_at —
-- plain TEXT email, no FK to users(id) (panel accounts aren't rows in `users`).
ALTER TABLE bookings
  ADD COLUMN IF NOT EXISTS waived_by TEXT,
  ADD COLUMN IF NOT EXISTS waived_at TIMESTAMPTZ;
