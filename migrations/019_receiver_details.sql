-- Migration 019 — Receiver details for truck/parcel deliveries

ALTER TABLE bookings
  ADD COLUMN IF NOT EXISTS receiver_name  TEXT,
  ADD COLUMN IF NOT EXISTS receiver_phone TEXT;
