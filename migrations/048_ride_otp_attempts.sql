-- Per-booking OTP attempt tracking, so VerifyRideOTP can lock out
-- brute-force guessing of the 4-digit ride-start code instead of allowing
-- unlimited attempts.
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS otp_attempts INT NOT NULL DEFAULT 0;
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS otp_locked_until TIMESTAMPTZ;
