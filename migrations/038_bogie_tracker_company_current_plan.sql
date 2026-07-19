-- Migration 038 — Bogie Tracker: company's current subscription plan.
--
-- current_plan: NULL means the company has never paid (or pre-dates this
-- column) — CreateTrackerCompanyOrder treats that the same as an expired
-- subscription and blocks dispatch creation entirely. Set/updated by
-- MarkTrackerPlanOrderPaid alongside subscription_expires_at (see migration
-- 036) whenever a plan order is confirmed paid. Constrained to the same
-- five plans trackerbilling.Lookup recognizes.
--
-- Backfills migration 032's plan_orders billing history with a live "what
-- do they have right now" column — plan_orders alone can't answer that
-- cheaply since a company can have several paid orders (renewals, an
-- upgrade) over time.

ALTER TABLE tracker_companies
  ADD COLUMN IF NOT EXISTS current_plan TEXT
    CHECK (current_plan IN ('single','2users','5users','mega','lifetime'));
