-- Migration 052 — Bogie Tracker: company wallet + auto-billed subscription,
-- replacing the manual staff-marked-paid plan-orders flow (migration 032)
-- as the source of truth for "is this company's subscription current."
--
-- current_plan (migration 038) is kept — it still names the tier/price the
-- company is on. subscription_expires_at/suspension_reason (migration 036)
-- are left in place for history but a follow-up Go change stops writing to
-- them once wallet billing is live; subscription_status/next_billing_date
-- below take over that role. tracker_plan_orders (032) remains queryable
-- for old invoices but stops being the thing that extends a subscription.
--
-- wallet_balance: cached/derived, same idiom as riders.wallet_balance
-- (migration 046) — tracker_wallet_ledger below is the source of truth,
-- recomputed on every ledger write, never a bare UPDATE outside that path.
--
-- subscription_amount: nullable — a per-company override an admin can set;
-- when NULL the daily charge job falls back to a global default amount in
-- code (no schema needed for that default).
--
-- subscription_status: 'active' (billing normally), 'overdue' (a charge
-- attempt failed — wallet balance insufficient), 'paused' (company or
-- admin explicitly paused auto-billing). Nothing here auto-suspends access;
-- that policy decision is Go-code-level, same as today.
--
-- next_billing_date: nullable until a company is first activated onto
-- wallet billing. Backfilled below from subscription_expires_at so
-- companies already mid-cycle aren't charged early or late on cutover.

ALTER TABLE tracker_companies
    ADD COLUMN IF NOT EXISTS wallet_balance      DECIMAL(12,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS subscription_amount DECIMAL(10,2),
    ADD COLUMN IF NOT EXISTS subscription_status TEXT NOT NULL DEFAULT 'active'
        CHECK (subscription_status IN ('active', 'overdue', 'paused')),
    ADD COLUMN IF NOT EXISTS next_billing_date   DATE;

UPDATE tracker_companies
SET next_billing_date = subscription_expires_at::date
WHERE subscription_expires_at IS NOT NULL
  AND next_billing_date IS NULL;

UPDATE tracker_companies
SET subscription_status = 'paused'
WHERE suspension_reason IS NOT NULL
  AND subscription_status = 'active';

-- tracker_wallet_ledger mirrors wallet_ledger's shape (migration 046) —
-- every balance change (topup, subscription charge, manual adjustment)
-- gets a row here; razorpay_payment_id's partial unique index is the
-- webhook-idempotency guard, same pattern as the rider ledger.
CREATE TABLE IF NOT EXISTS tracker_wallet_ledger (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id          UUID NOT NULL REFERENCES tracker_companies(id),
    type                TEXT NOT NULL
        CHECK (type IN ('topup', 'subscription_charge', 'adjustment')),
    amount              DECIMAL(10,2) NOT NULL,
    balance_after       DECIMAL(10,2) NOT NULL,
    razorpay_payment_id TEXT,
    status              TEXT NOT NULL DEFAULT 'completed'
        CHECK (status IN ('pending', 'completed', 'failed')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tracker_wallet_ledger_company_id
    ON tracker_wallet_ledger(company_id);

CREATE INDEX IF NOT EXISTS idx_tracker_wallet_ledger_created_at
    ON tracker_wallet_ledger(created_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_tracker_wallet_ledger_razorpay_payment_id
    ON tracker_wallet_ledger(razorpay_payment_id)
    WHERE razorpay_payment_id IS NOT NULL;

-- tracker_wallet_charge_attempts: dedup guard for the daily auto-charge job,
-- same idiom as tracker_delivery_reminders_sent (049) — keyed on
-- (company_id, billing_date) so a given day's charge attempt only ever
-- happens once, whether it succeeds or fails. No 7-day cap here (unlike
-- the delivery reminders): the job just retries on the next billing_date
-- it advances to, whether or not the previous attempt was paid.
CREATE TABLE IF NOT EXISTS tracker_wallet_charge_attempts (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id       UUID NOT NULL REFERENCES tracker_companies(id) ON DELETE CASCADE,
    billing_date     DATE NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('charged', 'failed')),
    amount           DECIMAL(10,2) NOT NULL,
    wallet_ledger_id UUID REFERENCES tracker_wallet_ledger(id),
    attempted_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, billing_date)
);

CREATE INDEX IF NOT EXISTS idx_tracker_wallet_charge_attempts_company_id
    ON tracker_wallet_charge_attempts(company_id);
