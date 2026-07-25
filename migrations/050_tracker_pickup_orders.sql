-- Migration 050 — Bogie Tracker: inbound orders (goods received from a
-- supplier), alongside the existing outbound orders (goods sent out).
--
-- order_type: existing rows are all outbound dispatches, hence the
-- 'outbound' default/backfill. 'inbound' orders reuse booked_for_* as the
-- supplier's details (no new schema — approved reuse) and are the only
-- ones eligible for the staff-side mark-received action.
--
-- default_address / default_address_lat / default_address_lng: the
-- company's own address, used to prefill the destination on inbound orders
-- (goods flow back to the company) the same way dispatch_from/dispatch_to
-- free-text + lat/lng already work on tracker_orders (migration 026).
-- Nullable — existing companies have none set.

ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS order_type TEXT NOT NULL DEFAULT 'outbound'
        CHECK (order_type IN ('outbound', 'inbound'));

ALTER TABLE tracker_companies
    ADD COLUMN IF NOT EXISTS default_address     TEXT,
    ADD COLUMN IF NOT EXISTS default_address_lat DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS default_address_lng DOUBLE PRECISION;
