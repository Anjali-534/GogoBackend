-- Migration 028 — Bogie Tracker: two-way driver link (quick-status events,
-- delivery signature, company -> driver messages).
--
-- Part A: tracker_order_events gets a reported_by flag and an event_kind
-- column. Driver quick-status taps (On Break / About to Reach / Reached /
-- Unloading / Delivered-claimed) are NOT status transitions — they're
-- notes attached to the order's CURRENT status, reported by the driver
-- instead of the company. event_kind carries which quick-status button was
-- pressed; it stays NULL for ordinary company-driven status-change events
-- (those are already described by the `status` column).

ALTER TABLE tracker_order_events
    ADD COLUMN IF NOT EXISTS reported_by TEXT NOT NULL DEFAULT 'company',
    ADD COLUMN IF NOT EXISTS event_kind  TEXT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tracker_order_events_reported_by_check'
        AND conrelid = 'tracker_order_events'::regclass
    ) THEN
        ALTER TABLE tracker_order_events
            ADD CONSTRAINT tracker_order_events_reported_by_check
            CHECK (reported_by IN ('company', 'driver'));
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tracker_order_events_event_kind_check'
        AND conrelid = 'tracker_order_events'::regclass
    ) THEN
        ALTER TABLE tracker_order_events
            ADD CONSTRAINT tracker_order_events_event_kind_check
            CHECK (event_kind IS NULL OR event_kind IN (
                'on_break', 'about_to_reach', 'reached', 'unloading', 'delivery_claimed'
            ));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_tracker_order_events_reported_by ON tracker_order_events(reported_by);


-- Part B: proof-of-delivery signature. Company still confirms the
-- 'delivered' status transition in the panel (prompted by a banner once a
-- delivery_claimed event + signature exist); the driver token only ever
-- writes this column, never the order status.

ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS signature_url TEXT;


-- Part C: company -> driver messages (one-way for v1; driver's reverse
-- channel is the quick-status events from Part A). Polled from the drive
-- page alongside its existing location/order poll — no push infra yet.

CREATE TABLE IF NOT EXISTS tracker_driver_messages (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id    UUID NOT NULL REFERENCES tracker_orders(id) ON DELETE CASCADE,
    body        TEXT NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    read_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_tracker_driver_messages_order_id ON tracker_driver_messages(order_id);
