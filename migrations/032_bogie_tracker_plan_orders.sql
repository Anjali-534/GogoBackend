-- Migration 032 — Bogie Tracker: plan/subscription billing orders.
--
-- Named tracker_plan_orders (not tracker_orders — that table already means
-- shipment dispatch orders, migration 022). A company picks a pricing plan
-- + duration, submits billing details, and an order is created as
-- 'pending_payment'. No payment gateway is wired up yet (see payment_gateway_ref
-- below) — a staff member confirms payment was received out-of-band and
-- marks the order paid, which generates the invoice number/PDF and emails
-- it. When a gateway is added later, its webhook can call the same
-- mark-paid path instead of a staff click, with no schema change needed.
--
-- Plan/duration pricing is looked up server-side from a fixed table mirroring
-- app/bogie-tracker/TrackerPricing.tsx on the marketing site — base_amount/
-- gst_amount/total_amount are computed and stored at order-creation time, not
-- trusted from the client, so a later price change doesn't alter historical
-- orders and a tampered request can't set its own price.

CREATE TABLE IF NOT EXISTS tracker_plan_orders (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id            UUID NOT NULL REFERENCES tracker_companies(id),

  plan                  TEXT NOT NULL
                          CHECK (plan IN ('single','2users','5users','mega','lifetime')),
  billing_duration      TEXT NOT NULL
                          CHECK (billing_duration IN ('monthly','quarterly','halfYearly','yearly','onetime')),

  base_amount           DECIMAL(10,2) NOT NULL,
  gst_amount            DECIMAL(10,2) NOT NULL,
  total_amount          DECIMAL(10,2) NOT NULL,

  billing_name          TEXT NOT NULL,
  billing_address_line  TEXT NOT NULL,
  billing_city          TEXT NOT NULL,
  billing_state         TEXT NOT NULL,
  billing_pincode       TEXT NOT NULL,
  gstin                 TEXT,

  invoice_number        TEXT UNIQUE,
  status                TEXT NOT NULL DEFAULT 'pending_payment'
                          CHECK (status IN ('pending_payment','paid','cancelled')),
  payment_gateway_ref   TEXT,

  created_at            TIMESTAMPTZ DEFAULT NOW(),
  paid_at               TIMESTAMPTZ,
  updated_at            TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tracker_plan_orders_company_id ON tracker_plan_orders(company_id);
CREATE INDEX IF NOT EXISTS idx_tracker_plan_orders_status ON tracker_plan_orders(status);
