-- NGO / Free ambulance organizations
CREATE TABLE IF NOT EXISTS ambulance_ngos (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  type TEXT DEFAULT 'ngo',
  phone TEXT NOT NULL,
  whatsapp TEXT,
  email TEXT,
  address TEXT,
  area TEXT,
  city TEXT DEFAULT 'Delhi',
  pincode TEXT,
  coverage_areas TEXT[],
  vehicle_count INTEGER DEFAULT 0,
  is_active BOOLEAN DEFAULT TRUE,
  is_verified BOOLEAN DEFAULT FALSE,
  notes TEXT,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Hospital / Paid ambulance providers
CREATE TABLE IF NOT EXISTS ambulance_hospitals (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  type TEXT DEFAULT 'hospital',
  phone TEXT NOT NULL,
  whatsapp TEXT,
  email TEXT,
  address TEXT,
  area TEXT,
  city TEXT DEFAULT 'Delhi',
  pincode TEXT,
  latitude DECIMAL(10,8),
  longitude DECIMAL(11,8),
  ambulance_types TEXT[],
  vehicle_count INTEGER DEFAULT 0,
  base_fare DECIMAL(10,2) DEFAULT 500,
  per_km_rate DECIMAL(10,2) DEFAULT 30,
  login_email TEXT UNIQUE,
  password_hash TEXT,
  is_active BOOLEAN DEFAULT TRUE,
  is_verified BOOLEAN DEFAULT FALSE,
  rating DECIMAL(3,2) DEFAULT 0,
  total_bookings INTEGER DEFAULT 0,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Hospital ambulance bookings
CREATE TABLE IF NOT EXISTS hospital_ambulance_bookings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  booking_id UUID REFERENCES bookings(id),
  hospital_id UUID REFERENCES ambulance_hospitals(id),
  rider_id UUID,
  rider_name TEXT,
  rider_phone TEXT,
  patient_name TEXT,
  patient_condition TEXT,
  pickup_address TEXT,
  pickup_lat DECIMAL(10,8),
  pickup_lng DECIMAL(11,8),
  drop_address TEXT,
  drop_lat DECIMAL(10,8),
  drop_lng DECIMAL(11,8),
  ambulance_type TEXT,
  estimated_fare DECIMAL(10,2),
  status TEXT DEFAULT 'pending',
  hospital_confirmed_at TIMESTAMPTZ,
  hospital_rejected_reason TEXT,
  notes TEXT,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Sample Delhi NGOs
INSERT INTO ambulance_ngos
  (name, type, phone, whatsapp, email, address, area, pincode, coverage_areas, vehicle_count, is_active, is_verified)
VALUES
  ('Helpline Ambulance Seva','ngo','011-23456789','9811001100','helpline@seva.org','Sector 7, Rohini, Delhi','Rohini','110085',ARRAY['Rohini','Pitampura','Shalimar Bagh'],3,true,true),
  ('Jan Kalyan Ambulance Trust','trust','011-45678901','9811002200','jankalyan@trust.org','Lajpat Nagar, New Delhi','Lajpat Nagar','110024',ARRAY['Lajpat Nagar','Nehru Place','Sarita Vihar'],2,true,true),
  ('Delhi Sewa Ambulance','ngo','011-98765432','9811003300','delhisewa@ambulance.org','Karol Bagh, New Delhi','Karol Bagh','110005',ARRAY['Karol Bagh','Patel Nagar','Rajendra Place'],4,true,true),
  ('Manav Sewa Ambulance','trust','011-11223344','9811004400','manav@sewa.org','Dwarka Sector 12, Delhi','Dwarka','110075',ARRAY['Dwarka','Uttam Nagar','Janakpuri'],2,true,true),
  ('Sahyog Free Ambulance','ngo','011-55667788','9811005500','sahyog@ambulance.org','Shahdara, Delhi','Shahdara','110032',ARRAY['Shahdara','Preet Vihar','Vivek Vihar'],3,true,true),
  ('Asha Jyoti Ambulance Sewa','ngo','011-99887766','9811006600','ashajyoti@sewa.org','Vasant Kunj, New Delhi','Vasant Kunj','110070',ARRAY['Vasant Kunj','Mehrauli','Saket'],2,true,false),
  ('Seva Bharti Ambulance','trust','011-12345678','9811007700','sevabharti@ambulance.org','Connaught Place, New Delhi','Central Delhi','110001',ARRAY['CP','Paharganj','Karol Bagh','New Delhi'],5,true,true),
  ('Prayas Ambulance Foundation','ngo','011-87654321','9811008800','prayas@foundation.org','Mayur Vihar Phase 1, Delhi','Mayur Vihar','110091',ARRAY['Mayur Vihar','Patparganj','IP Extension'],2,true,true)
ON CONFLICT DO NOTHING;

-- Sample hospitals
INSERT INTO ambulance_hospitals
  (name, type, phone, email, address, area, pincode, latitude, longitude, ambulance_types, vehicle_count, base_fare, per_km_rate, is_active, is_verified)
VALUES
  ('AIIMS Delhi','government','011-26588500','aiims@aiims.edu','Ansari Nagar East, New Delhi','Ansari Nagar','110029',28.5672,77.2100,ARRAY['BLS','ALS','ICU'],8,500,25,true,true),
  ('Apollo Hospital Sarita Vihar','private','011-29871111','ambulance@apollo.com','Sarita Vihar, New Delhi','Sarita Vihar','110076',28.5355,77.2990,ARRAY['BLS','ALS','Neonatal'],5,800,35,true,true),
  ('Fortis Hospital Vasant Kunj','private','011-42776222','ambulance@fortis.com','Vasant Kunj, New Delhi','Vasant Kunj','110070',28.5244,77.1580,ARRAY['BLS','ALS'],4,700,30,true,true),
  ('Max Hospital Saket','private','011-26515050','ambulance@maxhealthcare.in','Press Enclave Road, Saket','Saket','110017',28.5274,77.2159,ARRAY['BLS','ALS','ICU'],6,750,32,true,true),
  ('Safdarjung Hospital','government','011-26707444','safdarjung@gov.in','Ansari Nagar West, New Delhi','Ansari Nagar','110029',28.5689,77.2042,ARRAY['BLS','ALS'],6,300,15,true,true),
  ('BLK Super Speciality Hospital','private','011-30403040','ambulance@blkhospital.com','Pusa Road, New Delhi','Pusa Road','110005',28.6422,77.1756,ARRAY['BLS','ALS','Cardiac'],3,800,35,true,true)
ON CONFLICT DO NOTHING;

-- Ambulance panel_access entry
INSERT INTO panel_access (panel_name, email, password_hash, role)
VALUES ('ambulance','ambulance@bogie.in','$2a$10$92IXUNpkjO0rOQ5byMi.Ye4oKoEa3Ro9llC/.og/at2.uheWG/igi','manager')
ON CONFLICT (panel_name, email) DO NOTHING;
