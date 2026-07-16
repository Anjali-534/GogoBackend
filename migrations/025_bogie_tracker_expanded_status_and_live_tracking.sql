-- Migration 025 — Bogie Tracker: expanded status flow + live driver location tracking.
--
-- Part A: widen the status CHECK constraints on tracker_orders and
-- tracker_order_events to insert 'loading' and 'loaded' between 'created'
-- and 'dispatched'. Full sequence becomes:
--   created -> loading -> loaded -> dispatched -> in_transit -> delivered
-- (+ 'cancelled' remains reachable from any non-terminal status).
-- Terminal statuses stay 'delivered' and 'cancelled' — no change needed
-- there, both are already in the allowed set.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tracker_orders_status_check'
        AND conrelid = 'tracker_orders'::regclass
    ) THEN
        ALTER TABLE tracker_orders DROP CONSTRAINT tracker_orders_status_check;
    END IF;
EXCEPTION WHEN OTHERS THEN
    NULL;
END $$;

ALTER TABLE tracker_orders
    ADD CONSTRAINT tracker_orders_status_check
    CHECK (status IN ('created', 'loading', 'loaded', 'dispatched', 'in_transit', 'delivered', 'cancelled'));

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tracker_order_events_status_check'
        AND conrelid = 'tracker_order_events'::regclass
    ) THEN
        ALTER TABLE tracker_order_events DROP CONSTRAINT tracker_order_events_status_check;
    END IF;
EXCEPTION WHEN OTHERS THEN
    NULL;
END $$;

ALTER TABLE tracker_order_events
    ADD CONSTRAINT tracker_order_events_status_check
    CHECK (status IN ('created', 'loading', 'loaded', 'dispatched', 'in_transit', 'delivered', 'cancelled'));


-- Part B: live driver location tracking.
--
-- driver_tracking_token is a separate crypto-random token from
-- public_tracking_token (the customer-facing one) — it identifies the
-- DRIVER's share-link session, generated server-side the first time an
-- order's status moves to 'dispatched'. Same unguessability-only security
-- model as public_tracking_token (see tracker.go's generateTrackingToken).

ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS driver_tracking_token TEXT,
    ADD COLUMN IF NOT EXISTS last_lat               DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS last_lng               DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS last_location_at        TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tracker_orders_driver_tracking_token_key'
        AND conrelid = 'tracker_orders'::regclass
    ) THEN
        ALTER TABLE tracker_orders
            ADD CONSTRAINT tracker_orders_driver_tracking_token_key UNIQUE (driver_tracking_token);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_tracker_orders_driver_tracking_token
    ON tracker_orders(driver_tracking_token);

-- Route trail: every location ping the driver's browser sends, kept for
-- drawing the polyline on the map. Not deduped/downsampled here — done
-- purely by the ~15s client-side POST interval.

CREATE TABLE IF NOT EXISTS tracker_location_pings (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id    UUID NOT NULL REFERENCES tracker_orders(id) ON DELETE CASCADE,
    lat         DOUBLE PRECISION NOT NULL,
    lng         DOUBLE PRECISION NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tracker_location_pings_order_id_created_at
    ON tracker_location_pings(order_id, created_at);
