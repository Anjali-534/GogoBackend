-- Migration 034 — Bogie Tracker: email OTP verification at signup.
--
-- Signup now only collects name/email/password (migration 033 made
-- contact_phone optional for the same reason). Before a company can log
-- in, it must prove ownership of contact_email via a 6-digit OTP emailed
-- through the existing Resend integration (see sendTrackerSignupEmail).
-- Only one live OTP per company at a time — inline columns rather than a
-- separate table, mirroring the ride_otp precedent (migration 010).
--
-- Verification gate runs before the existing status (pending/active/
-- rejected/suspended) switch in TrackerCompanyLogin: a company can't even
-- be usefully "pending staff approval" until it's proven the email is real.

ALTER TABLE tracker_companies
  ADD COLUMN IF NOT EXISTS email_verified       BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS email_otp_code        TEXT,
  ADD COLUMN IF NOT EXISTS email_otp_expires_at  TIMESTAMPTZ;
