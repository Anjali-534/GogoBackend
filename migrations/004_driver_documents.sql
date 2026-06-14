-- ============================================================
-- Migration 004 — Driver document uploads
-- ============================================================

CREATE TABLE IF NOT EXISTS driver_documents (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  driver_id     UUID NOT NULL REFERENCES drivers(id) ON DELETE CASCADE,
  doc_type      TEXT NOT NULL CHECK (doc_type IN (
                  'passport_photo',
                  'aadhaar_front', 'aadhaar_back',
                  'pan_card',
                  'driving_license_front', 'driving_license_back',
                  'rc_front', 'rc_back',
                  'insurance',
                  'pollution_cert',
                  'fitness_cert',
                  'permit',
                  'gst_cert',
                  'emt_cert',
                  'goods_insurance',
                  'bank_passbook',
                  'vehicle_photo_front',
                  'vehicle_photo_side'
                )),
  file_url      TEXT NOT NULL,
  file_name     TEXT,
  file_size     INT,
  mime_type     TEXT,
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

-- Track overall document verification status on drivers table
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS docs_submitted     BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS docs_verified_at   TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS rejection_reason   TEXT;
