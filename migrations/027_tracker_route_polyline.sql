-- Migration 027 — Bogie Tracker: planned-route cache on the order.
--
-- When an order has both dispatch_from and dispatch_to coordinates, the
-- backend fetches the driving route from Ola Directions ONCE (server-side
-- key, via the same upstream call ProxyOlaRoute uses) and stores the encoded
-- polyline + distance/duration here. All three tracking surfaces (company
-- order detail, public tracking page, driver share page) read these columns
-- instead of calling the directions API per page view — the public and
-- driver pages carry no JWT and couldn't call the authenticated /gogoo/route
-- proxy anyway.
--
-- Nullable: orders without both coordinate pairs (all pre-autocomplete
-- orders, and manually typed addresses) never get a route.

ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS route_polyline      TEXT,
    ADD COLUMN IF NOT EXISTS route_distance_km   DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS route_duration_mins INTEGER;
