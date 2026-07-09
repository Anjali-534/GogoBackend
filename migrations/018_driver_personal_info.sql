-- ============================================================
-- Migration 018 — Driver personal info (DOB + Address)
-- Run in the Railway console before deploying the backend build
-- that depends on it.
-- ============================================================

-- Both nullable — every driver who signed up before this migration
-- has neither, and the signup form fields are optional going forward.
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS date_of_birth DATE;
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS address TEXT;
