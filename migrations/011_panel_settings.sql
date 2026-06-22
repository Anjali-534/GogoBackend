-- Panel settings table: global key/value config managed from master panel
CREATE TABLE IF NOT EXISTS panel_settings (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  key TEXT UNIQUE NOT NULL,
  value TEXT NOT NULL,
  updated_by TEXT,
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Panel access control: sub-panel operator credentials
CREATE TABLE IF NOT EXISTS panel_access (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  panel_name TEXT NOT NULL,
  email TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  role TEXT DEFAULT 'operator',
  permissions JSONB DEFAULT '{}',
  is_active BOOLEAN DEFAULT TRUE,
  created_by TEXT,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  last_login TIMESTAMPTZ,
  UNIQUE(panel_name, email)
);

-- Default platform settings
INSERT INTO panel_settings (key, value) VALUES
  ('commission_percent',            '20'),
  ('surge_multiplier',              '1.0'),
  ('min_wallet_balance',            '500'),
  ('wallet_block_threshold',        '-1000'),
  ('registration_fee',              '700'),
  ('cancellation_fee',              '30'),
  ('otp_expiry_seconds',            '300'),
  ('cab_base_fare_2w',              '15'),
  ('cab_base_fare_3w',              '20'),
  ('cab_base_fare_4w',              '30'),
  ('cab_base_fare_suv',             '50'),
  ('cab_per_km_2w',                 '5'),
  ('cab_per_km_3w',                 '8'),
  ('cab_per_km_4w',                 '12'),
  ('cab_per_km_suv',                '18'),
  ('truck_city_base_fare',          '150'),
  ('truck_city_per_km',             '25'),
  ('truck_outstation_base_fare',    '500'),
  ('truck_outstation_per_km',       '35'),
  ('ambulance_bls_base_fare',       '300'),
  ('ambulance_als_base_fare',       '600'),
  ('ambulance_transport_base_fare', '200')
ON CONFLICT (key) DO NOTHING;
