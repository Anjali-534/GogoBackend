-- 046_wallet_ledger.sql
-- Bogie Wallet — rider wallet ledger + booking payment method.
--
-- riders.wallet_balance already exists (added by MigrateReferrals) and
-- stays as-is: a cached/derived column, recomputed from this ledger on
-- every write. wallet_ledger is the source of truth going forward —
-- every balance change (topup, ride payment, refund, referral credit,
-- manual adjustment) gets a row here, never a bare UPDATE.
--
-- Idempotency: the partial unique index on razorpay_payment_id means a
-- duplicate webhook delivery for the same payment can't double-credit —
-- the second INSERT simply fails the unique constraint.

CREATE TABLE IF NOT EXISTS wallet_ledger (
  id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  rider_id            UUID NOT NULL REFERENCES riders(id),
  type                TEXT NOT NULL CHECK (type IN ('topup','ride_payment','refund','referral_credit','adjustment')),
  amount              DECIMAL(10,2) NOT NULL,  -- signed: positive = credit, negative = debit
  balance_after       DECIMAL(10,2) NOT NULL,
  razorpay_payment_id TEXT,
  booking_id          UUID REFERENCES bookings(id),
  status              TEXT NOT NULL DEFAULT 'completed' CHECK (status IN ('pending','completed','failed')),
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_wallet_ledger_rider_id   ON wallet_ledger(rider_id);
CREATE INDEX IF NOT EXISTS idx_wallet_ledger_booking_id ON wallet_ledger(booking_id);
CREATE INDEX IF NOT EXISTS idx_wallet_ledger_created_at ON wallet_ledger(created_at);

-- Prevents double-crediting the same Razorpay payment from a duplicate
-- webhook call. NULL is fine — most rows (ride_payment, refund,
-- referral_credit, adjustment) never have a razorpay_payment_id.
CREATE UNIQUE INDEX IF NOT EXISTS idx_wallet_ledger_razorpay_payment_id
  ON wallet_ledger(razorpay_payment_id) WHERE razorpay_payment_id IS NOT NULL;

-- Ride payment method. Defaults every existing/in-flight booking to
-- 'cash' — today's actual behavior — so this migration changes nothing
-- for bookings already in progress.
ALTER TABLE bookings
  ADD COLUMN IF NOT EXISTS payment_method TEXT NOT NULL DEFAULT 'cash'
    CHECK (payment_method IN ('cash','wallet'));
