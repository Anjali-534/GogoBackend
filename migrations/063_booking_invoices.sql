-- Post-ride invoices, phase 1: persist the fare breakdown at booking
-- creation, and give each completed, non-zero-fare booking one invoice
-- number at completion time.
--
-- Fare breakdown (written once by createBookingCore, never recomputed):
--   trip_fare                 server-side fare BEFORE promo discount
--                             (base + distance, cab-variant multiplier,
--                             truck loading/unloading addons)
--   discount_amount           promo discount actually applied
--   promo_code                the promo code that produced it, if any
--   carried_cancellation_fee  outstanding fee from an earlier ride that was
--                             folded into this booking's fare
-- so that estimated_fare = trip_fare - discount_amount + carried_cancellation_fee.
-- Bookings created before this migration keep trip_fare NULL.
--
-- invoice_number is BGR/<FY>/<NNNNN> (e.g. BGR/2627/00001 for FY 2026-27),
-- allocated gap-free per financial year by assignBookingInvoiceNumber under
-- a transaction-scoped advisory lock (same mechanism as tracker plan
-- invoices). 14 chars, so TEXT is plenty and it stays under GST's 16-char
-- limit. Set once, never regenerated.
--
-- The unique index uses text_pattern_ops so the allocator's
-- `invoice_number LIKE 'BGR/2627/%'` count can use it, while still
-- enforcing uniqueness as a last-line guard against a duplicate number.
--
-- discount_amount is a documented no-op: migration 002 already created it
-- (DECIMAL(10,2) DEFAULT 0, nullable), so IF NOT EXISTS skips it and no NOT
-- NULL constraint applies. It stays nullable; createBookingCore writes an
-- explicit value (0 when no promo) on every insert. Listed here, matching
-- 002's definition, so this file documents the full fare-breakdown schema.
--
-- promo_code is likewise a documented no-op: migration 002 already created
-- it with the same TEXT type, so IF NOT EXISTS skips it.
ALTER TABLE bookings
  ADD COLUMN IF NOT EXISTS trip_fare                NUMERIC(10,2),
  ADD COLUMN IF NOT EXISTS discount_amount          NUMERIC(10,2) DEFAULT 0,
  ADD COLUMN IF NOT EXISTS promo_code               TEXT,
  ADD COLUMN IF NOT EXISTS carried_cancellation_fee NUMERIC(10,2) NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS invoice_number           TEXT,
  ADD COLUMN IF NOT EXISTS invoice_issued_at        TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS idx_bookings_invoice_number
  ON bookings (invoice_number text_pattern_ops)
  WHERE invoice_number IS NOT NULL;
