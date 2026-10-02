-- Free-text note on a driver_documents row, outside the review flow (not
-- shown to the driver, unlike reject_reason). First use: rows rebuilt from
-- Cloudinary by cmd/restore_driver_docs are marked
-- 'restored-from-cloudinary' after migration 006 used to wipe the table on
-- every boot.
ALTER TABLE driver_documents ADD COLUMN IF NOT EXISTS review_note TEXT;
