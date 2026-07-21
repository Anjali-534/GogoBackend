-- Real server-side promo code validation (closes audit Finding #3).
--
-- The old `promo_codes` table (from 002_gogoo.sql) is dead: zero rows,
-- and no Go code anywhere ever queries it. Dropping and recreating it
-- rather than ALTERing, since there's nothing in it to preserve.
--
-- Schema note: BOGIE100 and NEWUSER are used by both cab and truck today
-- but with different min_fare thresholds per category (see coupons.tsx
-- catalogs client-side). UNIQUE(code, applies_to) instead of UNIQUE(code)
-- lets the same code have one row per category with its own min_fare,
-- while applies_to = NULL is reserved for a genuine all-categories code.

DROP TABLE IF EXISTS promo_redemptions;
DROP TABLE IF EXISTS promo_codes;

CREATE TABLE promo_codes (
  id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  code                  TEXT NOT NULL,
  discount_type         TEXT NOT NULL CHECK (discount_type IN ('flat','percent')),
  discount_value        DECIMAL(10,2) NOT NULL,
  min_fare              DECIMAL(10,2) NOT NULL DEFAULT 0,
  max_discount          DECIMAL(10,2),
  valid_from            TIMESTAMPTZ,
  valid_until           TIMESTAMPTZ,
  usage_limit_total     INT,
  usage_limit_per_user  INT,
  active                BOOLEAN NOT NULL DEFAULT TRUE,
  applies_to            TEXT,  -- 'cab' | 'truck' | ... | NULL (all categories)
  created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Partial unique indexes: one for category-scoped codes, one for the
-- (rare) all-categories case, since UNIQUE(code, applies_to) treats
-- NULL applies_to as distinct per row in Postgres and wouldn't actually
-- stop two NULL-applies_to duplicates of the same code.
CREATE UNIQUE INDEX idx_promo_codes_code_category
  ON promo_codes (code, applies_to) WHERE applies_to IS NOT NULL;
CREATE UNIQUE INDEX idx_promo_codes_code_global
  ON promo_codes (code) WHERE applies_to IS NULL;

CREATE TABLE promo_redemptions (
  id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  promo_code_id  UUID NOT NULL REFERENCES promo_codes(id),
  code           TEXT NOT NULL,  -- denormalized copy for easy auditing/reporting
  rider_id       UUID NOT NULL REFERENCES riders(id),
  booking_id     UUID NOT NULL REFERENCES bookings(id),
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_promo_redemptions_rider_code ON promo_redemptions (rider_id, code);
CREATE INDEX idx_promo_redemptions_promo_code_id ON promo_redemptions (promo_code_id);

-- Seed the three existing client-side codes as real rows so nothing
-- breaks for users mid-transition. Values match the current hardcoded
-- catalogs in cab/coupons.tsx and truck/coupons.tsx exactly.
INSERT INTO promo_codes (code, discount_type, discount_value, min_fare, applies_to) VALUES
  ('BOGIE100', 'flat',    100, 150, 'cab'),
  ('BOGIE100', 'flat',    100, 200, 'truck'),
  ('RIDE10',   'percent', 10,  200, 'cab'),
  ('TRUCK10',  'percent', 10,  500, 'truck'),
  ('NEWUSER',  'flat',    150, 250, 'cab'),
  ('NEWUSER',  'flat',    150, 300, 'truck');
