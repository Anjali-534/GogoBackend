package handlers

// Bogie Tracker — company-facing endpoints.
//
// Every handler here is scoped to the calling company via
// middleware.RequireTrackerCompany(), which puts the JWT-derived company id
// into gin context as "company_id" (defense layer 1). Every query in this
// file additionally hard-scopes on WHERE company_id = $N using that same
// context value — never a client-supplied path/query param (defense layer
// 2). This mirrors GetHospitalBookings' scoping rule in ambulance.go. Do
// not add a route here that trusts a client-supplied company id.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// terminal tracker order statuses — no transition is allowed out of these.
var terminalOrderStatuses = map[string]bool{
	"delivered": true,
	"cancelled": true,
}

var validOrderStatuses = map[string]bool{
	"created":    true,
	"loading":    true,
	"loaded":     true,
	"dispatched": true,
	"in_transit": true,
	"delivered":  true,
	"cancelled":  true,
}

// ─── Auth ───────────────────────────────────────────────────────────────────

// POST /gogoo/tracker/signup
func TrackerCompanySignup(c *gin.Context) {
	var req struct {
		CompanyName  string `json:"company_name" binding:"required"`
		ContactPhone string `json:"contact_phone" binding:"required"`
		ContactEmail string `json:"contact_email" binding:"required,email"`
		Password     string `json:"password" binding:"required,min=8"`
		GSTIN        string `json:"gstin"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var count int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM tracker_companies WHERE contact_email=$1", req.ContactEmail).Scan(&count)
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "an account with this email already exists"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash failed"})
		return
	}

	id := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO tracker_companies (id, company_name, contact_phone, contact_email, password_hash, gstin)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, id, req.CompanyName, req.ContactPhone, req.ContactEmail, string(hash), nullIfEmpty(req.GSTIN))
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "23505") {
			c.JSON(http.StatusConflict, gin.H{"error": "an account with this email already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create account: " + err.Error()})
		return
	}

	cfg := c.MustGet("config").(*config.Config)
	sendTrackerSignupEmail(cfg, req.CompanyName, req.ContactEmail)

	c.JSON(http.StatusCreated, gin.H{
		"id":      id,
		"message": "Signup received — your account is pending approval",
	})
}

// POST /gogoo/tracker/login
func TrackerCompanyLogin(c *gin.Context) {
	var req struct {
		Email    string `json:"email" binding:"required,email"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var id, companyName, passwordHash, status string
	err := pool.QueryRow(ctx, `
		SELECT id, company_name, password_hash, status
		FROM tracker_companies WHERE contact_email=$1
	`, req.Email).Scan(&id, &companyName, &passwordHash, &status)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	switch status {
	case "pending":
		c.JSON(http.StatusForbidden, gin.H{"error": "account pending approval", "status": status})
		return
	case "rejected":
		c.JSON(http.StatusForbidden, gin.H{"error": "account rejected", "status": status})
		return
	case "suspended":
		c.JSON(http.StatusForbidden, gin.H{"error": "account suspended", "status": status})
		return
	case "active":
		// proceed
	default:
		c.JSON(http.StatusForbidden, gin.H{"error": "account not active", "status": status})
		return
	}

	cfg := c.MustGet("config").(*config.Config)
	token := signPanelToken(id, req.Email, "company", "tracker_company", cfg.JWTSecret)
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"company": gin.H{
			"id":           id,
			"company_name": companyName,
			"status":       status,
		},
	})
}

// ─── Company profile ────────────────────────────────────────────────────────

// GET /gogoo/tracker/company/profile
func GetTrackerCompanyProfile(c *gin.Context) {
	companyID := c.GetString("company_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var comp TrackerCompany
	var approvedBy *string
	err := pool.QueryRow(ctx, `
		SELECT id, company_name, contact_phone, contact_email,
		       COALESCE(gstin,''), status, approved_by::text, approved_at, created_at
		FROM tracker_companies WHERE id = $1
	`, companyID).Scan(
		&comp.ID, &comp.CompanyName, &comp.ContactPhone, &comp.ContactEmail,
		&comp.GSTIN, &comp.Status, &approvedBy, &comp.ApprovedAt, &comp.CreatedAt,
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "company not found"})
		return
	}
	comp.ApprovedBy = approvedBy
	c.JSON(http.StatusOK, comp)
}

