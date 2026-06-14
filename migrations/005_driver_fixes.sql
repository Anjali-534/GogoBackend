-- ============================================================
-- Migration 005 — Driver onboarding fixes
--   1. Allow real-world vehicle_type values (logistics fleet)
--   2. Add bank / payout / GST columns used at signup
--   3. Widen driver_documents.doc_type to cover every category
-- ============================================================

-- 1) vehicle_type was locked to ride-hailing codes only. The logistics
--    platform sends free-form category labels, so drop the CHECK and keep
--    it as plain TEXT (validated on the client + via doc requirements).
ALTER TABLE drivers DROP CONSTRAINT IF EXISTS drivers_vehicle_type_check;

-- 2) Payout & business columns collected on the registration form.
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS bank_account_holder TEXT,
  ADD COLUMN IF NOT EXISTS bank_account_number TEXT,
  ADD COLUMN IF NOT EXISTS bank_ifsc           TEXT,
  ADD COLUMN IF NOT EXISTS bank_name           TEXT,
  ADD COLUMN IF NOT EXISTS upi_id              TEXT,
  ADD COLUMN IF NOT EXISTS gst_number          TEXT;

-- 3) driver_documents.doc_type CHECK rebuilt to match the app's doc ids.
--    (Older single-side ids like 'aadhaar' / 'driving_license' / 'rc' / 'puc'
--    are now accepted alongside the *_front / *_back variants.)
ALTER TABLE driver_documents DROP CONSTRAINT IF EXISTS driver_documents_doc_type_check;

ALTER TABLE driver_documents
  ADD CONSTRAINT driver_documents_doc_type_check CHECK (doc_type IN (
    'passport_photo',
    'aadhaar', 'aadhaar_front', 'aadhaar_back',
    'pan_card',
    'driving_license', 'driving_license_front', 'driving_license_back',
    'rc', 'rc_front', 'rc_back',
    'insurance',
    'puc', 'pollution_cert',
    'fitness_cert', 'fitness',
    'permit', 'national_permit',
    'gst_cert',
    'emt_cert',
    'goods_insurance',
    'bank_passbook',
    'vehicle_photo', 'vehicle_photo_front', 'vehicle_photo_side'
  ));

-- Store the document number / expiry the driver typed in the form.
ALTER TABLE driver_documents
  ADD COLUMN IF NOT EXISTS doc_number TEXT,
  ADD COLUMN IF NOT EXISTS expiry_date TEXT;
