-- Bogie Tracker: B2B subscription dispatch-tracking product.
-- Companies self-signup (status='pending'), an admin approves them in the
-- dashboard, then the company gets a scoped panel to manage drivers/orders
-- and share a public no-login tracking link per order.

CREATE TABLE IF NOT EXISTS tracker_companies (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  company_name    TEXT NOT NULL,
  contact_phone   TEXT NOT NULL,
  contact_email   TEXT NOT NULL UNIQUE,
  password_hash   TEXT NOT NULL,
  gstin           TEXT,
  status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','active','suspended')),
  approved_by     UUID REFERENCES users(id),
  approved_at     TIMESTAMPTZ,
  created_at      TIMESTAMPTZ DEFAULT NOW(),
  updated_at      TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS tracker_drivers (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id          UUID NOT NULL REFERENCES tracker_companies(id),
  driver_name         TEXT NOT NULL,
  phone               TEXT NOT NULL,
  vehicle_number      TEXT,
  transporter_name    TEXT,
  transporter_phone   TEXT,
  is_active           BOOLEAN DEFAULT TRUE,
  created_at          TIMESTAMPTZ DEFAULT NOW(),
  updated_at          TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tracker_drivers_company_id ON tracker_drivers(company_id);

CREATE TABLE IF NOT EXISTS tracker_orders (
  id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id                UUID NOT NULL REFERENCES tracker_companies(id),
  booked_for_company_name   TEXT NOT NULL,
  booked_for_phone          TEXT NOT NULL,
  dispatch_from             TEXT NOT NULL,
  dispatch_to               TEXT NOT NULL,
  transporter_name          TEXT,
  transporter_phone         TEXT,
  driver_id                 UUID REFERENCES tracker_drivers(id) ON DELETE SET NULL,
  driver_name               TEXT,
  driver_phone              TEXT,
  vehicle_number            TEXT NOT NULL,
  eway_bill_number          TEXT,
  eway_bill_file_url        TEXT,
  status                    TEXT NOT NULL DEFAULT 'created'
                               CHECK (status IN ('created','dispatched','in_transit','delivered','cancelled')),
  public_tracking_token     TEXT NOT NULL UNIQUE,
  created_at                TIMESTAMPTZ DEFAULT NOW(),
  updated_at                TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tracker_orders_company_id ON tracker_orders(company_id);
CREATE INDEX IF NOT EXISTS idx_tracker_orders_public_tracking_token ON tracker_orders(public_tracking_token);

CREATE TABLE IF NOT EXISTS tracker_order_events (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id    UUID NOT NULL REFERENCES tracker_orders(id) ON DELETE CASCADE,
  status      TEXT NOT NULL
                CHECK (status IN ('created','dispatched','in_transit','delivered','cancelled')),
  note        TEXT,
  location    TEXT,
  created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tracker_order_events_order_id ON tracker_order_events(order_id);
