-- Migration 039 — Bogie Tracker: staff logins per company.
--
-- The original tracker_companies row (contact_email/password_hash) is the
-- implicit "owner" and stays exactly as-is — no data migrated out of it.
-- This table is purely additive: each row is one more login that shares the
-- owner's full access (no role/permission column — staff = owner-equivalent,
-- just not allowed to manage other staff logins, which is enforced in the
-- application layer via the token's is_owner claim, not here).
--
-- UNIQUE (company_id, email) is per-company, not global — two different
-- companies are allowed to each have a staff login with the same email.
-- Login dispatch handles the resulting ambiguity by checking password hashes
-- across all matching rows rather than assuming the email alone picks one.

CREATE TABLE IF NOT EXISTS tracker_staff_users (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id    UUID NOT NULL REFERENCES tracker_companies(id) ON DELETE CASCADE,
  email         TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  created_at    TIMESTAMPTZ DEFAULT NOW(),
  created_by    UUID NOT NULL REFERENCES tracker_companies(id),
  UNIQUE (company_id, email)
);
