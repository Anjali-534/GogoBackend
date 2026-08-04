-- Migration 055 — Bogie Tracker: multi-drop / part-shipment trips.
--
-- One truck+driver can now deliver to multiple consignees in sequence
-- (Porter-style multi-stop) instead of the existing strict 1 order = 1
-- truck = 1 pickup = 1 drop model. tracker_trips is the new parent — each
-- stop is still a normal tracker_orders row, now optionally linked via
-- trip_id + stop_sequence.
--
-- trip_id IS NULL (the default for every existing row, and every future
-- single-shipment order nobody adds a stop to) means "unaffected, behaves
-- exactly as before" — this migration only adds nullable columns to
-- tracker_orders plus a wholly new table, so it's a zero-downtime,
-- backward-compatible change with no data migration required.
--
-- driver_tracking_token / last_lat / last_lng / last_location_at on
-- tracker_trips mirror the same columns already on tracker_orders
-- (migration 025) — for a multi-stop trip, THESE are the live-location
-- authority the driver's one shared tracking link writes to, not any
-- individual stop's order (see PostTrackerDriverLocation). Legacy orders
-- (trip_id IS NULL) keep using their own order-level columns, unchanged.

CREATE TABLE IF NOT EXISTS tracker_trips (
  id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id             UUID NOT NULL REFERENCES tracker_companies(id),
  dispatch_from          TEXT NOT NULL,
  vehicle_number         TEXT NOT NULL,
  driver_id              UUID REFERENCES tracker_drivers(id) ON DELETE SET NULL,
  driver_name            TEXT,
  driver_phone           TEXT,
  overall_status         TEXT NOT NULL DEFAULT 'created'
                           CHECK (overall_status IN ('created','in_transit','completed','cancelled')),
  driver_tracking_token  TEXT UNIQUE,
  last_lat               DOUBLE PRECISION,
  last_lng               DOUBLE PRECISION,
  last_location_at       TIMESTAMPTZ,
  public_tracking_token  TEXT NOT NULL UNIQUE,
  created_at             TIMESTAMPTZ DEFAULT NOW(),
  completed_at           TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_tracker_trips_company_id ON tracker_trips(company_id);
CREATE INDEX IF NOT EXISTS idx_tracker_trips_public_tracking_token ON tracker_trips(public_tracking_token);
CREATE INDEX IF NOT EXISTS idx_tracker_trips_driver_tracking_token ON tracker_trips(driver_tracking_token);

-- trip_id: NULL for every row until a company explicitly adds a second
-- stop to an order. stop_sequence: defaults to 1 so a standalone order
-- (trip_id IS NULL) and a trip's first stop both read the same way.
ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS trip_id UUID REFERENCES tracker_trips(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS stop_sequence INTEGER NOT NULL DEFAULT 1;

CREATE INDEX IF NOT EXISTS idx_tracker_orders_trip_id ON tracker_orders(trip_id);