// PATCH /gogoo/tracker/company/profile
func UpdateTrackerCompanyProfile(c *gin.Context) {
	companyID := c.GetString("company_id")
	var req struct {
		CompanyName  string `json:"company_name" binding:"required"`
		ContactPhone string `json:"contact_phone" binding:"required"`
		GSTIN        string `json:"gstin"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	_, err := pool.Exec(ctx, `
		UPDATE tracker_companies
		SET company_name=$1, contact_phone=$2, gstin=$3, updated_at=NOW()
		WHERE id=$4
	`, req.CompanyName, req.ContactPhone, nullIfEmpty(req.GSTIN), companyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "profile updated"})
}

// POST /gogoo/tracker/company/password
func UpdateTrackerCompanyPassword(c *gin.Context) {
	companyID := c.GetString("company_id")
	var req struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required,min=8"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var currentHash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM tracker_companies WHERE id=$1`, companyID).Scan(&currentHash); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "company not found"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(req.OldPassword)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "current password is incorrect"})
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash failed"})
		return
	}
	_, err = pool.Exec(ctx, `UPDATE tracker_companies SET password_hash=$1, updated_at=NOW() WHERE id=$2`, string(newHash), companyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "password updated"})
}

// ─── Drivers ────────────────────────────────────────────────────────────────

