-- Migration 041 — Bogie Tracker: saved recipients ("bank beneficiaries" for
-- dispatch orders).
--
-- A recipient bundles the who/where side of an order — the Booked For party,
-- the Consignee party, and the Dispatch To destination — so a company doesn't
-- re-enter the same delivery contact on every order. Shipment-specific fields
-- (material, quantity, dispatch_datetime, documents, e-way bill, transporter,
-- driver, vehicle) are deliberately NOT part of a recipient: those are always
-- entered fresh per order.
--
-- Rows are company-scoped and shared across the whole company — owner and
-- every staff login see the same list (routes are gated by
-- RequireTrackerCompany only, not RequireTrackerOwner: this is operational
-- data, not administrative).
--
-- Orders never reference this table. Selecting a recipient just pre-fills the
-- order form client-side; the order stores plain field values as always, and
-- the optional saved_recipient_id on order creation only bumps
-- use_count/last_used_at (which power the most-used-first list ordering).

CREATE TABLE IF NOT EXISTS tracker_saved_recipients (
  id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id                UUID NOT NULL REFERENCES tracker_companies(id) ON DELETE CASCADE,

  -- Display label, e.g. "Reliance Warehouse - Gurgaon".
  label                     TEXT NOT NULL,

  -- Booked For party (matches tracker_orders columns of the same name).
  booked_for_company_name   TEXT NOT NULL,
  booked_for_phone          TEXT NOT NULL,
  booked_for_email          TEXT,
  booked_for_gstin          TEXT,
  booked_for_state          TEXT,

  -- Consignee party (receiving entity, if different from Booked For).
  consignee_name            TEXT,
  consignee_email           TEXT,
  consignee_gstin           TEXT,
  consignee_state           TEXT,

  -- Destination. Coordinates present only when the address was picked from
  -- Ola Places autocomplete (same convention as tracker_orders).
  dispatch_to               TEXT,
  dispatch_to_lat           DOUBLE PRECISION,
  dispatch_to_lng           DOUBLE PRECISION,

  -- Most-used-first ordering: bumped when an order is created with this
  -- recipient's saved_recipient_id.
  use_count                 INT NOT NULL DEFAULT 0,
  last_used_at              TIMESTAMPTZ,

  created_at                TIMESTAMPTZ DEFAULT NOW(),
  updated_at                TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tracker_saved_recipients_company_id
  ON tracker_saved_recipients(company_id);
