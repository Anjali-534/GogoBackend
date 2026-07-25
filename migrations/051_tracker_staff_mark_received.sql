-- Migration 051 — Bogie Tracker: allow tracker_order_events.reported_by to
-- record 'staff' (the new POST /tracker/orders/:id/mark-received action),
-- alongside the existing 'company' | 'driver' | 'consignee' values from
-- migrations 028 and 030.

ALTER TABLE tracker_order_events
    DROP CONSTRAINT IF EXISTS tracker_order_events_reported_by_check;

ALTER TABLE tracker_order_events
    ADD CONSTRAINT tracker_order_events_reported_by_check
    CHECK (reported_by IN ('company', 'driver', 'consignee', 'staff'));