// GET /gogoo/tracker/drivers
func ListTrackerCompanyOwnDrivers(c *gin.Context) {
	companyID := c.GetString("company_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	query := `
		SELECT id, company_id, driver_name, phone,
		       COALESCE(vehicle_number,''), COALESCE(transporter_name,''),
		       COALESCE(transporter_phone,''), is_active, created_at
		FROM tracker_drivers
		WHERE company_id = $1`
	if c.Query("include_inactive") != "true" {
		query += " AND is_active = true"
	}
	query += " ORDER BY created_at DESC"

	rows, err := pool.Query(ctx, query, companyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()

	var drivers []TrackerDriver
	for rows.Next() {
		var d TrackerDriver
		if err := rows.Scan(
			&d.ID, &d.CompanyID, &d.DriverName, &d.Phone,
			&d.VehicleNumber, &d.TransporterName, &d.TransporterPhone,
			&d.IsActive, &d.CreatedAt,
		); err != nil {
			continue
		}
		drivers = append(drivers, d)
	}
	if drivers == nil {
		drivers = []TrackerDriver{}
	}
	c.JSON(http.StatusOK, drivers)
}

// POST /gogoo/tracker/drivers
func CreateTrackerCompanyDriver(c *gin.Context) {
	companyID := c.GetString("company_id")
	var req struct {
		DriverName       string `json:"driver_name" binding:"required"`
		Phone            string `json:"phone" binding:"required"`
		VehicleNumber    string `json:"vehicle_number"`
		TransporterName  string `json:"transporter_name"`
		TransporterPhone string `json:"transporter_phone"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	id := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO tracker_drivers (id, company_id, driver_name, phone, vehicle_number, transporter_name, transporter_phone)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
	`, id, companyID, req.DriverName, req.Phone,
		nullIfEmpty(req.VehicleNumber), nullIfEmpty(req.TransporterName), nullIfEmpty(req.TransporterPhone))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create driver: " + err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "message": "driver added"})
}

// PATCH /gogoo/tracker/drivers/:id
func UpdateTrackerCompanyDriver(c *gin.Context) {
	companyID := c.GetString("company_id")
	driverID := c.Param("id")
	var req struct {
		DriverName       string `json:"driver_name" binding:"required"`
		Phone            string `json:"phone" binding:"required"`
		VehicleNumber    string `json:"vehicle_number"`
		TransporterName  string `json:"transporter_name"`
		TransporterPhone string `json:"transporter_phone"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	tag, err := pool.Exec(ctx, `
		UPDATE tracker_drivers
		SET driver_name=$1, phone=$2, vehicle_number=$3, transporter_name=$4, transporter_phone=$5, updated_at=NOW()
		WHERE id=$6 AND company_id=$7
	`, req.DriverName, req.Phone, nullIfEmpty(req.VehicleNumber), nullIfEmpty(req.TransporterName), nullIfEmpty(req.TransporterPhone),
		driverID, companyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "driver not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "driver updated"})
}

// DELETE /gogoo/tracker/drivers/:id — soft delete (is_active=false)
func DeactivateTrackerCompanyDriver(c *gin.Context) {
	companyID := c.GetString("company_id")
	driverID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	tag, err := pool.Exec(ctx, `
		UPDATE tracker_drivers SET is_active=false, updated_at=NOW()
		WHERE id=$1 AND company_id=$2
	`, driverID, companyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "driver not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "driver deactivated"})
}

// ─── Orders ─────────────────────────────────────────────────────────────────

// generateTrackingToken returns a crypto-random, non-guessable public
// tracking token — the only protection on the unauthenticated tracking page.
func generateTrackingToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// fetchLocationPings returns an order's route trail, oldest first, for
// drawing the polyline on the map.
func fetchLocationPings(ctx context.Context, pool *pgxpool.Pool, orderID string) ([]TrackerLocationPing, error) {
	rows, err := pool.Query(ctx, `
		SELECT lat, lng, created_at
		FROM tracker_location_pings
		WHERE order_id = $1
		ORDER BY created_at ASC
	`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pings := []TrackerLocationPing{}
	for rows.Next() {
		var p TrackerLocationPing
		if err := rows.Scan(&p.Lat, &p.Lng, &p.CreatedAt); err != nil {
			continue
		}
		pings = append(pings, p)
	}
	return pings, nil
}

// GET /gogoo/tracker/orders
func ListTrackerCompanyOwnOrders(c *gin.Context) {
	companyID := c.GetString("company_id")
	status := c.Query("status")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	query := `
		SELECT id, company_id, booked_for_company_name, booked_for_phone,
		       dispatch_from, dispatch_to,
		       COALESCE(transporter_name,''), COALESCE(transporter_phone,''),
		       driver_id::text, COALESCE(driver_name,''), COALESCE(driver_phone,''),
		       vehicle_number, COALESCE(eway_bill_number,''), COALESCE(eway_bill_file_url,''),
		       status, public_tracking_token, created_at,
		       consignee_name, material, quantity, dispatch_datetime, documents_enclosed
		FROM tracker_orders
		WHERE company_id = $1`
	args := []interface{}{companyID}
	if status != "" {
		query += " AND status = $2"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC"

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()

	var orders []TrackerOrder
	for rows.Next() {
		var o TrackerOrder
		var driverID *string
		if err := rows.Scan(
			&o.ID, &o.CompanyID, &o.BookedForCompanyName, &o.BookedForPhone,
			&o.DispatchFrom, &o.DispatchTo,
			&o.TransporterName, &o.TransporterPhone,
			&driverID, &o.DriverName, &o.DriverPhone,
			&o.VehicleNumber, &o.EwayBillNumber, &o.EwayBillFileURL,
			&o.Status, &o.PublicTrackingToken, &o.CreatedAt,
			&o.ConsigneeName, &o.Material, &o.Quantity, &o.DispatchDatetime, &o.DocumentsEnclosed,
		); err != nil {
			continue
		}
		o.DriverID = driverID
		orders = append(orders, o)
	}
	if orders == nil {
		orders = []TrackerOrder{}
	}
	c.JSON(http.StatusOK, orders)
}

// POST /gogoo/tracker/orders
func CreateTrackerCompanyOrder(c *gin.Context) {
	companyID := c.GetString("company_id")
	var req struct {
		BookedForCompanyName string   `json:"booked_for_company_name" binding:"required"`
		BookedForPhone       string   `json:"booked_for_phone" binding:"required"`
		DispatchFrom         string   `json:"dispatch_from" binding:"required"`
		DispatchTo           string   `json:"dispatch_to" binding:"required"`
		DispatchFromLat      *float64 `json:"dispatch_from_lat"`
		DispatchFromLng      *float64 `json:"dispatch_from_lng"`
		DispatchToLat        *float64 `json:"dispatch_to_lat"`
		DispatchToLng        *float64 `json:"dispatch_to_lng"`
		TransporterName      string   `json:"transporter_name"`
		TransporterPhone     string   `json:"transporter_phone"`
		DriverID             *string  `json:"driver_id"`
		VehicleNumber        string   `json:"vehicle_number" binding:"required"`
		EwayBillNumber       string   `json:"eway_bill_number"`

		// Dispatch details — from the real dispatch sheet, all optional.
		ConsigneeName     string     `json:"consignee_name"`
		Material          string     `json:"material"`
		Quantity          string     `json:"quantity"`
		DispatchDatetime  *time.Time `json:"dispatch_datetime"`
		DocumentsEnclosed string     `json:"documents_enclosed"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// If a driver is attached, it must belong to this company and snapshot
	// its name/phone onto the order at creation time.
	var driverName, driverPhone *string
	if req.DriverID != nil && *req.DriverID != "" {
		var name, phone string
		err := pool.QueryRow(ctx, `
			SELECT driver_name, phone FROM tracker_drivers WHERE id=$1 AND company_id=$2
		`, *req.DriverID, companyID).Scan(&name, &phone)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "driver not found"})
			return
		}
		driverName = &name
		driverPhone = &phone
	}

	token, err := generateTrackingToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate tracking token"})
		return
	}

	id := uuid.New()
	tx, err := pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO tracker_orders
			(id, company_id, booked_for_company_name, booked_for_phone,
			 dispatch_from, dispatch_to, dispatch_from_lat, dispatch_from_lng,
			 dispatch_to_lat, dispatch_to_lng, transporter_name, transporter_phone,
			 driver_id, driver_name, driver_phone, vehicle_number,
			 eway_bill_number, status, public_tracking_token,
			 consignee_name, material, quantity, dispatch_datetime, documents_enclosed)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,'created',$18,$19,$20,$21,$22,$23)
	`, id, companyID, req.BookedForCompanyName, req.BookedForPhone,
		req.DispatchFrom, req.DispatchTo, req.DispatchFromLat, req.DispatchFromLng,
		req.DispatchToLat, req.DispatchToLng, nullIfEmpty(req.TransporterName), nullIfEmpty(req.TransporterPhone),
		req.DriverID, driverName, driverPhone, req.VehicleNumber,
		nullIfEmpty(req.EwayBillNumber), token,
		nullIfEmpty(req.ConsigneeName), nullIfEmpty(req.Material), nullIfEmpty(req.Quantity),
		req.DispatchDatetime, nullIfEmpty(req.DocumentsEnclosed))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order: " + err.Error()})
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO tracker_order_events (id, order_id, status)
		VALUES ($1,$2,'created')
	`, uuid.New(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order event"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": id, "public_tracking_token": token, "message": "order created"})
}

// GET /gogoo/tracker/orders/:id
func GetTrackerCompanyOwnOrder(c *gin.Context) {
	companyID := c.GetString("company_id")
	orderID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var o TrackerOrder
	var driverID *string
	err := pool.QueryRow(ctx, `
		SELECT id, company_id, booked_for_company_name, booked_for_phone,
		       dispatch_from, dispatch_to,
		       COALESCE(transporter_name,''), COALESCE(transporter_phone,''),
		       driver_id::text, COALESCE(driver_name,''), COALESCE(driver_phone,''),
		       vehicle_number, COALESCE(eway_bill_number,''), COALESCE(eway_bill_file_url,''),
		       status, public_tracking_token, created_at,
		       consignee_name, material, quantity, dispatch_datetime, documents_enclosed,
		       driver_tracking_token, last_lat, last_lng, last_location_at,
		       dispatch_from_lat, dispatch_from_lng, dispatch_to_lat, dispatch_to_lng
		FROM tracker_orders
		WHERE id = $1 AND company_id = $2
	`, orderID, companyID).Scan(
		&o.ID, &o.CompanyID, &o.BookedForCompanyName, &o.BookedForPhone,
		&o.DispatchFrom, &o.DispatchTo,
		&o.TransporterName, &o.TransporterPhone,
		&driverID, &o.DriverName, &o.DriverPhone,
		&o.VehicleNumber, &o.EwayBillNumber, &o.EwayBillFileURL,
		&o.Status, &o.PublicTrackingToken, &o.CreatedAt,
		&o.ConsigneeName, &o.Material, &o.Quantity, &o.DispatchDatetime, &o.DocumentsEnclosed,
		&o.DriverTrackingToken, &o.LastLat, &o.LastLng, &o.LastLocationAt,
		&o.DispatchFromLat, &o.DispatchFromLng, &o.DispatchToLat, &o.DispatchToLng,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		} else {
			log.Printf("GetTrackerCompanyOwnOrder: scan failed for order=%s company=%s: %v", orderID, companyID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		}
		return
	}
	o.DriverID = driverID

	rows, err := pool.Query(ctx, `
		SELECT id, order_id, status, COALESCE(note,''), COALESCE(location,''), created_at
		FROM tracker_order_events
		WHERE order_id = $1
		ORDER BY created_at ASC
	`, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()

	var events []TrackerOrderEvent
	for rows.Next() {
		var e TrackerOrderEvent
		if err := rows.Scan(&e.ID, &e.OrderID, &e.Status, &e.Note, &e.Location, &e.CreatedAt); err != nil {
			continue
		}
		events = append(events, e)
	}
	if events == nil {
		events = []TrackerOrderEvent{}
	}

	pings, err := fetchLocationPings(ctx, pool, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"order": o, "events": events, "location_pings": pings})
}

// PATCH /gogoo/tracker/orders/:id — status transition + event log entry.
// Terminal states (delivered, cancelled) cannot transition to anything else.
func UpdateTrackerCompanyOrderStatus(c *gin.Context) {
	companyID := c.GetString("company_id")
	orderID := c.Param("id")
	var req struct {
		Status   string `json:"status" binding:"required"`
		Note     string `json:"note"`
		Location string `json:"location"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validOrderStatuses[req.Status] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status"})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var currentStatus string
	var driverTrackingToken *string
	if err := pool.QueryRow(ctx, `
		SELECT status, driver_tracking_token FROM tracker_orders WHERE id=$1 AND company_id=$2
	`, orderID, companyID).Scan(&currentStatus, &driverTrackingToken); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}
	if terminalOrderStatuses[currentStatus] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order is in a terminal state (" + currentStatus + ") and cannot be updated"})
		return
	}

	// The driver's share-link token is generated the first time an order
	// moves to 'dispatched' — that's the point the driver actually needs a
	// link to send from. Never regenerated on later transitions.
	newDriverToken := ""
	if req.Status == "dispatched" && driverTrackingToken == nil {
		token, err := generateTrackingToken()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate driver tracking token"})
			return
		}
		newDriverToken = token
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback(ctx)

	var tag interface{ RowsAffected() int64 }
	if newDriverToken != "" {
		tag, err = tx.Exec(ctx, `
			UPDATE tracker_orders SET status=$1, driver_tracking_token=$2, updated_at=NOW() WHERE id=$3 AND company_id=$4
		`, req.Status, newDriverToken, orderID, companyID)
	} else {
		tag, err = tx.Exec(ctx, `
			UPDATE tracker_orders SET status=$1, updated_at=NOW() WHERE id=$2 AND company_id=$3
		`, req.Status, orderID, companyID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO tracker_order_events (id, order_id, status, note, location)
		VALUES ($1,$2,$3,$4,$5)
	`, uuid.New(), orderID, req.Status, nullIfEmpty(req.Note), nullIfEmpty(req.Location))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to log event"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit"})
		return
	}

	resp := gin.H{"message": "status updated"}
	if newDriverToken != "" {
		resp["driver_tracking_token"] = newDriverToken
	}
	c.JSON(http.StatusOK, resp)
}

// POST /gogoo/tracker/orders/:id/events — add a note/location event without
// changing status (e.g. a live location ping).
func AddTrackerCompanyOrderEvent(c *gin.Context) {
	companyID := c.GetString("company_id")
	orderID := c.Param("id")
	var req struct {
		Note     string `json:"note"`
		Location string `json:"location"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var currentStatus string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM tracker_orders WHERE id=$1 AND company_id=$2
	`, orderID, companyID).Scan(&currentStatus); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	_, err := pool.Exec(ctx, `
		INSERT INTO tracker_order_events (id, order_id, status, note, location)
		VALUES ($1,$2,$3,$4,$5)
	`, uuid.New(), orderID, currentStatus, nullIfEmpty(req.Note), nullIfEmpty(req.Location))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add event"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "event added"})
}

// POST /gogoo/tracker/orders/:id/eway-bill  (multipart/form-data, field "file")
// Reuses the same Cloudinary/local-disk upload pattern as UploadDriverDocument
// in documents.go.
func UploadTrackerOrderEwayBill(c *gin.Context) {
	companyID := c.GetString("company_id")
	orderID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM tracker_orders WHERE id=$1 AND company_id=$2)
	`, orderID, companyID).Scan(&exists); err != nil || !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	if err := c.Request.ParseMultipartForm(maxFileSize); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file too large — max 10MB allowed"})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	defer file.Close()

	mimeType := header.Header.Get("Content-Type")
	if idx := strings.Index(mimeType, ";"); idx > 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	ext, allowed := allowedMimeTypes[mimeType]
	if !allowed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only JPG, PNG and PDF files allowed"})
		return
	}
	if header.Size > maxFileSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file must be under 10MB"})
		return
	}

	var fileURL string
	if os.Getenv("CLOUDINARY_CLOUD_NAME") != "" {
		secureURL, err := uploadToCloudinary(ctx, file, header.Filename, "eway_bill", orderID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "cloud storage error: " + err.Error()})
			return
		}
		fileURL = secureURL
	} else {
		uploadDir := filepath.Join("uploads", "tracker", companyID)
		if err := os.MkdirAll(uploadDir, 0755); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "storage error"})
			return
		}
		localName := "eway_" + orderID + "_" + uuid.New().String()[:8] + ext
		filePath := filepath.Join(uploadDir, localName)
		dst, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save file"})
			return
		}
		_, err = dst.ReadFrom(file)
		dst.Close()
		if err != nil {
			os.Remove(filePath)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to write file"})
			return
		}
		fileURL = "/uploads/tracker/" + companyID + "/" + localName
	}

	_, err = pool.Exec(ctx, `
		UPDATE tracker_orders SET eway_bill_file_url=$1, updated_at=NOW() WHERE id=$2 AND company_id=$3
	`, fileURL, orderID, companyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"file_url": fileURL, "message": "e-way bill uploaded"})
}

