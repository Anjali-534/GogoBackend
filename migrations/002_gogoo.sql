-- ============================================================
-- GOGOO - Ride-hailing platform schema
-- Add on top of existing deploykit 001_init.sql
-- ============================================================

-- Driver profiles (linked to users table)
CREATE TABLE drivers (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  phone             TEXT NOT NULL,
  license_number    TEXT NOT NULL,
  vehicle_type      TEXT NOT NULL CHECK (vehicle_type IN ('bike','auto','mini','sedan','suv','xl')),
  vehicle_number    TEXT NOT NULL,
  vehicle_model     TEXT NOT NULL,
  vehicle_color     TEXT NOT NULL,
  profile_photo_url TEXT,
  license_photo_url TEXT,
  rc_photo_url      TEXT,
  is_verified       BOOLEAN DEFAULT FALSE,
  is_online         BOOLEAN DEFAULT FALSE,
  is_active         BOOLEAN DEFAULT TRUE,
  current_lat       DECIMAL(10,8),
  current_lng       DECIMAL(11,8),
  rating            DECIMAL(3,2) DEFAULT 5.00,
  total_rides       INT DEFAULT 0,
  total_earnings    DECIMAL(12,2) DEFAULT 0,
  created_at        TIMESTAMPTZ DEFAULT NOW(),
  updated_at        TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(user_id)
);

-- Rider profiles (linked to users table)
CREATE TABLE riders (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  phone           TEXT NOT NULL,
  profile_photo_url TEXT,
  rating          DECIMAL(3,2) DEFAULT 5.00,
  total_rides     INT DEFAULT 0,
  saved_addresses JSONB DEFAULT '[]',
  created_at      TIMESTAMPTZ DEFAULT NOW(),
  updated_at      TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(user_id)
);

-- Service types (bike, auto, mini, etc.)
CREATE TABLE service_types (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  name            TEXT NOT NULL,
  slug            TEXT UNIQUE NOT NULL,
  vehicle_type    TEXT NOT NULL,
  base_fare       DECIMAL(10,2) NOT NULL,
  per_km_rate     DECIMAL(10,2) NOT NULL,
  per_min_rate    DECIMAL(10,2) NOT NULL,
  surge_multiplier DECIMAL(4,2) DEFAULT 1.0,
  capacity        INT DEFAULT 4,
  icon_url        TEXT,
  is_active       BOOLEAN DEFAULT TRUE,
  created_at      TIMESTAMPTZ DEFAULT NOW()
);

-- Bookings / Rides
CREATE TABLE bookings (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  rider_id          UUID NOT NULL REFERENCES riders(id),
  driver_id         UUID REFERENCES drivers(id),
  service_type_id   UUID NOT NULL REFERENCES service_types(id),
  status            TEXT NOT NULL DEFAULT 'searching'
                      CHECK (status IN ('searching','accepted','arriving','in_progress','completed','cancelled')),
  -- Pickup
  pickup_lat        DECIMAL(10,8) NOT NULL,
  pickup_lng        DECIMAL(11,8) NOT NULL,
  pickup_address    TEXT NOT NULL,
  -- Drop
  drop_lat          DECIMAL(10,8) NOT NULL,
  drop_lng          DECIMAL(11,8) NOT NULL,
  drop_address      TEXT NOT NULL,
  -- Route
  distance_km       DECIMAL(10,2),
  duration_mins     INT,
  -- Fare
  estimated_fare    DECIMAL(10,2),
  final_fare        DECIMAL(10,2),
  surge_multiplier  DECIMAL(4,2) DEFAULT 1.0,
  promo_code        TEXT,
  discount_amount   DECIMAL(10,2) DEFAULT 0,
  -- Timing
  requested_at      TIMESTAMPTZ DEFAULT NOW(),
  accepted_at       TIMESTAMPTZ,
  arrived_at        TIMESTAMPTZ,
  started_at        TIMESTAMPTZ,
  completed_at      TIMESTAMPTZ,
  cancelled_at      TIMESTAMPTZ,
  cancel_reason     TEXT,
  cancelled_by      TEXT CHECK (cancelled_by IN ('rider','driver','system')),
  -- Ratings
  rider_rating      INT CHECK (rider_rating BETWEEN 1 AND 5),
  driver_rating     INT CHECK (driver_rating BETWEEN 1 AND 5),
  rider_review      TEXT,
  driver_review     TEXT,
  created_at        TIMESTAMPTZ DEFAULT NOW(),
  updated_at        TIMESTAMPTZ DEFAULT NOW()
);

