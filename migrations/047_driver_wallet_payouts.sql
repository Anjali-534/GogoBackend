-- 047_driver_wallet_payouts.sql
-- Extend driver_earnings (already the driver ledger) to support
-- Razorpay top-ups and RazorpayX payouts, instead of introducing a
-- parallel driver_wallet_ledger table.

ALTER TABLE driver_earnings
  ADD COLUMN IF NOT EXISTS razorpay_payment_id  TEXT,
  ADD COLUMN IF NOT EXISTS razorpayx_payout_id  TEXT,
  ADD COLUMN IF NOT EXISTS status                TEXT NOT NULL DEFAULT 'completed';

-- Every row written before this migration is a settled, already-applied
-- balance movement (ride credit, commission, registration fee, referral) —
-- the DEFAULT above backfills them all to 'completed', so existing history
-- (statement PDFs, ledger listing) is unaffected.

-- Uniqueness only where the id is actually present, so the vast majority
-- of rows (which have neither) are unrestricted.
CREATE UNIQUE INDEX IF NOT EXISTS driver_earnings_razorpay_payment_id_uq
  ON driver_earnings (razorpay_payment_id)
  WHERE razorpay_payment_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS driver_earnings_razorpayx_payout_id_uq
  ON driver_earnings (razorpayx_payout_id)
  WHERE razorpayx_payout_id IS NOT NULL;

-- status is a payout-lifecycle marker, not a "has this balance change
-- happened yet" flag — every row is still written only once its amount is
-- already applied to drivers.wallet_balance (see wallet.go's rider pattern
-- and the driver withdraw/payout-webhook handlers in Phase 2). Existing
-- SUM-based queries (ledger.BuildStatement, GetDriverLedger) therefore need
-- no WHERE status='completed' filter.
