-- Migration 060 — Driver account deletion + ban-evasion Aadhaar hash
--
-- Backs DELETE /driver/account and POST /driver/account/delete-check.
-- drivers.phone is relaxed to nullable so it can be scrubbed immediately
-- on deletion (same idiom 059_rider_account_deletion.sql used for
-- riders.phone) while driver_documents, bank/UPI details, and earnings
-- stay intact — driver_documents for Motor Vehicles Act / RTO document
-- retention, bank/UPI + earnings for the same 3-year GST window as
-- bookings, both already promised in the privacy policy. users.is_active
-- / deleted_at already exist from migration 059 and are reused as-is.
--
-- driver_aadhaar_hashes is a ban-evasion detection table, deliberately
-- decoupled from every other driver PII table (driver_documents,
-- drivers, users) so it survives independent of the rest of the
-- deletion/retention lifecycle. It stores only a one-way SHA-256 hash
-- of a driver's Aadhaar number (Aadhaar number + AADHAAR_HASH_PEPPER env
-- var, never the number itself) — there is no reverse-lookup path back
-- to the original number. is_banned records whether the account was
-- banned (drivers.is_blocked=true) at the moment of deletion; only a
-- banned account's hash blocks a future re-signup attempt.

ALTER TABLE drivers ALTER COLUMN phone DROP NOT NULL;

CREATE TABLE IF NOT EXISTS driver_aadhaar_hashes (
  id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  aadhaar_hash TEXT NOT NULL,
  is_banned    BOOLEAN NOT NULL DEFAULT FALSE,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_driver_aadhaar_hashes_hash ON driver_aadhaar_hashes (aadhaar_hash);
