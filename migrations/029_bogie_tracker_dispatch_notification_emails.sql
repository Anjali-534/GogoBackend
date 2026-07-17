-- Migration 029 — Bogie Tracker: dispatch notification emails.
--
-- Lets a company send the traditional dispatch-details summary (party /
-- consignee / material / qty / truck / driver / transporter / date /
-- documents, plus the public tracking link) to the order's stakeholders
-- straight from the order detail page.

-- notification_email is the company's "send as" reply-to address for
-- dispatch emails. Nullable — falls back to contact_email in application
-- code when unset, so existing companies need no backfill.
ALTER TABLE tracker_companies
    ADD COLUMN IF NOT EXISTS notification_email TEXT;

-- Per-order stakeholder emails, all optional. The driver has no email field
-- by design — drivers get the WhatsApp tracking link instead, not email.
ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS booked_for_email  TEXT,
    ADD COLUMN IF NOT EXISTS consignee_email   TEXT,
    ADD COLUMN IF NOT EXISTS transporter_email TEXT;
