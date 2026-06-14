-- ============================================================
-- GOGOO Migration 008 — Simplified vehicle taxonomy
-- Three top-level categories:
--   1. TRUCK     — within-city + outstation
--   2. CAB       — 2-wheeler, 3-wheeler, 4-wheeler
--   3. AMBULANCE
-- Run after 007_live_tracking.sql
-- ============================================================

-- ---- Drivers: refresh the allowed vehicle_type set ----------
ALTER TABLE drivers DROP CONSTRAINT IF EXISTS drivers_vehicle_type_check;

-- A coarse category column makes filtering/reporting easy.
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS vehicle_category TEXT;

-- Map any legacy rows onto the new scheme so the new CHECK passes.
UPDATE drivers SET vehicle_type = 'truck_city_tata_ace'      WHERE vehicle_type IN ('tata_ace','bolero_pickup');
UPDATE drivers SET vehicle_type = 'truck_city_14ft'          WHERE vehicle_type IN ('truck_14ft','truck_17ft','truck_20ft');
UPDATE drivers SET vehicle_type = 'truck_os_14ft'            WHERE vehicle_type IN ('truck_14ft_os','truck_20ft_os','truck_32ft_os','truck_40ft_os');
UPDATE drivers SET vehicle_type = 'cab_2w'                   WHERE vehicle_type IN ('bike_delivery','scooter_delivery');
UPDATE drivers SET vehicle_type = 'ambulance_bls'            WHERE vehicle_type LIKE 'ambulance%';
UPDATE drivers SET vehicle_type = 'cab_4w'                   WHERE vehicle_type IN ('other','PENDING') OR vehicle_type IS NULL
                                                               OR vehicle_type NOT IN (
  'truck_city_tata_ace','truck_city_14ft','truck_city_open','truck_city_container',
  'truck_os_14ft','truck_os_20ft','truck_os_container','truck_os_trailer',
  'cab_2w','cab_3w','cab_4w','cab_4w_suv',
  'ambulance_bls','ambulance_als','ambulance_transport'
);

-- Backfill the category column from the (now normalised) vehicle_type.
UPDATE drivers SET vehicle_category =
  CASE
    WHEN vehicle_type LIKE 'truck_%'     THEN 'truck'
    WHEN vehicle_type LIKE 'cab_%'       THEN 'cab'
    WHEN vehicle_type LIKE 'ambulance_%' THEN 'ambulance'
    ELSE 'cab'
  END;

ALTER TABLE drivers ADD CONSTRAINT drivers_vehicle_type_check
  CHECK (vehicle_type IN (
    -- Trucks — within city
    'truck_city_tata_ace','truck_city_14ft','truck_city_open','truck_city_container',
    -- Trucks — outstation
    'truck_os_14ft','truck_os_20ft','truck_os_container','truck_os_trailer',
    -- Cabs
    'cab_2w','cab_3w','cab_4w','cab_4w_suv',
    -- Ambulance
    'ambulance_bls','ambulance_als','ambulance_transport'
  ));

-- ---- Service types: a "category" column + a fresh catalogue --
ALTER TABLE service_types
  ADD COLUMN IF NOT EXISTS category TEXT,
  ADD COLUMN IF NOT EXISTS scope    TEXT; -- 'city' | 'outstation' | null

DELETE FROM service_types;

INSERT INTO service_types
  (name, slug, vehicle_type, category, scope, base_fare, per_km_rate, per_min_rate, surge_multiplier, capacity, is_active)
VALUES
-- ============ TRUCK — WITHIN CITY ============
('Tata Ace / Mini Truck', 'truck_city_tata_ace',  'truck_city_tata_ace',  'truck', 'city',  200.00, 15.00, 2.00, 1.0, 1, true),
('14ft Truck',            'truck_city_14ft',       'truck_city_14ft',      'truck', 'city',  500.00, 22.00, 3.00, 1.0, 1, true),
('Open Body Truck',       'truck_city_open',       'truck_city_open',      'truck', 'city',  600.00, 24.00, 3.00, 1.0, 1, true),
('Container Truck',       'truck_city_container',  'truck_city_container', 'truck', 'city',  900.00, 28.00, 4.00, 1.0, 1, true),
-- ============ TRUCK — OUTSTATION ============
('14ft Truck (Outstation)',  'truck_os_14ft',      'truck_os_14ft',      'truck', 'outstation', 2500.00, 20.00, 0.00, 1.0, 1, true),
('20ft Truck (Outstation)',  'truck_os_20ft',      'truck_os_20ft',      'truck', 'outstation', 4000.00, 22.00, 0.00, 1.0, 1, true),
('Container (Outstation)',   'truck_os_container', 'truck_os_container', 'truck', 'outstation', 7000.00, 25.00, 0.00, 1.0, 1, true),
('Trailer (Outstation)',     'truck_os_trailer',   'truck_os_trailer',   'truck', 'outstation', 9000.00, 28.00, 0.00, 1.0, 1, true),
-- ============ CAB ============
('2 Wheeler',  'cab_2w',     'cab_2w',     'cab', null,  30.00,  6.00, 0.50, 1.0, 1, true),
('3 Wheeler (Auto)', 'cab_3w', 'cab_3w',   'cab', null,  40.00,  9.00, 1.00, 1.0, 3, true),
('4 Wheeler (Car)', 'cab_4w',  'cab_4w',    'cab', null,  60.00, 12.00, 1.50, 1.0, 4, true),
('4 Wheeler (SUV)', 'cab_4w_suv', 'cab_4w_suv', 'cab', null, 90.00, 16.00, 1.80, 1.0, 6, true),
-- ============ AMBULANCE ============
('Basic Life Support (BLS)',   'ambulance_bls',       'ambulance_bls',       'ambulance', null, 500.00, 20.00, 5.00, 1.0, 2, true),
('Advanced Life Support (ALS)','ambulance_als',       'ambulance_als',       'ambulance', null, 1000.00, 25.00, 8.00, 1.0, 2, true),
('Patient Transport',          'ambulance_transport', 'ambulance_transport', 'ambulance', null, 400.00, 18.00, 3.00, 1.0, 2, true);
