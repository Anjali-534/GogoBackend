-- ============================================================
-- GOGOO Migration 003 — Logistics vehicle types & driver fields
-- Run after 002_gogoo.sql
-- ============================================================

-- Drop old vehicle_type constraint
ALTER TABLE drivers DROP CONSTRAINT IF EXISTS drivers_vehicle_type_check;

-- Add new columns to drivers table
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS license_type        TEXT DEFAULT 'LMV',
  ADD COLUMN IF NOT EXISTS license_expiry      DATE,
  ADD COLUMN IF NOT EXISTS aadhaar_number      TEXT,
  ADD COLUMN IF NOT EXISTS pan_number          TEXT,
  ADD COLUMN IF NOT EXISTS upi_id              TEXT,
  ADD COLUMN IF NOT EXISTS payload_capacity    DECIMAL(6,2),
  ADD COLUMN IF NOT EXISTS permit_type         TEXT,
  ADD COLUMN IF NOT EXISTS fitness_expiry      DATE,
  ADD COLUMN IF NOT EXISTS rc_expiry           DATE,
  ADD COLUMN IF NOT EXISTS insurance_expiry    DATE,
  ADD COLUMN IF NOT EXISTS pollution_expiry    DATE,
  ADD COLUMN IF NOT EXISTS gps_installed       BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS delivery_box        BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS gst_number          TEXT,
  ADD COLUMN IF NOT EXISTS team_size           INT DEFAULT 1,
  ADD COLUMN IF NOT EXISTS experience_years    INT DEFAULT 0,
  ADD COLUMN IF NOT EXISTS goods_insured       BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS packing_available   BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS emt_certified       BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS oxygen_cylinders    INT DEFAULT 0,
  ADD COLUMN IF NOT EXISTS stretcher_available BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS available_24x7      BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS hospital_tie_up     TEXT,
  ADD COLUMN IF NOT EXISTS bank_account_holder TEXT,
  ADD COLUMN IF NOT EXISTS bank_account_number TEXT,
  ADD COLUMN IF NOT EXISTS bank_ifsc           TEXT,
  ADD COLUMN IF NOT EXISTS bank_name           TEXT,
  ADD COLUMN IF NOT EXISTS rc_number           TEXT,
  ADD COLUMN IF NOT EXISTS insurance_number    TEXT,
  ADD COLUMN IF NOT EXISTS pollution_number    TEXT,
  ADD COLUMN IF NOT EXISTS fuel_type           TEXT DEFAULT 'Petrol',
  ADD COLUMN IF NOT EXISTS services_offered    TEXT[];

-- Add new vehicle_type constraint
ALTER TABLE drivers ADD CONSTRAINT drivers_vehicle_type_check
  CHECK (vehicle_type IN (
    'bike_delivery', 'scooter_delivery',
    'tata_ace', 'bolero_pickup', 'truck_14ft', 'truck_17ft', 'truck_20ft',
    'truck_14ft_os', 'truck_20ft_os', 'truck_32ft_os', 'truck_40ft_os',
    'packers_1bhk', 'packers_2bhk', 'packers_3bhk', 'packers_office', 'packers_single',
    'ambulance_bls', 'ambulance_als', 'ambulance_transport', 'ambulance_dbv'
  ));

-- Clear old service types
DELETE FROM service_types;

-- Insert new service types
INSERT INTO service_types (name, slug, vehicle_type, base_fare, per_km_rate, per_min_rate, surge_multiplier, capacity, is_active) VALUES

-- 2 Wheelers
('Bike Delivery',    'bike_delivery',    'bike_delivery',    30.00,  5.00,  0.50, 1.0, 1, true),
('Scooter Delivery', 'scooter_delivery', 'scooter_delivery', 35.00,  6.00,  0.60, 1.0, 1, true),

-- Trucks Within City
('Tata Ace / Mini Truck', 'tata_ace',     'tata_ace',     200.00, 15.00, 2.00, 1.0, 1, true),
('Bolero Pickup',         'bolero_pickup','bolero_pickup', 300.00, 18.00, 2.50, 1.0, 1, true),
('14ft Truck',            'truck_14ft',   'truck_14ft',   500.00, 22.00, 3.00, 1.0, 1, true),
('17ft Truck',            'truck_17ft',   'truck_17ft',   700.00, 25.00, 3.50, 1.0, 1, true),
('20ft Truck',            'truck_20ft',   'truck_20ft',   900.00, 28.00, 4.00, 1.0, 1, true),

-- Trucks Outstation
('14ft Truck (Outstation)', 'truck_14ft_os', 'truck_14ft_os', 2500.00, 20.00, 0.00, 1.0, 1, true),
('20ft Truck (Outstation)', 'truck_20ft_os', 'truck_20ft_os', 4000.00, 22.00, 0.00, 1.0, 1, true),
('32ft Trailer',            'truck_32ft_os', 'truck_32ft_os', 7000.00, 25.00, 0.00, 1.0, 1, true),
('40ft Container',          'truck_40ft_os', 'truck_40ft_os', 9000.00, 28.00, 0.00, 1.0, 1, true),

-- Packers & Movers
('1 BHK Move',    'packers_1bhk',    'packers_1bhk',    2500.00, 0.00, 0.00, 1.0, 1, true),
('2 BHK Move',    'packers_2bhk',    'packers_2bhk',    4500.00, 0.00, 0.00, 1.0, 1, true),
('3 BHK Move',    'packers_3bhk',    'packers_3bhk',    7000.00, 0.00, 0.00, 1.0, 1, true),
('Office Shift',  'packers_office',  'packers_office',  9000.00, 0.00, 0.00, 1.0, 1, true),
('Single Item',   'packers_single',  'packers_single',   800.00, 10.00, 0.00, 1.0, 1, true),

-- Ambulance
('Basic Life Support (BLS)',  'ambulance_bls',       'ambulance_bls',       500.00, 20.00, 5.00, 1.0, 2, true),
('Advanced Life Support (ALS)','ambulance_als',      'ambulance_als',      1000.00, 25.00, 8.00, 1.0, 2, true),
('Patient Transport',          'ambulance_transport','ambulance_transport',  400.00, 18.00, 3.00, 1.0, 2, true),
('Dead Body Van',              'ambulance_dbv',      'ambulance_dbv',        600.00, 20.00, 0.00, 1.0, 1, true);
