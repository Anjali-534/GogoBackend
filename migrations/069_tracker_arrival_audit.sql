-- Arrival audit trail for the tracker drive page (driver_tracking_token).
--
-- Every driver 'reached' / 'delivery_claimed' event now records where the
-- driver's phone said it was at that moment: coordinates, reported GPS
-- accuracy and straight-line distance to the stop's drop point.
-- outside_radius marks an "I'm at the drop location" override taken from
-- outside TRACKER_ARRIVAL_RADIUS_M; no_location marks an event sent with no
-- coordinates at all (older open pages, GPS off).
--
-- tracker_orders gets the per-stop summary the company panel badges on:
-- reached_at (also lets the drive page skip a duplicate 'reached' after a
-- reload), the two flags, the distance of the first outside-radius event
-- and the driver's reason. Trip stops are tracker_orders rows, so this is
-- per stop, not per trip.

ALTER TABLE tracker_order_events
    ADD COLUMN IF NOT EXISTS lat            DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS lng            DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS accuracy_m     DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS distance_m     DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS outside_radius BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS no_location    BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS reached_at              TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS arrived_outside_radius  BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS arrival_no_location     BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS arrival_distance_m      DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS arrival_override_reason TEXT;

-- Backfill reached_at from 'reached' events recorded before this migration,
-- so a driver page reopened mid-delivery doesn't post a second one.
-- Idempotent: only fills rows still NULL.
UPDATE tracker_orders o
SET reached_at = e.first_reached
FROM (
    SELECT order_id, MIN(created_at) AS first_reached
    FROM tracker_order_events
    WHERE event_kind = 'reached'
    GROUP BY order_id
) e
WHERE o.id = e.order_id AND o.reached_at IS NULL;