-- Payments
CREATE TABLE payments (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  booking_id      UUID NOT NULL REFERENCES bookings(id),
  rider_id        UUID NOT NULL REFERENCES riders(id),
  driver_id       UUID REFERENCES drivers(id),
  amount          DECIMAL(10,2) NOT NULL,
  platform_fee    DECIMAL(10,2) DEFAULT 0,
  driver_earnings DECIMAL(10,2) DEFAULT 0,
  method          TEXT NOT NULL CHECK (method IN ('cash','upi','card','wallet')),
  status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','processing','completed','failed','refunded')),
  transaction_id  TEXT,
  gateway_ref     TEXT,
  created_at      TIMESTAMPTZ DEFAULT NOW(),
  updated_at      TIMESTAMPTZ DEFAULT NOW()
);

-- Driver earnings ledger
CREATE TABLE driver_earnings (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  driver_id   UUID NOT NULL REFERENCES drivers(id),
  booking_id  UUID REFERENCES bookings(id),
  amount      DECIMAL(10,2) NOT NULL,
  type        TEXT NOT NULL CHECK (type IN ('ride','bonus','adjustment')),
  description TEXT,
  created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- Driver documents for verification
CREATE TABLE driver_documents (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  driver_id   UUID NOT NULL REFERENCES drivers(id),
  type        TEXT NOT NULL CHECK (type IN ('license','rc','insurance','photo','aadhar','pan')),
  url         TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','approved','rejected')),
  reject_reason TEXT,
  reviewed_by UUID REFERENCES users(id),
  reviewed_at TIMESTAMPTZ,
  created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- Real-time location tracking
CREATE TABLE location_history (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  driver_id   UUID NOT NULL REFERENCES drivers(id),
  booking_id  UUID REFERENCES bookings(id),
  lat         DECIMAL(10,8) NOT NULL,
  lng         DECIMAL(11,8) NOT NULL,
  speed       DECIMAL(6,2),
  heading     INT,
  recorded_at TIMESTAMPTZ DEFAULT NOW()
);

-- Push notification tokens
CREATE TABLE device_tokens (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token       TEXT NOT NULL,
  platform    TEXT NOT NULL CHECK (platform IN ('ios','android')),
  created_at  TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(user_id, token)
);

-- Promo codes
CREATE TABLE promo_codes (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  code            TEXT UNIQUE NOT NULL,
  discount_type   TEXT NOT NULL CHECK (discount_type IN ('flat','percent')),
  discount_value  DECIMAL(10,2) NOT NULL,
  max_discount    DECIMAL(10,2),
  min_fare        DECIMAL(10,2) DEFAULT 0,
  max_uses        INT,
  uses_count      INT DEFAULT 0,
  valid_from      TIMESTAMPTZ,
  valid_until     TIMESTAMPTZ,
  is_active       BOOLEAN DEFAULT TRUE,
  created_at      TIMESTAMPTZ DEFAULT NOW()
);

-- Seed service types
INSERT INTO service_types (name, slug, vehicle_type, base_fare, per_km_rate, per_min_rate, capacity) VALUES
  ('Bike', 'bike', 'bike', 15.00, 5.00, 0.75, 1),
  ('Auto', 'auto', 'auto', 25.00, 8.00, 1.00, 3),
  ('Mini', 'mini', 'mini', 40.00, 11.00, 1.25, 4),
  ('Sedan', 'sedan', 'sedan', 60.00, 14.00, 1.50, 4),
  ('SUV', 'suv', 'suv', 80.00, 18.00, 2.00, 6),
  ('XL', 'xl', 'xl', 90.00, 20.00, 2.25, 6);

-- Indexes
CREATE INDEX idx_bookings_rider_id ON bookings(rider_id);
CREATE INDEX idx_bookings_driver_id ON bookings(driver_id);
CREATE INDEX idx_bookings_status ON bookings(status);
CREATE INDEX idx_bookings_created_at ON bookings(created_at);
CREATE INDEX idx_drivers_is_online ON drivers(is_online);
CREATE INDEX idx_payments_booking_id ON payments(booking_id);
CREATE INDEX idx_driver_earnings_driver_id ON driver_earnings(driver_id);
CREATE INDEX idx_location_history_driver_id ON location_history(driver_id);
