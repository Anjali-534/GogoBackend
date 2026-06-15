-- Migration 010 — Ride OTP
-- A 4-digit OTP is generated at booking creation.
-- The rider shows it to the driver, who enters it to start the trip.
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS ride_otp TEXT;