// ─── Public tracking ────────────────────────────────────────────────────────

// GET /gogoo/public/tracker/orders/:token — unauthenticated. Returns only
// receiver-relevant fields: no company email/GSTIN/financials/internal ids.
func GetPublicTrackerOrder(c *gin.Context) {
	token := c.Param("token")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var orderID, status, dispatchFrom, dispatchTo, vehicleNumber string
	var transporterName, transporterPhone, driverName, driverPhone *string
	var consigneeName, material, quantity *string
	var dispatchDatetime *time.Time
	var lastLat, lastLng *float64
	var lastLocationAt *time.Time
	var dispatchFromLat, dispatchFromLng, dispatchToLat, dispatchToLng *float64
	err := pool.QueryRow(ctx, `
		SELECT id, status, dispatch_from, dispatch_to, vehicle_number,
		       transporter_name, transporter_phone, driver_name, driver_phone,
		       consignee_name, material, quantity, dispatch_datetime,
		       last_lat, last_lng, last_location_at,
		       dispatch_from_lat, dispatch_from_lng, dispatch_to_lat, dispatch_to_lng
		FROM tracker_orders WHERE public_tracking_token = $1
	`, token).Scan(&orderID, &status, &dispatchFrom, &dispatchTo, &vehicleNumber,
		&transporterName, &transporterPhone, &driverName, &driverPhone,
		&consigneeName, &material, &quantity, &dispatchDatetime,
		&lastLat, &lastLng, &lastLocationAt,
		&dispatchFromLat, &dispatchFromLng, &dispatchToLat, &dispatchToLng)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tracking link not found"})
		return
	}

	rows, err := pool.Query(ctx, `
		SELECT e.status, COALESCE(e.note,''), COALESCE(e.location,''), e.created_at
		FROM tracker_order_events e
		JOIN tracker_orders o ON o.id = e.order_id
		WHERE o.public_tracking_token = $1
		ORDER BY e.created_at ASC
	`, token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	type publicEvent struct {
		Status    string    `json:"status"`
		Note      string    `json:"note"`
		Location  string    `json:"location"`
		CreatedAt time.Time `json:"created_at"`
	}
	var events []publicEvent
	for rows.Next() {
		var e publicEvent
		if err := rows.Scan(&e.Status, &e.Note, &e.Location, &e.CreatedAt); err != nil {
			continue
		}
		events = append(events, e)
	}
	if events == nil {
		events = []publicEvent{}
	}

	pings, err := fetchLocationPings(ctx, pool, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":            status,
		"dispatch_from":     dispatchFrom,
		"dispatch_to":       dispatchTo,
		"vehicle_number":    vehicleNumber,
		"transporter_name":  transporterName,
		"transporter_phone": transporterPhone,
		"driver_name":       driverName,
		"driver_phone":      driverPhone,
		"consignee_name":    consigneeName,
		"material":          material,
		"quantity":          quantity,
		"dispatch_datetime": dispatchDatetime,
		"events":            events,
		"last_lat":          lastLat,
		"last_lng":          lastLng,
		"last_location_at":  lastLocationAt,
		"location_pings":    pings,
		"dispatch_from_lat": dispatchFromLat,
		"dispatch_from_lng": dispatchFromLng,
		"dispatch_to_lat":   dispatchToLat,
		"dispatch_to_lng":   dispatchToLng,
	})
}

