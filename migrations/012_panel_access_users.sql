-- Default panel operator accounts
-- Initial password for both is: password
-- Change via Master Panel → Settings → Panel Access
INSERT INTO panel_access (panel_name, email, password_hash, role)
VALUES
  ('cab',   'cab@bogie.in',   '$2a$10$92IXUNpkjO0rOQ5byMi.Ye4oKoEa3Ro9llC/.og/at2.uheWG/igi', 'manager'),
  ('truck', 'truck@bogie.in', '$2a$10$92IXUNpkjO0rOQ5byMi.Ye4oKoEa3Ro9llC/.og/at2.uheWG/igi', 'manager')
ON CONFLICT (panel_name, email) DO NOTHING;
