-- ============================================================
-- Migration 016 — MVAG 2020 compliance
--   • Police Clearance Certificate (PCC) as a required document
--   • MVAG self-declaration at driver signup
--   • Manual/BGV-API-ready background check status on drivers
-- Run this in the Railway console (or psql) before deploying the
-- backend build that depends on it.
-- ============================================================

-- Allow 'police_clearance' as an uploadable/reviewable document type.
ALTER TABLE driver_documents DROP CONSTRAINT IF EXISTS driver_documents_doc_type_check;
ALTER TABLE driver_documents ADD CONSTRAINT driver_documents_doc_type_check CHECK (doc_type IN (
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
                  'vehicle_photo', 'vehicle_photo_front', 'vehicle_photo_side',
                  'police_clearance'
                ));

-- MVAG self-declaration (Part 3)
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS
  mvag_declaration_accepted BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS
  mvag_declaration_at TIMESTAMPTZ;

-- Background check status — manual today, API-ready later (Part 4)
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS
  background_check_status TEXT DEFAULT 'pending',
  -- 'pending' | 'in_review' | 'clear' | 'flagged'
  ADD COLUMN IF NOT EXISTS
  background_check_notes TEXT,
  ADD COLUMN IF NOT EXISTS
  background_checked_by TEXT,
  ADD COLUMN IF NOT EXISTS
  background_checked_at TIMESTAMPTZ;
