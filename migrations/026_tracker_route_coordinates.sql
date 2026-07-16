-- Migration 026 — Bogie Tracker: optional lat/lng for the dispatch_from and
-- dispatch_to addresses, captured when the company picks an address from
-- Ola Places autocomplete (or "use current location") on the New Dispatch
-- form. Nullable — manual free-text addresses (no suggestion picked) and
-- all pre-existing orders have no coordinates, and nothing should require
-- them.

ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS dispatch_from_lat DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS dispatch_from_lng DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS dispatch_to_lat   DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS dispatch_to_lng   DOUBLE PRECISION;
