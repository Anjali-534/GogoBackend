-- Migration 037 — Bogie Tracker: optional company logo.
--
-- logo_url: NULL means the company hasn't uploaded a logo. Set by
-- POST /gogoo/tracker/logo (handlers.UploadTrackerCompanyLogo), which stores
-- the Cloudinary secure_url here. GET /gogoo/tracker/partners/public only
-- returns companies where this is non-null, so unset logos never show up as
-- broken/placeholder images on the marketing site's "Our Partners" section.

ALTER TABLE tracker_companies
  ADD COLUMN IF NOT EXISTS logo_url TEXT;
