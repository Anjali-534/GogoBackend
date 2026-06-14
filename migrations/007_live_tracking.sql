-- ============================================================
-- Migration 007 — Live tracking support
--   * Driver's live GPS stored on the driver row (current position)
--   * Booking carries a snapshot of the driver's live position so the
--     rider's poll (GET /bookings/:id) returns everything in one call
-- ============================================================

-- Driver's current live location (updated every few seconds while online).
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS current_lat        DECIMAL(10,8),
  ADD COLUMN IF NOT EXISTS current_lng        DECIMAL(11,8),
  ADD COLUMN IF NOT EXISTS location_updated_at TIMESTAMPTZ;

-- Live driver position mirrored onto the active booking, plus a heading
-- for smooth marker rotation on the rider's map.
ALTER TABLE bookings
  ADD COLUMN IF NOT EXISTS driver_lat        DECIMAL(10,8),
  ADD COLUMN IF NOT EXISTS driver_lng        DECIMAL(11,8),
  ADD COLUMN IF NOT EXISTS driver_heading    DECIMAL(6,2),
  ADD COLUMN IF NOT EXISTS driver_updated_at TIMESTAMPTZ;

-- Fast lookup of unassigned requests for the driver feed.
CREATE INDEX IF NOT EXISTS idx_bookings_searching
  ON bookings(status) WHERE status = 'searching';
