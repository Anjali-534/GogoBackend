package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// ─── Shared types ─────────────────────────────────────────────────────────────

type AmbulanceNGO struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	Phone         string    `json:"phone"`
	Whatsapp      string    `json:"whatsapp"`
	Email         string    `json:"email"`
	Address       string    `json:"address"`
	Area          string    `json:"area"`
	City          string    `json:"city"`
	Pincode       string    `json:"pincode"`
	CoverageAreas []string  `json:"coverage_areas"`
	VehicleCount  int       `json:"vehicle_count"`
	IsActive      bool      `json:"is_active"`
	IsVerified    bool      `json:"is_verified"`
	Notes         string    `json:"notes"`
	CreatedAt     time.Time `json:"created_at"`
}

type AmbulanceHospital struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Type           string    `json:"type"`
	Phone          string    `json:"phone"`
	Whatsapp       string    `json:"whatsapp"`
	Email          string    `json:"email"`
	Address        string    `json:"address"`
	Area           string    `json:"area"`
	City           string    `json:"city"`
	Pincode        string    `json:"pincode"`
	Latitude       float64   `json:"latitude"`
	Longitude      float64   `json:"longitude"`
	AmbulanceTypes []string  `json:"ambulance_types"`
	VehicleCount   int       `json:"vehicle_count"`
	BaseFare       float64   `json:"base_fare"`
	PerKmRate      float64   `json:"per_km_rate"`
	LoginEmail     string    `json:"login_email"`
	IsActive       bool      `json:"is_active"`
	IsVerified     bool      `json:"is_verified"`
	Rating         float64   `json:"rating"`
	TotalBookings  int       `json:"total_bookings"`
	CreatedAt      time.Time `json:"created_at"`
}

// ─── NGO Handlers ─────────────────────────────────────────────────────────────

// GET /gogoo/ambulance/ngos
func GetNGOs(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT id, name, COALESCE(type,'ngo'), phone,
		       COALESCE(whatsapp,''), COALESCE(email,''),
		       COALESCE(address,''), COALESCE(area,''),
		       COALESCE(city,'Delhi'), COALESCE(pincode,''),
		       COALESCE(coverage_areas, ARRAY[]::TEXT[]),
		       vehicle_count, is_active, is_verified,
		       COALESCE(notes,''), created_at
		FROM ambulance_ngos ORDER BY name
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	var ngos []AmbulanceNGO
	for rows.Next() {
		var n AmbulanceNGO
		rows.Scan(&n.ID, &n.Name, &n.Type, &n.Phone, &n.Whatsapp, &n.Email,
			&n.Address, &n.Area, &n.City, &n.Pincode, &n.CoverageAreas,
			&n.VehicleCount, &n.IsActive, &n.IsVerified, &n.Notes, &n.CreatedAt)
		ngos = append(ngos, n)
	}
	if ngos == nil {
		ngos = []AmbulanceNGO{}
	}
	c.JSON(http.StatusOK, ngos)
}

