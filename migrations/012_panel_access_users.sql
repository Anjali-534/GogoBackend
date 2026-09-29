-- Default panel operator accounts, seeded LOCKED: '!locked' is not a valid
-- bcrypt hash, so bcrypt.CompareHashAndPassword rejects every password.
-- Set a real password via Master Panel → Settings → Panel Access before use.
-- DO NOTHING: this file re-runs on every boot and must never touch an
-- existing row's hash.
-- Any production row still on the old default hash needs a one-time manual
-- UPDATE (Master Panel UI or manual SQL, not part of this migration) — DO
-- NOTHING means this migration will never touch an existing row.
INSERT INTO panel_access (panel_name, email, password_hash, role)
VALUES
  ('cab',   'cab@bogie.in',   '!locked', 'manager'),
  ('truck', 'truck@bogie.in', '!locked', 'manager')
ON CONFLICT (panel_name, email) DO NOTHING;
