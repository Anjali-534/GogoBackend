-- Migration 033 — Bogie Tracker: make contact_phone optional at signup.
--
-- Signup is being scoped down to name/email/password only (plus upcoming
-- email OTP verification, migration 034). Full company details — phone,
-- address, GSTIN — are now collected at checkout (the plan-order billing
-- form, migration 032) instead of at signup. contact_phone therefore can
-- no longer be guaranteed NOT NULL at insert time; it's backfilled later
-- via PATCH /gogoo/tracker/company/profile, which still requires it.
-- contact_email stays NOT NULL — it's the login identifier and OTP target.

ALTER TABLE tracker_companies ALTER COLUMN contact_phone DROP NOT NULL;
