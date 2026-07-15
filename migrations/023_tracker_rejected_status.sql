-- Migration 023 — Add 'rejected' as a valid tracker_companies.status value,
-- so the dashboard admin approve/reject/suspend workflow has a distinct
-- terminal state for rejected signups (separate from 'suspended', which
-- implies the company was active before being cut off).

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tracker_companies_status_check'
        AND conrelid = 'tracker_companies'::regclass
    ) THEN
        ALTER TABLE tracker_companies DROP CONSTRAINT tracker_companies_status_check;
    END IF;
EXCEPTION WHEN OTHERS THEN
    NULL;
END $$;

ALTER TABLE tracker_companies
    ADD CONSTRAINT tracker_companies_status_check
    CHECK (status IN ('pending', 'active', 'rejected', 'suspended'));
