-- Migration 015 — Booking source tracking (mobile app vs website)

ALTER TABLE bookings
  ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'app';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'bookings_source_check'
        AND conrelid = 'bookings'::regclass
    ) THEN
        ALTER TABLE bookings DROP CONSTRAINT bookings_source_check;
    END IF;
EXCEPTION WHEN OTHERS THEN
    NULL;
END $$;

ALTER TABLE bookings
    ADD CONSTRAINT bookings_source_check
    CHECK (source IN ('app', 'website'));
