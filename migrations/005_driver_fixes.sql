-- ============================================================
-- Migration 005 — Driver onboarding fixes
--   1. Allow real-world vehicle_type values (logistics fleet)
--   2. Add bank / payout / GST columns used at signup
--   3. Widen driver_documents.doc_type to cover every category
--
-- RunFileMigrations used to re-run every file on every boot, and failed
-- files are still retried on each boot, so both constraint changes below
-- are guarded to the schema they were written for. Unguarded, (1) would
-- drop the vehicle_type CHECK that 056 maintains, and (3) would replace
-- 016's doc_type CHECK with one that rejects police_clearance — failing
-- while any PCC row exists, and silently succeeding (with neither 016 nor
-- 056 left to repair it) on a boot where none does.
-- ============================================================

-- 1) vehicle_type was locked to ride-hailing codes only. The logistics
--    platform sends free-form category labels, so drop the CHECK and keep
--    it as plain TEXT (validated on the client + via doc requirements).
--    Only before 008 (no drivers.vehicle_category), which reinstates it.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = current_schema() AND table_name = 'drivers' AND column_name = 'vehicle_category') THEN
    ALTER TABLE drivers DROP CONSTRAINT IF EXISTS drivers_vehicle_type_check;
  END IF;
END $$;

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
--    Only on the new-shape table (has doc_type; 006 rebuilds the old one)
--    and only before 016 (no drivers.mvag_declaration_accepted), which
--    replaces this CHECK with one that also allows police_clearance.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = current_schema() AND table_name = 'driver_documents' AND column_name = 'doc_type')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = current_schema() AND table_name = 'drivers' AND column_name = 'mvag_declaration_accepted') THEN
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
  END IF;
END $$;

-- Store the document number / expiry the driver typed in the form.
ALTER TABLE driver_documents
  ADD COLUMN IF NOT EXISTS doc_number TEXT,
  ADD COLUMN IF NOT EXISTS expiry_date TEXT;