// ─── Driver share-link (public, token-gated) ───────────────────────────────

// GET /gogoo/public/tracker/driver/:driver_token — unauthenticated. Returns
// the route summary + status the driver's share page needs to render itself.
// No customer/company financial or contact fields — the driver already
// knows who they are, this is just enough context to confirm the right
// order and show the route while they share their location.
func GetTrackerDriverOrder(c *gin.Context) {
	driverToken := c.Param("driver_token")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var status, dispatchFrom, dispatchTo, vehicleNumber string
	err := pool.QueryRow(ctx, `
		SELECT status, dispatch_from, dispatch_to, vehicle_number
		FROM tracker_orders WHERE driver_tracking_token = $1
	`, driverToken).Scan(&status, &dispatchFrom, &dispatchTo, &vehicleNumber)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tracking link not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":         status,
		"dispatch_from":  dispatchFrom,
		"dispatch_to":    dispatchTo,
		"vehicle_number": vehicleNumber,
		"is_terminal":    terminalOrderStatuses[status],
	})
}

// POST /gogoo/public/tracker/driver/:driver_token/location — unauthenticated,
// token-gated. Updates the order's last-known location and appends to the
// route trail. Rejects once the order has reached a terminal state — the
// driver's page should stop sending once the trip is over.
func PostTrackerDriverLocation(c *gin.Context) {
	driverToken := c.Param("driver_token")
	var req struct {
		Lat *float64 `json:"lat" binding:"required"`
		Lng *float64 `json:"lng" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if *req.Lat < -90 || *req.Lat > 90 || *req.Lng < -180 || *req.Lng > 180 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lat/lng out of range"})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var orderID, status string
	if err := pool.QueryRow(ctx, `
		SELECT id, status FROM tracker_orders WHERE driver_tracking_token = $1
	`, driverToken).Scan(&orderID, &status); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tracking link not found"})
		return
	}
	if terminalOrderStatuses[status] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order is in a terminal state (" + status + ") and is no longer tracked"})
		return
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE tracker_orders SET last_lat=$1, last_lng=$2, last_location_at=NOW() WHERE id=$3
	`, *req.Lat, *req.Lng, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO tracker_location_pings (id, order_id, lat, lng)
		VALUES ($1,$2,$3,$4)
	`, uuid.New(), orderID, *req.Lat, *req.Lng)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to log ping"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "location updated"})
}
