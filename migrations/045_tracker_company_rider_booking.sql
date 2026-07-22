-- Lets a Bogie Tracker company book rides (cab/truck/ambulance) through the
-- existing bookings pipeline, indistinguishable from a regular rider booking
-- to drivers or any other system component. Mechanism: lazily provision a
-- synthetic riders row (backed by a synthetic users row) tied to the
-- company, and remember it here so subsequent bookings reuse the same
-- identity instead of creating a new one each time.
ALTER TABLE tracker_companies
  ADD COLUMN IF NOT EXISTS synthetic_rider_id UUID REFERENCES riders(id);
