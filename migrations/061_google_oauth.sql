-- Migration 061 — Google Sign-In for the rider/website login (bogie.in)
--
-- Reuses the existing github_id/gitlab_id pattern: no separate
-- auth_provider column — whether an account is Google-authenticated is
-- derivable from google_id IS NOT NULL (password_hash was already nullable
-- since 001_init, so no change needed there).
--
-- Sign-in matches an existing account by email (users.email is already
-- UNIQUE, so there's never more than one row to match) and links google_id
-- onto it rather than creating a duplicate account.

ALTER TABLE users ADD COLUMN IF NOT EXISTS google_id TEXT UNIQUE;
