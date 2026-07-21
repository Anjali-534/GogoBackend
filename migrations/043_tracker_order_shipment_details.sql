-- Migration 043 — Bogie Tracker: shipment-detail expansion (Phase 1).
--
-- Adds registered/factory addresses, a named contact person, priority,
-- expected delivery date, declared value, special-handling flags, and an
-- internal reference/PO number to orders — plus the matching fields on
-- saved recipients so picking a recipient pre-fills the fuller picture too
-- (shipment-specific fields like priority/declared_value stay order-only,
-- per the same split saved_recipients already draws for material/quantity).
--
-- CC/BCC is variable-length by nature (a company may list anywhere from
-- zero to many extra addresses per order), so it gets its own child table
-- rather than fixed columns — same reasoning as the upcoming
-- tracker_order_documents table in Phase 2.

ALTER TABLE tracker_orders
  ADD COLUMN IF NOT EXISTS registered_address       TEXT,
  ADD COLUMN IF NOT EXISTS factory_address           TEXT,
  ADD COLUMN IF NOT EXISTS contact_person_name       TEXT,
  ADD COLUMN IF NOT EXISTS contact_person_phone      TEXT,
  ADD COLUMN IF NOT EXISTS contact_person_email      TEXT,
  ADD COLUMN IF NOT EXISTS contact_person_designation TEXT,
  ADD COLUMN IF NOT EXISTS priority                  TEXT NOT NULL DEFAULT 'normal'
                                CHECK (priority IN ('normal','urgent','same_day')),
  ADD COLUMN IF NOT EXISTS expected_delivery_date    DATE,
  ADD COLUMN IF NOT EXISTS declared_value            NUMERIC,
  ADD COLUMN IF NOT EXISTS special_handling          TEXT[],
  ADD COLUMN IF NOT EXISTS internal_reference        TEXT;

CREATE TABLE IF NOT EXISTS tracker_order_cc_emails (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id    UUID NOT NULL REFERENCES tracker_orders(id) ON DELETE CASCADE,
  email       TEXT NOT NULL,
  kind        TEXT NOT NULL CHECK (kind IN ('cc','bcc')),
  created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tracker_order_cc_emails_order_id
  ON tracker_order_cc_emails(order_id);

ALTER TABLE tracker_saved_recipients
  ADD COLUMN IF NOT EXISTS registered_address        TEXT,
  ADD COLUMN IF NOT EXISTS factory_address            TEXT,
  ADD COLUMN IF NOT EXISTS contact_person_name        TEXT,
  ADD COLUMN IF NOT EXISTS contact_person_phone       TEXT,
  ADD COLUMN IF NOT EXISTS contact_person_email       TEXT,
  ADD COLUMN IF NOT EXISTS contact_person_designation TEXT;