// POST /gogoo/ambulance/ngos
func CreateNGO(c *gin.Context) {
	var req struct {
		Name          string   `json:"name"`
		Type          string   `json:"type"`
		Phone         string   `json:"phone"`
		Whatsapp      string   `json:"whatsapp"`
		Email         string   `json:"email"`
		Address       string   `json:"address"`
		Area          string   `json:"area"`
		City          string   `json:"city"`
		Pincode       string   `json:"pincode"`
		CoverageAreas []string `json:"coverage_areas"`
		VehicleCount  int      `json:"vehicle_count"`
		IsActive      bool     `json:"is_active"`
		IsVerified    bool     `json:"is_verified"`
		Notes         string   `json:"notes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Type == "" {
		req.Type = "ngo"
	}
	if req.City == "" {
		req.City = "Delhi"
	}
	if req.CoverageAreas == nil {
		req.CoverageAreas = []string{}
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	id := uuid.New()

	_, err := pool.Exec(ctx, `
		INSERT INTO ambulance_ngos
		    (id, name, type, phone, whatsapp, email, address, area, city,
		     pincode, coverage_areas, vehicle_count, is_active, is_verified, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
	`, id, req.Name, req.Type, req.Phone, req.Whatsapp, req.Email,
		req.Address, req.Area, req.City, req.Pincode, req.CoverageAreas,
		req.VehicleCount, req.IsActive, req.IsVerified, req.Notes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create NGO: " + err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "message": "NGO created"})
}

// PATCH /gogoo/ambulance/ngos/:id
func UpdateNGO(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Name          string   `json:"name"`
		Type          string   `json:"type"`
		Phone         string   `json:"phone"`
		Whatsapp      string   `json:"whatsapp"`
		Email         string   `json:"email"`
		Address       string   `json:"address"`
		Area          string   `json:"area"`
		City          string   `json:"city"`
		Pincode       string   `json:"pincode"`
		CoverageAreas []string `json:"coverage_areas"`
		VehicleCount  int      `json:"vehicle_count"`
		IsActive      bool     `json:"is_active"`
		IsVerified    bool     `json:"is_verified"`
		Notes         string   `json:"notes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.CoverageAreas == nil {
		req.CoverageAreas = []string{}
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	_, err := pool.Exec(ctx, `
		UPDATE ambulance_ngos SET
		    name=$1, type=$2, phone=$3, whatsapp=$4, email=$5,
		    address=$6, area=$7, city=$8, pincode=$9,
		    coverage_areas=$10, vehicle_count=$11,
		    is_active=$12, is_verified=$13, notes=$14,
		    updated_at=NOW()
		WHERE id=$15
	`, req.Name, req.Type, req.Phone, req.Whatsapp, req.Email,
		req.Address, req.Area, req.City, req.Pincode, req.CoverageAreas,
		req.VehicleCount, req.IsActive, req.IsVerified, req.Notes, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "NGO updated"})
}

// DELETE /gogoo/ambulance/ngos/:id
func DeleteNGO(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	_, err := pool.Exec(ctx, `DELETE FROM ambulance_ngos WHERE id=$1`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "NGO deleted"})
}

// ─── Hospital Handlers ────────────────────────────────────────────────────────

// GET /gogoo/ambulance/hospitals
func GetHospitals(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT id, name, COALESCE(type,'hospital'), phone,
		       COALESCE(whatsapp,''), COALESCE(email,''),
		       COALESCE(address,''), COALESCE(area,''),
		       COALESCE(city,'Delhi'), COALESCE(pincode,''),
		       COALESCE(latitude,0), COALESCE(longitude,0),
		       COALESCE(ambulance_types, ARRAY[]::TEXT[]),
		       vehicle_count, base_fare, per_km_rate,
		       COALESCE(login_email,''),
		       is_active, is_verified, rating, total_bookings, created_at
		FROM ambulance_hospitals ORDER BY name
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	var hospitals []AmbulanceHospital
	for rows.Next() {
		var h AmbulanceHospital
		rows.Scan(&h.ID, &h.Name, &h.Type, &h.Phone, &h.Whatsapp, &h.Email,
			&h.Address, &h.Area, &h.City, &h.Pincode, &h.Latitude, &h.Longitude,
			&h.AmbulanceTypes, &h.VehicleCount, &h.BaseFare, &h.PerKmRate,
			&h.LoginEmail, &h.IsActive, &h.IsVerified, &h.Rating, &h.TotalBookings, &h.CreatedAt)
		hospitals = append(hospitals, h)
	}
	if hospitals == nil {
		hospitals = []AmbulanceHospital{}
	}
	c.JSON(http.StatusOK, hospitals)
}

// GET /gogoo/ambulance/hospitals/:id
func GetHospitalByID(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var h AmbulanceHospital
	err := pool.QueryRow(ctx, `
		SELECT id, name, COALESCE(type,'hospital'), phone,
		       COALESCE(whatsapp,''), COALESCE(email,''),
		       COALESCE(address,''), COALESCE(area,''),
		       COALESCE(city,'Delhi'), COALESCE(pincode,''),
		       COALESCE(latitude,0), COALESCE(longitude,0),
		       COALESCE(ambulance_types, ARRAY[]::TEXT[]),
		       vehicle_count, base_fare, per_km_rate,
		       COALESCE(login_email,''),
		       is_active, is_verified, rating, total_bookings, created_at
		FROM ambulance_hospitals WHERE id=$1
	`, id).Scan(&h.ID, &h.Name, &h.Type, &h.Phone, &h.Whatsapp, &h.Email,
		&h.Address, &h.Area, &h.City, &h.Pincode, &h.Latitude, &h.Longitude,
		&h.AmbulanceTypes, &h.VehicleCount, &h.BaseFare, &h.PerKmRate,
		&h.LoginEmail, &h.IsActive, &h.IsVerified, &h.Rating, &h.TotalBookings, &h.CreatedAt)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "hospital not found"})
		return
	}
	c.JSON(http.StatusOK, h)
}

