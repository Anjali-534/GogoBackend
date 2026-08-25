-- Migration 058 — Live driver speed
--
-- Mirrors the driver's live GPS speed (km/h) onto the active booking,
-- same pattern as driver_heading from migration 007, so the rider's
-- poll (GET /bookings/:id) can show a live speed readout alongside the
-- existing distance/time pill.

ALTER TABLE bookings ADD COLUMN IF NOT EXISTS driver_speed DECIMAL(6,2);
