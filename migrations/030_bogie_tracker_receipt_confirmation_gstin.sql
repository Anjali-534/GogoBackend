-- Migration 030 — Bogie Tracker: consignee goods-received confirmation,
-- and GSTIN fields on the order form (booked-for / consignee).
--
-- Part A: receipt confirmation. A dedicated, unguessable token (same
-- generator pattern as public_tracking_token / driver_tracking_token) is
-- created the first time an order reaches 'delivered'. The consignee (and
-- booked_for, via the dispatch email) uses it to confirm goods were
-- received in proper condition — a one-way, idempotent action, separate
-- from the general public tracking link so it isn't exposed to anyone the
-- tracking link gets forwarded to.

ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS received_confirmed_at        TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS received_confirmation_token  TEXT UNIQUE;

CREATE INDEX IF NOT EXISTS idx_tracker_orders_received_confirmation_token
    ON tracker_orders(received_confirmation_token);


-- Part B: allow the confirmation event to be recorded as reported_by =
-- 'consignee', alongside the existing 'company' / 'driver' values from
-- migration 028. DROP + re-ADD instead of an IF-NOT-EXISTS guard so the
-- migration is still safely re-runnable and always leaves the constraint
-- in the correct final state.

ALTER TABLE tracker_order_events
    DROP CONSTRAINT IF EXISTS tracker_order_events_reported_by_check;

ALTER TABLE tracker_order_events
    ADD CONSTRAINT tracker_order_events_reported_by_check
    CHECK (reported_by IN ('company', 'driver', 'consignee'));


-- Part C: GSTIN fields for the two other parties on the dispatch sheet.
-- Both optional; format/checksum validated client-side by the GSTInput
-- component, never enforced server-side.

ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS consignee_gstin   TEXT,
    ADD COLUMN IF NOT EXISTS booked_for_gstin  TEXT;
