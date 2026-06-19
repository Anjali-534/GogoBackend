-- 011_driver_wallet.sql
-- Add wallet and ledger columns to drivers + driver_earnings

ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS wallet_balance          DECIMAL(12,2) DEFAULT -700.00,
  ADD COLUMN IF NOT EXISTS registration_fee_paid   BOOLEAN       DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS registration_fee_paid_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS is_wallet_blocked       BOOLEAN       DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS wallet_blocked_at       TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS wallet_blocked_reason   TEXT;

ALTER TABLE driver_earnings
  ADD COLUMN IF NOT EXISTS debit_type  TEXT,
  ADD COLUMN IF NOT EXISTS is_debit    BOOLEAN DEFAULT FALSE;

-- Backfill registration fee entry for all existing drivers
INSERT INTO driver_earnings (
  id, driver_id, amount, type, description,
  is_debit, debit_type, created_at
)
SELECT
  uuid_generate_v4(),
  d.id,
  700.00,
  'adjustment',
  'One-time registration fee — gogoo onboarding',
  true,
  'registration_fee',
  d.created_at
FROM drivers d
WHERE NOT EXISTS (
  SELECT 1 FROM driver_earnings de
  WHERE de.driver_id = d.id
  AND de.debit_type = 'registration_fee'
);
