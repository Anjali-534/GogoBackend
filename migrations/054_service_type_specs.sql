-- Migration 054 — Vehicle spec fields on service_types + 4 new categories
--
-- Adds dimension/capacity/fuel spec columns to service_types (nullable —
-- not every category has full specs yet), backfills them for the existing
-- 8 truck rows, and inserts 4 new categories: 2 Wheeler (Parcel),
-- E-Loader (3 Wheeler Electric), Eeco Cargo, and Pickup 8ft.
--
-- Documents changes already applied directly against Railway.

ALTER TABLE service_types
  ADD COLUMN IF NOT EXISTS length_m NUMERIC,
  ADD COLUMN IF NOT EXISTS width_m NUMERIC,
  ADD COLUMN IF NOT EXISTS height_m NUMERIC,
  ADD COLUMN IF NOT EXISTS weight_capacity_kg NUMERIC,
  ADD COLUMN IF NOT EXISTS fuel_types TEXT[];

-- ---- New categories ----------------------------------------------
INSERT INTO service_types (id, name, slug, vehicle_type, category, scope, base_fare, per_km_rate, per_min_rate, surge_multiplier, capacity, length_m, width_m, height_m, weight_capacity_kg, fuel_types)
VALUES
  (gen_random_uuid(), '2 Wheeler (Parcel)', 'parcel_2w', 'parcel_2w', 'truck', 'city', 30, 6, 0, 1, 1, NULL, NULL, NULL, 20, ARRAY['petrol','electric']),
  (gen_random_uuid(), 'E-Loader (3 Wheeler Electric)', 'truck_city_eloader', 'truck_city_eloader', 'truck', 'city', 80, 10, 0, 1, 1, 3.3, 1.5, 1.8, 500, ARRAY['electric']),
  (gen_random_uuid(), 'Eeco Cargo', 'truck_city_eeco', 'truck_city_eeco', 'truck', 'city', 100, 12, 0, 1, 1, 3.68, 1.48, 1.83, 920, ARRAY['petrol','cng']),
  (gen_random_uuid(), 'Pickup 8ft', 'truck_city_pickup8ft', 'truck_city_pickup8ft', 'truck', 'city', 250, 18, 0, 1, 1, 5.2, 1.7, 1.87, 1000, ARRAY['diesel'])
ON CONFLICT (slug) DO NOTHING;

-- ---- Backfill specs on existing truck rows -------------------------
UPDATE service_types SET length_m = 3.8, width_m = 1.5, height_m = 1.85, weight_capacity_kg = 900, fuel_types = ARRAY['petrol','diesel','cng','electric']
  WHERE slug = 'truck_city_tata_ace';

UPDATE service_types SET length_m = 4.3, weight_capacity_kg = 3500, fuel_types = ARRAY['diesel']
  WHERE slug = 'truck_city_14ft';

UPDATE service_types SET length_m = 4.5, weight_capacity_kg = 4000, fuel_types = ARRAY['diesel']
  WHERE slug = 'truck_city_open';

UPDATE service_types SET length_m = 6.5, weight_capacity_kg = 5000, fuel_types = ARRAY['diesel']
  WHERE slug = 'truck_city_container';

UPDATE service_types SET length_m = 4.3, weight_capacity_kg = 3500, fuel_types = ARRAY['diesel']
  WHERE slug = 'truck_os_14ft';

UPDATE service_types SET length_m = 6.0, weight_capacity_kg = 7000, fuel_types = ARRAY['diesel']
  WHERE slug = 'truck_os_20ft';

UPDATE service_types SET length_m = 7.3, weight_capacity_kg = 10000, fuel_types = ARRAY['diesel']
  WHERE slug = 'truck_os_container';

UPDATE service_types SET length_m = 10.0, weight_capacity_kg = 20000, fuel_types = ARRAY['diesel']
  WHERE slug = 'truck_os_trailer';