// POST /gogoo/ambulance/hospitals
func CreateHospital(c *gin.Context) {
	var req struct {
		Name           string   `json:"name"`
		Type           string   `json:"type"`
		Phone          string   `json:"phone"`
		Whatsapp       string   `json:"whatsapp"`
		Email          string   `json:"email"`
		Address        string   `json:"address"`
		Area           string   `json:"area"`
		City           string   `json:"city"`
		Pincode        string   `json:"pincode"`
		Latitude       float64  `json:"latitude"`
		Longitude      float64  `json:"longitude"`
		AmbulanceTypes []string `json:"ambulance_types"`
		VehicleCount   int      `json:"vehicle_count"`
		BaseFare       float64  `json:"base_fare"`
		PerKmRate      float64  `json:"per_km_rate"`
		LoginEmail     string   `json:"login_email"`
		Password       string   `json:"password"`
		IsActive       bool     `json:"is_active"`
		IsVerified     bool     `json:"is_verified"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.City == "" {
		req.City = "Delhi"
	}
	if req.AmbulanceTypes == nil {
		req.AmbulanceTypes = []string{}
	}
	if req.BaseFare == 0 {
		req.BaseFare = 500
	}
	if req.PerKmRate == 0 {
		req.PerKmRate = 30
	}

	var passwordHash interface{}
	if req.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "hash failed"})
			return
		}
		passwordHash = string(hash)
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	id := uuid.New()

	_, err := pool.Exec(ctx, `
		INSERT INTO ambulance_hospitals
		    (id, name, type, phone, whatsapp, email, address, area, city, pincode,
		     latitude, longitude, ambulance_types, vehicle_count, base_fare, per_km_rate,
		     login_email, password_hash, is_active, is_verified)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	`, id, req.Name, req.Type, req.Phone, req.Whatsapp, req.Email,
		req.Address, req.Area, req.City, req.Pincode,
		req.Latitude, req.Longitude, req.AmbulanceTypes,
		req.VehicleCount, req.BaseFare, req.PerKmRate,
		nullIfEmpty(req.LoginEmail), passwordHash, req.IsActive, req.IsVerified)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create hospital: " + err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "message": "Hospital created"})
}

// PATCH /gogoo/ambulance/hospitals/:id
func UpdateHospital(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Name           string   `json:"name"`
		Type           string   `json:"type"`
		Phone          string   `json:"phone"`
		Whatsapp       string   `json:"whatsapp"`
		Email          string   `json:"email"`
		Address        string   `json:"address"`
		Area           string   `json:"area"`
		City           string   `json:"city"`
		Pincode        string   `json:"pincode"`
		Latitude       float64  `json:"latitude"`
		Longitude      float64  `json:"longitude"`
		AmbulanceTypes []string `json:"ambulance_types"`
		VehicleCount   int      `json:"vehicle_count"`
		BaseFare       float64  `json:"base_fare"`
		PerKmRate      float64  `json:"per_km_rate"`
		IsActive       bool     `json:"is_active"`
		IsVerified     bool     `json:"is_verified"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.AmbulanceTypes == nil {
		req.AmbulanceTypes = []string{}
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	_, err := pool.Exec(ctx, `
		UPDATE ambulance_hospitals SET
		    name=$1, type=$2, phone=$3, whatsapp=$4, email=$5,
		    address=$6, area=$7, city=$8, pincode=$9,
		    latitude=$10, longitude=$11, ambulance_types=$12,
		    vehicle_count=$13, base_fare=$14, per_km_rate=$15,
		    is_active=$16, is_verified=$17, updated_at=NOW()
		WHERE id=$18
	`, req.Name, req.Type, req.Phone, req.Whatsapp, req.Email,
		req.Address, req.Area, req.City, req.Pincode,
		req.Latitude, req.Longitude, req.AmbulanceTypes,
		req.VehicleCount, req.BaseFare, req.PerKmRate,
		req.IsActive, req.IsVerified, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Hospital updated"})
}

// DELETE /gogoo/ambulance/hospitals/:id
func DeleteHospital(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	_, err := pool.Exec(ctx, `DELETE FROM ambulance_hospitals WHERE id=$1`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Hospital deleted"})
}

// PATCH /gogoo/ambulance/hospitals/:id/password
func ResetHospitalPassword(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash failed"})
		return
	}
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	_, err = pool.Exec(ctx, `UPDATE ambulance_hospitals SET password_hash=$1 WHERE id=$2`, string(hash), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Password updated"})
}

// ─── Hospital Portal Login ────────────────────────────────────────────────────

// POST /gogoo/hospital-login
func HospitalLogin(c *gin.Context) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var id, name, passwordHash string
	var isActive bool
	err := pool.QueryRow(ctx, `
		SELECT id, name, password_hash, is_active
		FROM ambulance_hospitals WHERE login_email=$1
	`, req.Email).Scan(&id, &name, &passwordHash, &isActive)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}
	if !isActive {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Hospital account is inactive"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	cfg := c.MustGet("config").(*config.Config)
	token := signPanelToken(id, req.Email, "hospital", "hospital", cfg.JWTSecret)
	c.JSON(http.StatusOK, gin.H{
		"token":       token,
		"hospital_id": id,
		"name":        name,
		"role":        "hospital",
	})
}

// ─── Hospital Ambulance Bookings ──────────────────────────────────────────────

type HospitalBooking struct {
	ID                     string     `json:"id"`
	BookingID              string     `json:"booking_id"`
	HospitalID             string     `json:"hospital_id"`
	RiderName              string     `json:"rider_name"`
	RiderPhone             string     `json:"rider_phone"`
	PatientName            string     `json:"patient_name"`
	PatientCondition       string     `json:"patient_condition"`
	PickupAddress          string     `json:"pickup_address"`
	PickupLat              float64    `json:"pickup_lat"`
	PickupLng              float64    `json:"pickup_lng"`
	DropAddress            string     `json:"drop_address"`
	DropLat                float64    `json:"drop_lat"`
	DropLng                float64    `json:"drop_lng"`
	AmbulanceType          string     `json:"ambulance_type"`
	EstimatedFare          float64    `json:"estimated_fare"`
	Status                 string     `json:"status"`
	HospitalRejectedReason string     `json:"hospital_rejected_reason"`
	Notes                  string     `json:"notes"`
	CreatedAt              time.Time  `json:"created_at"`
	HospitalName           string     `json:"hospital_name"`
}

// GET /gogoo/ambulance/bookings/hospital
func GetHospitalBookings(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	hospitalID := c.Query("hospital_id")
	status := c.Query("status")

	query := `
		SELECT b.id,
		       COALESCE(b.booking_id::TEXT,''),
		       COALESCE(b.hospital_id::TEXT,''),
		       COALESCE(b.rider_name,''), COALESCE(b.rider_phone,''),
		       COALESCE(b.patient_name,''), COALESCE(b.patient_condition,''),
		       COALESCE(b.pickup_address,''),
		       COALESCE(b.pickup_lat,0), COALESCE(b.pickup_lng,0),
		       COALESCE(b.drop_address,''),
		       COALESCE(b.drop_lat,0), COALESCE(b.drop_lng,0),
		       COALESCE(b.ambulance_type,''), COALESCE(b.estimated_fare,0),
		       b.status,
		       COALESCE(b.hospital_rejected_reason,''),
		       COALESCE(b.notes,''), b.created_at,
		       COALESCE(h.name,'')
		FROM hospital_ambulance_bookings b
		LEFT JOIN ambulance_hospitals h ON h.id = b.hospital_id
		WHERE 1=1`
	args := []interface{}{}
	i := 1
	if hospitalID != "" {
		query += fmt.Sprintf(" AND b.hospital_id=$%d", i)
		args = append(args, hospitalID)
		i++
	}
	if status != "" {
		query += fmt.Sprintf(" AND b.status=$%d", i)
		args = append(args, status)
	}
	query += " ORDER BY b.created_at DESC LIMIT 100"

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	var bookings []HospitalBooking
	for rows.Next() {
		var b HospitalBooking
		rows.Scan(&b.ID, &b.BookingID, &b.HospitalID,
			&b.RiderName, &b.RiderPhone,
			&b.PatientName, &b.PatientCondition,
			&b.PickupAddress, &b.PickupLat, &b.PickupLng,
			&b.DropAddress, &b.DropLat, &b.DropLng,
			&b.AmbulanceType, &b.EstimatedFare,
			&b.Status, &b.HospitalRejectedReason, &b.Notes,
			&b.CreatedAt, &b.HospitalName)
		bookings = append(bookings, b)
	}
	if bookings == nil {
		bookings = []HospitalBooking{}
	}
	c.JSON(http.StatusOK, bookings)
}

// POST /gogoo/ambulance/bookings/hospital
func CreateHospitalBooking(c *gin.Context) {
	var req struct {
		HospitalID       string  `json:"hospital_id"`
		RiderName        string  `json:"rider_name"`
		RiderPhone       string  `json:"rider_phone"`
		PatientName      string  `json:"patient_name"`
		PatientCondition string  `json:"patient_condition"`
		PickupAddress    string  `json:"pickup_address"`
		PickupLat        float64 `json:"pickup_lat"`
		PickupLng        float64 `json:"pickup_lng"`
		DropAddress      string  `json:"drop_address"`
		DropLat          float64 `json:"drop_lat"`
		DropLng          float64 `json:"drop_lng"`
		AmbulanceType    string  `json:"ambulance_type"`
		EstimatedFare    float64 `json:"estimated_fare"`
		Notes            string  `json:"notes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	id := uuid.New()

	_, err := pool.Exec(ctx, `
		INSERT INTO hospital_ambulance_bookings
		    (id, hospital_id, rider_name, rider_phone, patient_name, patient_condition,
		     pickup_address, pickup_lat, pickup_lng, drop_address, drop_lat, drop_lng,
		     ambulance_type, estimated_fare, status, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'pending',$15)
	`, id, nullIfEmpty(req.HospitalID), req.RiderName, req.RiderPhone,
		req.PatientName, req.PatientCondition,
		req.PickupAddress, req.PickupLat, req.PickupLng,
		req.DropAddress, req.DropLat, req.DropLng,
		req.AmbulanceType, req.EstimatedFare, req.Notes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create booking"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "message": "Booking created"})
}

// PATCH /gogoo/ambulance/bookings/hospital/:id/status
func UpdateHospitalBookingStatus(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Status         string `json:"status"`
		RejectedReason string `json:"rejected_reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	_, err := pool.Exec(ctx, `
		UPDATE hospital_ambulance_bookings SET
		    status=$1,
		    hospital_rejected_reason=$2,
		    hospital_confirmed_at = CASE WHEN $1='confirmed' THEN NOW() ELSE hospital_confirmed_at END,
		    updated_at=NOW()
		WHERE id=$3
	`, req.Status, req.RejectedReason, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Status updated"})
}

// GET /gogoo/ambulance/hospitals/nearby?lat=X&lng=X&radius_km=50
func GetNearbyHospitals(c *gin.Context) {
	lat := c.Query("lat")
	lng := c.Query("lng")
	if lat == "" || lng == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lat and lng required"})
		return
	}

	radiusKm := 50.0
	if r := c.Query("radius_km"); r != "" {
		if v, err := strconv.ParseFloat(r, 64); err == nil && v > 0 {
			radiusKm = v
		}
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Hospitals without coordinates have no distance and are excluded.
	rows, err := pool.Query(ctx, `
		SELECT * FROM (
			SELECT
				id, name, COALESCE(type,'hospital'), phone,
				COALESCE(address,''), COALESCE(area,''),
				COALESCE(ambulance_types, ARRAY[]::TEXT[]),
				vehicle_count, base_fare, per_km_rate,
				COALESCE(latitude,0), COALESCE(longitude,0),
				COALESCE(rating,0), is_verified,
				ROUND(
					6371 * acos(
						LEAST(1.0, cos(radians($1::float)) * cos(radians(latitude)) *
						cos(radians(longitude) - radians($2::float)) +
						sin(radians($1::float)) * sin(radians(latitude)))
					)::numeric, 2
				) AS distance_km
			FROM ambulance_hospitals
			WHERE is_active = TRUE
		) nearby
		WHERE distance_km <= $3
		ORDER BY distance_km
		LIMIT 10
	`, lat, lng, radiusKm)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()

	type NearbyHospital struct {
		ID             string   `json:"id"`
		Name           string   `json:"name"`
		Type           string   `json:"type"`
		Phone          string   `json:"phone"`
		Address        string   `json:"address"`
		Area           string   `json:"area"`
		AmbulanceTypes []string `json:"ambulance_types"`
		VehicleCount   int      `json:"vehicle_count"`
		BaseFare       float64  `json:"base_fare"`
		PerKmRate      float64  `json:"per_km_rate"`
		Latitude       float64  `json:"latitude"`
		Longitude      float64  `json:"longitude"`
		Rating         float64  `json:"rating"`
		IsVerified     bool     `json:"is_verified"`
		DistanceKm     float64  `json:"distance_km"`
	}

	var hospitals []NearbyHospital
	for rows.Next() {
		var h NearbyHospital
		rows.Scan(&h.ID, &h.Name, &h.Type, &h.Phone,
			&h.Address, &h.Area, &h.AmbulanceTypes,
			&h.VehicleCount, &h.BaseFare, &h.PerKmRate,
			&h.Latitude, &h.Longitude, &h.Rating, &h.IsVerified, &h.DistanceKm)
		hospitals = append(hospitals, h)
	}
	if hospitals == nil {
		hospitals = []NearbyHospital{}
	}
	c.JSON(http.StatusOK, gin.H{"hospitals": hospitals})
}

// ─── Ambulance All-Bookings ───────────────────────────────────────────────────

// GET /gogoo/ambulance/all-bookings — bookings where service is ambulance category
func GetAmbulanceAllBookings(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT b.id, b.status, b.pickup_address, b.drop_address,
		       COALESCE(b.final_fare, b.estimated_fare, 0),
		       b.created_at,
		       COALESCE(u_r.name,'') AS rider_name,
		       COALESCE(r.phone,'') AS rider_phone,
		       st.name AS service_name,
		       COALESCE(st.vehicle_type,''),
		       b.hospital_name,
		       b.ambulance_sub_type,
		       COALESCE(b.is_free_ambulance, FALSE),
		       b.purpose_type
		FROM bookings b
		JOIN riders r ON r.id = b.rider_id
		JOIN users u_r ON u_r.id = r.user_id
		JOIN service_types st ON st.id = b.service_type_id
		WHERE st.category = 'ambulance'
		   OR st.vehicle_type LIKE 'ambulance%'
		ORDER BY b.created_at DESC
		LIMIT 200
	`)
	if err != nil {
		c.JSON(http.StatusOK, []interface{}{})
		return
	}
	defer rows.Close()

	type AmbBooking struct {
		ID               string    `json:"id"`
		Status           string    `json:"status"`
		Pickup           string    `json:"pickup_address"`
		Drop             string    `json:"drop_address"`
		Fare             float64   `json:"fare"`
		CreatedAt        time.Time `json:"created_at"`
		RiderName        string    `json:"rider_name"`
		RiderPhone       string    `json:"rider_phone"`
		ServiceName      string    `json:"service_name"`
		VehicleType      string    `json:"vehicle_type"`
		HospitalName     *string   `json:"hospital_name"`
		AmbulanceSubType *string   `json:"ambulance_sub_type"`
		IsFreeAmbulance  bool      `json:"is_free_ambulance"`
		PurposeType      *string   `json:"purpose_type"`
	}

	var bookings []AmbBooking
	for rows.Next() {
		var b AmbBooking
		rows.Scan(&b.ID, &b.Status, &b.Pickup, &b.Drop, &b.Fare,
			&b.CreatedAt, &b.RiderName, &b.RiderPhone, &b.ServiceName, &b.VehicleType,
			&b.HospitalName, &b.AmbulanceSubType, &b.IsFreeAmbulance, &b.PurposeType)
		bookings = append(bookings, b)
	}
	if bookings == nil {
		bookings = []AmbBooking{}
	}
	c.JSON(http.StatusOK, bookings)
}
