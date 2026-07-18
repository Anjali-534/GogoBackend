-- Migration 035 — Bogie Tracker: license key for auto-activation on payment.
--
-- New flow: staff marking a plan order as paid is now the activation trigger
-- (see MarkTrackerPlanOrderPaid) — it generates a license key and a fresh
-- system password for the company on the pending -> active transition, then
-- emails both to the company. license_key is nullable since it's only
-- populated at that point, never at signup.

ALTER TABLE tracker_companies
  ADD COLUMN IF NOT EXISTS license_key TEXT UNIQUE;
