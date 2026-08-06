-- Migration 056 — Parcel becomes a real top-level category
--
-- Two distinct fixes bundled into one migration because both happen
-- to touch drivers_vehicle_type_check and there's no reason to force
-- two separate DROP/ADD CONSTRAINT passes over the same constraint.
-- They are NOT related to each other beyond that.

-- ============================================================
-- FIX 1 — Parcel category move
--
-- parcel_2w was seeded in migration 054 under category='truck' as a
-- stopgap. Parcel is now a genuine top-level category — folded
-- operationally into truck-panel (no dedicated parcel-panel app),
-- but a distinct category value in service_types/bookings so it can
-- be filtered, reported on, and fare/cancellation-priced on its own
-- terms.
--
-- This is a pure relabel, not a data migration: bookings reference
-- service_types by service_type_id (a UUID FK, see migration 002),
-- never by a copy of the category string, so no booking row needs
-- touching or can be "orphaned" by this change — every booking that
-- already used parcel_2w simply now reports category='parcel'
-- instead of 'truck'.
--
-- drivers.vehicle_type could not previously equal 'parcel_2w' at
-- all — drivers_vehicle_type_check (migration 008) never included
-- it, and no migration since has touched that constraint. So no
-- driver row needs migrating either; the constraint rewrite below
-- just adds 'parcel_2w' to the allowed set so a driver CAN be
-- assigned it going forward (needed for Phase 2/3 to mean anything).
-- ============================================================

UPDATE service_types SET category = 'parcel' WHERE slug = 'parcel_2w';

-- ============================================================
-- FIX 2 — Pre-existing gap: migration 054's other 3 new
-- service_types rows (truck_city_eloader, truck_city_eeco,
-- truck_city_pickup8ft — all category='truck', unaffected by Fix 1)
-- were never added to drivers_vehicle_type_check either. Same bug
-- class as parcel_2w, unrelated feature — no driver could be
-- assigned any of these 3 vehicle types until now.
-- ============================================================

ALTER TABLE drivers DROP CONSTRAINT IF EXISTS drivers_vehicle_type_check;
ALTER TABLE drivers ADD CONSTRAINT drivers_vehicle_type_check
  CHECK (vehicle_type IN (
    -- Trucks — within city
    'truck_city_tata_ace','truck_city_14ft','truck_city_open','truck_city_container',
    'truck_city_eloader','truck_city_eeco','truck_city_pickup8ft',  -- Fix 2
    -- Trucks — outstation
    'truck_os_14ft','truck_os_20ft','truck_os_container','truck_os_trailer',
    -- Cabs
    'cab_2w','cab_3w','cab_4w','cab_4w_suv',
    -- Ambulance
    'ambulance_bls','ambulance_als','ambulance_transport',
    -- Parcel
    'parcel_2w'  -- Fix 1
  ));
