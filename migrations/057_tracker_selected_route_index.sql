-- Migration 057 — Live-route redesign: driver-selected route index
--
-- Backs PostTrackerDriverSelectedRoute / GetTrackerDriverLiveRoute
-- (tracker.go): the driver taps a route option on their own map, the
-- choice is persisted here, and the company panel shows a read-only
-- "Driver is on Route X" display instead of its own chip selector.
--
-- tracker_orders is where selection is actually read/written (route
-- choice is per-destination — see resolveDriverActiveOrder's comment in
-- tracker.go). tracker_trips gets the same column for schema symmetry
-- with the rest of the trip/order pairing (last_lat/last_lng, etc.) but
-- is intentionally left unused by the current design.
--
-- Null means no explicit selection yet — both driver and company sides
-- default to index 0 (the first/recommended route).

ALTER TABLE tracker_orders ADD COLUMN IF NOT EXISTS selected_route_index INT;
ALTER TABLE tracker_trips ADD COLUMN IF NOT EXISTS selected_route_index INT;
