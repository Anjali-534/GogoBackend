-- ============================================================
-- Migration 017 — Cancellation charges + Scheduled rides
-- Run in the Railway console before deploying the backend build
-- that depends on it.
-- ============================================================

-- ---- Feature 1: Cancellation charges ----------------------
-- accepted_at, cancelled_by, cancel_reason, cancelled_at already exist
-- (002_gogoo.sql). Only cancellation_fee is new; cancelled_by's CHECK
-- needs 'support' added since support-panel cancellations now record
-- who cancelled too.
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS cancellation_fee DECIMAL(10,2) DEFAULT 0;

ALTER TABLE bookings DROP CONSTRAINT IF EXISTS bookings_cancelled_by_check;
ALTER TABLE bookings ADD CONSTRAINT bookings_cancelled_by_check
  CHECK (cancelled_by IN ('rider','driver','support','system'));

ALTER TABLE riders ADD COLUMN IF NOT EXISTS outstanding_cancellation_fee DECIMAL(10,2) DEFAULT 0;

-- ---- Feature 2: Scheduled rides -----------------------------
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS is_scheduled BOOLEAN DEFAULT FALSE;
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS scheduled_at TIMESTAMPTZ;

ALTER TABLE bookings DROP CONSTRAINT IF EXISTS bookings_status_check;
ALTER TABLE bookings ADD CONSTRAINT bookings_status_check
  CHECK (status IN ('scheduled','searching','accepted','arriving','in_progress','completed','cancelled'));
