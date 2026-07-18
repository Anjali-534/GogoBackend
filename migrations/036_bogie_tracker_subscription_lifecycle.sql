-- Migration 036 — Bogie Tracker: subscription lifecycle (expiry, renewal
-- reminders, auto-suspend on lapsed payment).
--
-- subscription_expires_at: NULL means "never expires" — true for lifetime
-- plans (which only ever pair with billing_duration='onetime', enforced by
-- trackerbilling.Lookup) and for companies never yet activated. Recurring
-- plans get this set/extended by MarkTrackerPlanOrderPaid on every paid
-- order: stacked on top of the existing value if it's still in the future
-- (early renewal doesn't lose remaining paid time), computed from NOW()
-- otherwise.
--
-- suspension_reason: distinguishes why a company is 'suspended'. The daily
-- auto-suspend job (see trackersub.StartSubscriptionLifecycleJob) sets this
-- to 'expired' when it suspends a company for lapsed payment. Manual staff
-- suspends leave it NULL. This lets MarkTrackerPlanOrderPaid auto-reactivate
-- a lapsed-and-now-paid company (suspension_reason='expired') without
-- requiring a staff Approve click, while a staff-cause suspension
-- (suspension_reason IS NULL) still requires one — payment alone must never
-- override a deliberate staff decision.
--
-- tracker_subscription_reminders_sent: dedup table for the 7-day/1-day
-- renewal reminder emails, mirroring sent_statements (ledger package) —
-- keyed on (company_id, expires_at, reminder_type) rather than a calendar
-- month, so a renewal that changes expires_at naturally re-opens reminder
-- eligibility for the new cycle with no cleanup logic needed.

ALTER TABLE tracker_companies
  ADD COLUMN IF NOT EXISTS subscription_expires_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS suspension_reason       TEXT;

CREATE TABLE IF NOT EXISTS tracker_subscription_reminders_sent (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id     UUID NOT NULL REFERENCES tracker_companies(id),
  expires_at     TIMESTAMPTZ NOT NULL,
  reminder_type  TEXT NOT NULL CHECK (reminder_type IN ('7_day', '1_day')),
  sent_at        TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE (company_id, expires_at, reminder_type)
);
