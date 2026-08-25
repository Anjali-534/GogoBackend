-- Migration 059 — Rider account deletion
--
-- Backs DELETE /rider/account. users gets is_active + deleted_at so a
-- deleted account's token is rejected on its very next request
-- (AuthMiddleware re-checks this, same idiom as RequireTrackerCompany's
-- live-status check) — there's no separate session/token-revocation store
-- in this codebase, so re-checking on every request is how "immediate"
-- cutoff is done here.
--
-- riders.phone is relaxed to nullable so it can be scrubbed immediately on
-- deletion while the underlying booking/payment rows stay intact for the
-- 3-year GST retention window already promised in the privacy policy.

ALTER TABLE users ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

ALTER TABLE riders ALTER COLUMN phone DROP NOT NULL;

CREATE INDEX IF NOT EXISTS idx_users_deleted_at ON users (deleted_at) WHERE deleted_at IS NOT NULL;
