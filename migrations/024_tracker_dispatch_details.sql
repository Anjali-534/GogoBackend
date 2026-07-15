ALTER TABLE tracker_orders
  ADD COLUMN IF NOT EXISTS consignee_name       TEXT,
  ADD COLUMN IF NOT EXISTS material              TEXT,
  ADD COLUMN IF NOT EXISTS quantity              TEXT,
  ADD COLUMN IF NOT EXISTS dispatch_datetime     TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS documents_enclosed    TEXT;
