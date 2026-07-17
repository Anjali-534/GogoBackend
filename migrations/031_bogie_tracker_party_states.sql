-- Migration 031 — Bogie Tracker: persist the booked-for/consignee state,
-- auto-filled client-side from GSTInput's resolved state code (see
-- lib/gstin.ts) but always a normal editable field — manual entry/override
-- always works, same as every other dispatch-sheet field.

ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS booked_for_state  TEXT,
    ADD COLUMN IF NOT EXISTS consignee_state   TEXT;
