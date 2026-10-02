-- ============================================================
-- Migration 006 — Rebuild driver_documents with the correct schema
--
-- The original table (migration 002) used columns `type` / `url`.
-- All current code expects `doc_type`, `file_url`, `file_name`, etc.
-- Migration 004's CREATE TABLE IF NOT EXISTS skipped because the old
-- table already existed, so we drop and recreate here.
--
-- RunFileMigrations re-runs every numbered file on every boot, so this
-- file must be idempotent. It used to DROP unconditionally, which wiped
-- every uploaded document row on each deploy. It now drops only the
-- original 002-era table (identified by its `type` column, with no
-- `doc_type`), and otherwise just creates the table if it's missing.
-- ============================================================

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = current_schema() AND table_name = 'driver_documents' AND column_name = 'type')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = current_schema() AND table_name = 'driver_documents' AND column_name = 'doc_type') THEN
    DROP TABLE driver_documents CASCADE;
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS driver_documents (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  driver_id     UUID NOT NULL REFERENCES drivers(id) ON DELETE CASCADE,
  doc_type      TEXT NOT NULL CHECK (doc_type IN (
                  'passport_photo',
                  'aadhaar', 'aadhaar_front', 'aadhaar_back',
                  'pan_card',
                  'driving_license', 'driving_license_front', 'driving_license_back',
                  'rc', 'rc_front', 'rc_back',
                  'insurance',
                  'puc', 'pollution_cert',
                  'fitness', 'fitness_cert',
                  'permit', 'national_permit',
                  'gst_cert',
                  'emt_cert',
                  'goods_insurance',
                  'bank_passbook',
                  'vehicle_photo', 'vehicle_photo_front', 'vehicle_photo_side'
                )),
  file_url      TEXT NOT NULL,
  file_name     TEXT,
  file_size     INT,
  mime_type     TEXT,
  doc_number    TEXT,
  expiry_date   TEXT,
  status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','approved','rejected')),
  reject_reason TEXT,
  reviewed_by   UUID REFERENCES users(id),
  reviewed_at   TIMESTAMPTZ,
  uploaded_at   TIMESTAMPTZ DEFAULT NOW(),
  updated_at    TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(driver_id, doc_type)
);

CREATE INDEX IF NOT EXISTS idx_driver_documents_driver_id ON driver_documents(driver_id);
CREATE INDEX IF NOT EXISTS idx_driver_documents_status ON driver_documents(status);

-- Verification-tracking columns on drivers (idempotent).
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS docs_submitted   BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS docs_verified_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS rejection_reason TEXT;
