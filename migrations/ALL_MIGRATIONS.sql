-- ============================================================
-- DEPLOYKIT - Complete Database Schema
-- ============================================================

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================
-- USERS
-- ============================================================
CREATE TABLE users (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  email           TEXT UNIQUE NOT NULL,
  name            TEXT NOT NULL,
  avatar_url      TEXT,
  github_id       BIGINT UNIQUE,
  github_login    TEXT,
  gitlab_id       BIGINT UNIQUE,
  gitlab_login    TEXT,
  password_hash   TEXT,
  is_verified     BOOLEAN DEFAULT FALSE,
  created_at      TIMESTAMPTZ DEFAULT NOW(),
  updated_at      TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- OAUTH TOKENS (GitHub / GitLab per user)
-- ============================================================
CREATE TABLE oauth_tokens (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider      TEXT NOT NULL CHECK (provider IN ('github','gitlab')),
  access_token  TEXT NOT NULL,
  refresh_token TEXT,
  expires_at    TIMESTAMPTZ,
  scopes        TEXT[],
  created_at    TIMESTAMPTZ DEFAULT NOW(),
  updated_at    TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(user_id, provider)
);

-- ============================================================
-- PROJECTS (top-level workspace, like a GitHub org)
-- ============================================================
CREATE TABLE projects (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  name        TEXT NOT NULL,
  slug        TEXT UNIQUE NOT NULL,
  owner_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  plan        TEXT NOT NULL DEFAULT 'starter' CHECK (plan IN ('starter','standard','enterprise')),
  created_at  TIMESTAMPTZ DEFAULT NOW(),
  updated_at  TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- PROJECT MEMBERS (RBAC)
-- ============================================================
CREATE TABLE project_members (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role        TEXT NOT NULL DEFAULT 'developer' CHECK (role IN ('owner','admin','developer','viewer')),
  invited_by  UUID REFERENCES users(id),
  created_at  TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(project_id, user_id)
);

-- ============================================================
-- CLOUD CREDENTIALS (per project, per provider)
-- ============================================================
CREATE TABLE cloud_credentials (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  provider        TEXT NOT NULL CHECK (provider IN ('aws','gcp','azure')),
  name            TEXT NOT NULL,
  aws_account_id  TEXT,
  aws_role_arn    TEXT,
  aws_region      TEXT,
  gcp_project_id  TEXT,
  gcp_service_account JSONB,
  azure_subscription_id TEXT,
  azure_tenant_id TEXT,
  azure_client_id TEXT,
  azure_client_secret TEXT,
  created_at      TIMESTAMPTZ DEFAULT NOW(),
  updated_at      TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- CLUSTERS (one per customer environment)
-- ============================================================
CREATE TABLE clusters (
  id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id            UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  cloud_credential_id   UUID REFERENCES cloud_credentials(id),
  name                  TEXT NOT NULL,
  provider              TEXT NOT NULL CHECK (provider IN ('aws','gcp','azure','local')),
  region                TEXT NOT NULL,
  k8s_version           TEXT,
  status                TEXT NOT NULL DEFAULT 'provisioning'
                          CHECK (status IN ('provisioning','active','error','deleting','deleted')),
  vanity_url            TEXT,
  ingress_ip            TEXT,
  agent_connected       BOOLEAN DEFAULT FALSE,
  agent_version         TEXT,
  last_heartbeat        TIMESTAMPTZ,
  kubeconfig            TEXT,
  infra_state           JSONB,
  node_count            INT DEFAULT 2,
  node_instance_type    TEXT DEFAULT 't3.medium',
  created_at            TIMESTAMPTZ DEFAULT NOW(),
  updated_at            TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- ENVIRONMENT GROUPS (shared env vars across apps)
-- ============================================================
CREATE TABLE env_groups (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  cluster_id  UUID NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  version     INT NOT NULL DEFAULT 1,
  created_at  TIMESTAMPTZ DEFAULT NOW(),
  updated_at  TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(cluster_id, name)
);

CREATE TABLE env_group_vars (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  env_group_id  UUID NOT NULL REFERENCES env_groups(id) ON DELETE CASCADE,
  key           TEXT NOT NULL,
  value         TEXT NOT NULL,
  is_secret     BOOLEAN DEFAULT FALSE,
  created_at    TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(env_group_id, key)
);

CREATE TABLE env_group_versions (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  env_group_id  UUID NOT NULL REFERENCES env_groups(id) ON DELETE CASCADE,
  version       INT NOT NULL,
  snapshot      JSONB NOT NULL,
  created_by    UUID REFERENCES users(id),
  created_at    TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- APPS (web services, workers, cron jobs)
-- ============================================================
CREATE TABLE apps (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id        UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  cluster_id        UUID NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  name              TEXT NOT NULL,
  type              TEXT NOT NULL DEFAULT 'web'
                      CHECK (type IN ('web','worker','cron','job')),
  status            TEXT NOT NULL DEFAULT 'deploying'
                      CHECK (status IN ('deploying','running','errored','sleeping','deleting')),
  -- Source
  repo_url          TEXT,
  repo_branch       TEXT DEFAULT 'main',
  repo_provider     TEXT CHECK (repo_provider IN ('github','gitlab','docker')),
  docker_image      TEXT,
  -- Build
  build_method      TEXT DEFAULT 'buildpack'
                      CHECK (build_method IN ('buildpack','dockerfile','image')),
  dockerfile_path   TEXT DEFAULT 'Dockerfile',
  build_context     TEXT DEFAULT '.',
  -- Runtime
  start_command     TEXT,
  port              INT DEFAULT 3000,
  cpu_millicores    INT DEFAULT 100,
  ram_mb            INT DEFAULT 256,
  replicas          INT DEFAULT 1,
  -- Autoscaling
  autoscaling_enabled BOOLEAN DEFAULT FALSE,
  min_replicas        INT DEFAULT 1,
  max_replicas        INT DEFAULT 10,
  scale_on_cpu        INT DEFAULT 80,
  -- Cron
  cron_schedule     TEXT,
  -- Networking
  is_public         BOOLEAN DEFAULT TRUE,
  custom_domain     TEXT,
  subdomain         TEXT,
  -- Health check
  health_check_path TEXT DEFAULT '/health',
  -- Timestamps
  last_deployed_at  TIMESTAMPTZ,
  created_at        TIMESTAMPTZ DEFAULT NOW(),
  updated_at        TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(cluster_id, name)
);

-- ============================================================
-- APP ENV VARS
-- ============================================================
CREATE TABLE app_env_vars (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  app_id      UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  key         TEXT NOT NULL,
  value       TEXT NOT NULL,
  is_secret   BOOLEAN DEFAULT FALSE,
  created_at  TIMESTAMPTZ DEFAULT NOW(),
  updated_at  TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(app_id, key)
);

-- Link apps to env groups
CREATE TABLE app_env_groups (
  app_id        UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  env_group_id  UUID NOT NULL REFERENCES env_groups(id) ON DELETE CASCADE,
  PRIMARY KEY(app_id, env_group_id)
);

-- ============================================================
-- BUILDS
-- ============================================================
CREATE TABLE builds (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  app_id        UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  status        TEXT NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued','building','success','failed','cancelled')),
  commit_sha    TEXT,
  commit_msg    TEXT,
  commit_author TEXT,
  branch        TEXT,
  image_url     TEXT,
  logs          TEXT,
  error_msg     TEXT,
  started_at    TIMESTAMPTZ,
  finished_at   TIMESTAMPTZ,
  created_at    TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- DEPLOYMENTS (revisions)
-- ============================================================
CREATE TABLE deployments (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  app_id          UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  build_id        UUID REFERENCES builds(id),
  revision        INT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'deploying'
                    CHECK (status IN ('deploying','successful','failed','rolled_back')),
  image_url       TEXT,
  config_snapshot JSONB,
  deployed_by     UUID REFERENCES users(id),
  rollback_of     UUID REFERENCES deployments(id),
  created_at      TIMESTAMPTZ DEFAULT NOW(),
  updated_at      TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- DEPLOYMENT EVENTS (activity feed per app)
-- ============================================================
CREATE TABLE deployment_events (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  app_id          UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  deployment_id   UUID REFERENCES deployments(id),
  type            TEXT NOT NULL,
  message         TEXT NOT NULL,
  metadata        JSONB,
  created_at      TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- PREVIEW ENVIRONMENTS
-- ============================================================
CREATE TABLE preview_environments (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  app_id      UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  pr_number   INT NOT NULL,
  pr_title    TEXT,
  branch      TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'creating'
                CHECK (status IN ('creating','active','deleting','deleted')),
  url         TEXT,
  created_at  TIMESTAMPTZ DEFAULT NOW(),
  updated_at  TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(app_id, pr_number)
);

-- ============================================================
-- DATABASES (managed add-ons)
-- ============================================================
CREATE TABLE managed_databases (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  cluster_id    UUID NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  engine        TEXT NOT NULL CHECK (engine IN ('postgres','mysql','redis','mongodb','clickhouse')),
  version       TEXT,
  status        TEXT NOT NULL DEFAULT 'creating'
                  CHECK (status IN ('creating','available','deleting','deleted')),
  connection_url TEXT,
  cpu_millicores INT DEFAULT 500,
  ram_mb         INT DEFAULT 512,
  storage_gb     INT DEFAULT 20,
  created_at    TIMESTAMPTZ DEFAULT NOW(),
  updated_at    TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- BILLING USAGE (reported to Stripe hourly)
-- ============================================================
CREATE TABLE billing_usage (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  period_start  TIMESTAMPTZ NOT NULL,
  period_end    TIMESTAMPTZ NOT NULL,
  cpu_millicores BIGINT DEFAULT 0,
  ram_mb         BIGINT DEFAULT 0,
  stripe_reported BOOLEAN DEFAULT FALSE,
  created_at    TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- NOTIFICATIONS
-- ============================================================
CREATE TABLE notifications (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id  UUID REFERENCES projects(id) ON DELETE CASCADE,
  type        TEXT NOT NULL,
  title       TEXT NOT NULL,
  message     TEXT,
  is_read     BOOLEAN DEFAULT FALSE,
  metadata    JSONB,
  created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- WEBHOOKS
-- ============================================================
CREATE TABLE webhooks (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  app_id      UUID REFERENCES apps(id) ON DELETE CASCADE,
  url         TEXT NOT NULL,
  secret      TEXT NOT NULL,
  events      TEXT[] DEFAULT ARRAY['deploy.success','deploy.failed'],
  is_active   BOOLEAN DEFAULT TRUE,
  created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- INDEXES
-- ============================================================
CREATE INDEX idx_apps_cluster_id ON apps(cluster_id);
CREATE INDEX idx_apps_project_id ON apps(project_id);
CREATE INDEX idx_builds_app_id ON builds(app_id);
CREATE INDEX idx_deployments_app_id ON deployments(app_id);
CREATE INDEX idx_deployment_events_app_id ON deployment_events(app_id);
CREATE INDEX idx_clusters_project_id ON clusters(project_id);
CREATE INDEX idx_notifications_user_id ON notifications(user_id);
CREATE INDEX idx_billing_usage_project_id ON billing_usage(project_id);
CREATE INDEX idx_project_members_user_id ON project_members(user_id);
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
-- ============================================================
-- Migration 004 — Driver document uploads
-- ============================================================

CREATE TABLE IF NOT EXISTS driver_documents (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  driver_id     UUID NOT NULL REFERENCES drivers(id) ON DELETE CASCADE,
  doc_type      TEXT NOT NULL CHECK (doc_type IN (
                  'passport_photo',
                  'aadhaar_front', 'aadhaar_back',
                  'pan_card',
                  'driving_license_front', 'driving_license_back',
                  'rc_front', 'rc_back',
                  'insurance',
                  'pollution_cert',
                  'fitness_cert',
                  'permit',
                  'gst_cert',
                  'emt_cert',
                  'goods_insurance',
                  'bank_passbook',
                  'vehicle_photo_front',
                  'vehicle_photo_side'
                )),
  file_url      TEXT NOT NULL,
  file_name     TEXT,
  file_size     INT,
  mime_type     TEXT,
  status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','approved','rejected')),
  reject_reason TEXT,
  reviewed_by   UUID REFERENCES users(id),
  reviewed_at   TIMESTAMPTZ,
  uploaded_at   TIMESTAMPTZ DEFAULT NOW(),
  updated_at    TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(driver_id, doc_type)
);

CREATE INDEX idx_driver_documents_driver_id ON driver_documents(driver_id);
CREATE INDEX idx_driver_documents_status ON driver_documents(status);

-- Track overall document verification status on drivers table
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS docs_submitted     BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS docs_verified_at   TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS rejection_reason   TEXT;
-- ============================================================
-- Migration 005 — Driver onboarding fixes
--   1. Allow real-world vehicle_type values (logistics fleet)
--   2. Add bank / payout / GST columns used at signup
--   3. Widen driver_documents.doc_type to cover every category
-- ============================================================

-- 1) vehicle_type was locked to ride-hailing codes only. The logistics
--    platform sends free-form category labels, so drop the CHECK and keep
--    it as plain TEXT (validated on the client + via doc requirements).
ALTER TABLE drivers DROP CONSTRAINT IF EXISTS drivers_vehicle_type_check;

-- 2) Payout & business columns collected on the registration form.
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS bank_account_holder TEXT,
  ADD COLUMN IF NOT EXISTS bank_account_number TEXT,
  ADD COLUMN IF NOT EXISTS bank_ifsc           TEXT,
  ADD COLUMN IF NOT EXISTS bank_name           TEXT,
  ADD COLUMN IF NOT EXISTS upi_id              TEXT,
  ADD COLUMN IF NOT EXISTS gst_number          TEXT;

-- 3) driver_documents.doc_type CHECK rebuilt to match the app's doc ids.
--    (Older single-side ids like 'aadhaar' / 'driving_license' / 'rc' / 'puc'
--    are now accepted alongside the *_front / *_back variants.)
ALTER TABLE driver_documents DROP CONSTRAINT IF EXISTS driver_documents_doc_type_check;

ALTER TABLE driver_documents
  ADD CONSTRAINT driver_documents_doc_type_check CHECK (doc_type IN (
    'passport_photo',
    'aadhaar', 'aadhaar_front', 'aadhaar_back',
    'pan_card',
    'driving_license', 'driving_license_front', 'driving_license_back',
    'rc', 'rc_front', 'rc_back',
    'insurance',
    'puc', 'pollution_cert',
    'fitness_cert', 'fitness',
    'permit', 'national_permit',
    'gst_cert',
    'emt_cert',
    'goods_insurance',
    'bank_passbook',
    'vehicle_photo', 'vehicle_photo_front', 'vehicle_photo_side'
  ));

-- Store the document number / expiry the driver typed in the form.
ALTER TABLE driver_documents
  ADD COLUMN IF NOT EXISTS doc_number TEXT,
  ADD COLUMN IF NOT EXISTS expiry_date TEXT;
-- ============================================================
-- Migration 006 — Rebuild driver_documents with the correct schema
--
-- The original table (migration 002) used columns `type` / `url`.
-- All current code expects `doc_type`, `file_url`, `file_name`, etc.
-- Migration 004's CREATE TABLE IF NOT EXISTS skipped because the old
-- table already existed, so we drop and recreate here.
--
-- SAFE: document upload has never worked, so this table holds no real
-- data. CASCADE clears the stale constraints too.
-- ============================================================

DROP TABLE IF EXISTS driver_documents CASCADE;

CREATE TABLE driver_documents (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  driver_id     UUID NOT NULL REFERENCES drivers(id) ON DELETE CASCADE,
  doc_type      TEXT NOT NULL CHECK (doc_type IN (
                  'passport_photo',
                  'aadhaar', 'aadhaar_front', 'aadhaar_back',
                  'pan_card',
                  'driving_license', 'driving_license_front', 'driving_license_back',
                  'rc', 'rc_front', 'rc_back',
                  'insurance',
                  'puc', 'pollution_cert',
                  'fitness', 'fitness_cert',
                  'permit', 'national_permit',
                  'gst_cert',
                  'emt_cert',
                  'goods_insurance',
                  'bank_passbook',
                  'vehicle_photo', 'vehicle_photo_front', 'vehicle_photo_side'
                )),
  file_url      TEXT NOT NULL,
  file_name     TEXT,
  file_size     INT,
  mime_type     TEXT,
  doc_number    TEXT,
  expiry_date   TEXT,
  status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','approved','rejected')),
  reject_reason TEXT,
  reviewed_by   UUID REFERENCES users(id),
  reviewed_at   TIMESTAMPTZ,
  uploaded_at   TIMESTAMPTZ DEFAULT NOW(),
  updated_at    TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(driver_id, doc_type)
);

CREATE INDEX idx_driver_documents_driver_id ON driver_documents(driver_id);
CREATE INDEX idx_driver_documents_status ON driver_documents(status);

-- Verification-tracking columns on drivers (idempotent).
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS docs_submitted   BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS docs_verified_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS rejection_reason TEXT;
-- ============================================================
-- Migration 007 — Live tracking support
--   * Driver's live GPS stored on the driver row (current position)
--   * Booking carries a snapshot of the driver's live position so the
--     rider's poll (GET /bookings/:id) returns everything in one call
-- ============================================================

-- Driver's current live location (updated every few seconds while online).
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS current_lat        DECIMAL(10,8),
  ADD COLUMN IF NOT EXISTS current_lng        DECIMAL(11,8),
  ADD COLUMN IF NOT EXISTS location_updated_at TIMESTAMPTZ;

-- Live driver position mirrored onto the active booking, plus a heading
-- for smooth marker rotation on the rider's map.
ALTER TABLE bookings
  ADD COLUMN IF NOT EXISTS driver_lat        DECIMAL(10,8),
  ADD COLUMN IF NOT EXISTS driver_lng        DECIMAL(11,8),
  ADD COLUMN IF NOT EXISTS driver_heading    DECIMAL(6,2),
  ADD COLUMN IF NOT EXISTS driver_updated_at TIMESTAMPTZ;

-- Fast lookup of unassigned requests for the driver feed.
CREATE INDEX IF NOT EXISTS idx_bookings_searching
  ON bookings(status) WHERE status = 'searching';
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
-- Driver automatic blocking system
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS is_blocked    BOOLEAN     DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS blocked_until TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS block_reason  TEXT;

-- Index for fast blocked-driver lookups
CREATE INDEX IF NOT EXISTS idx_drivers_is_blocked ON drivers (is_blocked) WHERE is_blocked = TRUE;

-- ============================================================
-- 011_driver_wallet.sql — Driver wallet, ledger, earnings system
-- ============================================================
ALTER TABLE drivers
  ADD COLUMN IF NOT EXISTS wallet_balance           DECIMAL(12,2) DEFAULT -700.00,
  ADD COLUMN IF NOT EXISTS registration_fee_paid    BOOLEAN       DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS registration_fee_paid_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS is_wallet_blocked        BOOLEAN       DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS wallet_blocked_at        TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS wallet_blocked_reason    TEXT;

ALTER TABLE driver_earnings
  ADD COLUMN IF NOT EXISTS debit_type  TEXT,
  ADD COLUMN IF NOT EXISTS is_debit    BOOLEAN DEFAULT FALSE;

-- Backfill registration fee entry for all existing drivers
INSERT INTO driver_earnings (
  id, driver_id, amount, type, description,
  is_debit, debit_type, created_at
)
SELECT
  uuid_generate_v4(),
  d.id,
  700.00,
  'adjustment',
  'One-time registration fee — gogoo onboarding',
  true,
  'registration_fee',
  d.created_at
FROM drivers d
WHERE NOT EXISTS (
  SELECT 1 FROM driver_earnings de
  WHERE de.driver_id = d.id
  AND de.debit_type = 'registration_fee'
);

-- ============================================================
-- Migration 019 — Receiver details for truck/parcel deliveries
-- ============================================================
ALTER TABLE bookings
  ADD COLUMN IF NOT EXISTS receiver_name  TEXT,
  ADD COLUMN IF NOT EXISTS receiver_phone TEXT;

-- ============================================================
-- Migration 025 — Bogie Tracker: expanded status flow + live driver
-- location tracking.
-- ============================================================
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tracker_orders_status_check'
        AND conrelid = 'tracker_orders'::regclass
    ) THEN
        ALTER TABLE tracker_orders DROP CONSTRAINT tracker_orders_status_check;
    END IF;
EXCEPTION WHEN OTHERS THEN
    NULL;
END $$;

ALTER TABLE tracker_orders
    ADD CONSTRAINT tracker_orders_status_check
    CHECK (status IN ('created', 'loading', 'loaded', 'dispatched', 'in_transit', 'delivered', 'cancelled'));

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tracker_order_events_status_check'
        AND conrelid = 'tracker_order_events'::regclass
    ) THEN
        ALTER TABLE tracker_order_events DROP CONSTRAINT tracker_order_events_status_check;
    END IF;
EXCEPTION WHEN OTHERS THEN
    NULL;
END $$;

ALTER TABLE tracker_order_events
    ADD CONSTRAINT tracker_order_events_status_check
    CHECK (status IN ('created', 'loading', 'loaded', 'dispatched', 'in_transit', 'delivered', 'cancelled'));

ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS driver_tracking_token TEXT,
    ADD COLUMN IF NOT EXISTS last_lat               DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS last_lng               DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS last_location_at        TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tracker_orders_driver_tracking_token_key'
        AND conrelid = 'tracker_orders'::regclass
    ) THEN
        ALTER TABLE tracker_orders
            ADD CONSTRAINT tracker_orders_driver_tracking_token_key UNIQUE (driver_tracking_token);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_tracker_orders_driver_tracking_token
    ON tracker_orders(driver_tracking_token);

CREATE TABLE IF NOT EXISTS tracker_location_pings (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id    UUID NOT NULL REFERENCES tracker_orders(id) ON DELETE CASCADE,
    lat         DOUBLE PRECISION NOT NULL,
    lng         DOUBLE PRECISION NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tracker_location_pings_order_id_created_at
    ON tracker_location_pings(order_id, created_at);

-- ============================================================
-- Migration 026 — Bogie Tracker: optional lat/lng for dispatch_from
-- and dispatch_to addresses (Ola Places autocomplete / current location).
-- ============================================================
ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS dispatch_from_lat DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS dispatch_from_lng DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS dispatch_to_lat   DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS dispatch_to_lng   DOUBLE PRECISION;

-- ============================================================
-- Migration 027 — Bogie Tracker: planned-route cache (Ola Directions,
-- fetched once at order creation when both coordinate pairs exist).
-- ============================================================
ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS route_polyline      TEXT,
    ADD COLUMN IF NOT EXISTS route_distance_km   DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS route_duration_mins INTEGER;

-- ============================================================
-- Migration 028 — Bogie Tracker: two-way driver link (quick-status events,
-- delivery signature, company -> driver messages).
-- ============================================================
ALTER TABLE tracker_order_events
    ADD COLUMN IF NOT EXISTS reported_by TEXT NOT NULL DEFAULT 'company',
    ADD COLUMN IF NOT EXISTS event_kind  TEXT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tracker_order_events_reported_by_check'
        AND conrelid = 'tracker_order_events'::regclass
    ) THEN
        ALTER TABLE tracker_order_events
            ADD CONSTRAINT tracker_order_events_reported_by_check
            CHECK (reported_by IN ('company', 'driver'));
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tracker_order_events_event_kind_check'
        AND conrelid = 'tracker_order_events'::regclass
    ) THEN
        ALTER TABLE tracker_order_events
            ADD CONSTRAINT tracker_order_events_event_kind_check
            CHECK (event_kind IS NULL OR event_kind IN (
                'on_break', 'about_to_reach', 'reached', 'unloading', 'delivery_claimed'
            ));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_tracker_order_events_reported_by ON tracker_order_events(reported_by);

ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS signature_url TEXT;

CREATE TABLE IF NOT EXISTS tracker_driver_messages (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id    UUID NOT NULL REFERENCES tracker_orders(id) ON DELETE CASCADE,
    body        TEXT NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    read_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_tracker_driver_messages_order_id ON tracker_driver_messages(order_id);

-- ============================================================
-- Migration 029 — Bogie Tracker: dispatch notification emails.
-- ============================================================
ALTER TABLE tracker_companies
    ADD COLUMN IF NOT EXISTS notification_email TEXT;

ALTER TABLE tracker_orders
    ADD COLUMN IF NOT EXISTS booked_for_email  TEXT,
    ADD COLUMN IF NOT EXISTS consignee_email   TEXT,
    ADD COLUMN IF NOT EXISTS transporter_email TEXT;
