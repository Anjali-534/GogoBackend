-- Driver automatic blocking system
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS is_blocked    BOOLEAN     DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS blocked_until TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS block_reason  TEXT;

-- Index for fast blocked-driver lookups
CREATE INDEX IF NOT EXISTS idx_drivers_is_blocked ON drivers (is_blocked) WHERE is_blocked = TRUE;
