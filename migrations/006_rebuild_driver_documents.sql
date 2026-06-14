-- ============================================================
-- Migration 006 — Rebuild driver_documents with the correct schema
--
-- The original table (migration 002) used columns `type` / `url`.
-- All current code expects `doc_type`, `file_url`, `file_name`, etc.
-- Migration 004's CREATE TABLE IF NOT EXISTS skipped because the old
-- table already existed, so we drop and recreate here.
--
-- SAFE: document upload has never worked, so this table holds no real
-- data. CASCADE clears the stale constraints too.
-- ============================================================

DROP TABLE IF EXISTS driver_documents CASCADE;

CREATE TABLE driver_documents (
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

CREATE INDEX idx_driver_documents_driver_id ON driver_documents(driver_id);
CREATE INDEX idx_driver_documents_status ON driver_documents(status);

-- Verification-tracking columns on drivers (idempotent).
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS docs_submitted   BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS docs_verified_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS rejection_reason TEXT;
