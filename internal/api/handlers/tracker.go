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
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/dateutil"
	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/services/trackerbilling"
	"github.com/deploykit/backend/internal/services/trackerrider"
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

// validOrderPriorities matches the CHECK constraint added in migration 043.
// Empty request input defaults to "normal" rather than being rejected.
var validOrderPriorities = map[string]bool{
	"normal":   true,
	"urgent":   true,
	"same_day": true,
}

// validDriverEventKinds are the driver-reported quick-status taps from the
// drive page — notes at the order's CURRENT status, never a status
// transition. Must match the CHECK constraint added in migration 028.
// 'delivery_claimed' is the special one: paired with an uploaded signature
// (see UploadTrackerDriverSignature), it's what prompts the company to run
// the actual 'delivered' transition via the normal status-update endpoint.
var validDriverEventKinds = map[string]bool{
	"on_break":         true,
	"about_to_reach":   true,
	"reached":          true,
	"unloading":        true,
	"delivery_claimed": true,
}

// ─── Auth ───────────────────────────────────────────────────────────────────

// otpTTL is how long an email OTP stays valid after being (re)issued.
// otpResendCooldown gates /resend-otp: if more than otpTTL-otpResendCooldown
// remains on the current code's expiry, one was just sent — this is a
// cheap, stateless rate limit (no extra column, derived from the existing
// expiry) rather than a real per-IP/per-account limiter.
const otpTTL = 10 * time.Minute
const otpResendCooldown = 30 * time.Second

// generateOTPCode returns a crypto-random 6-digit numeric code, zero-padded.
func generateOTPCode() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint32(buf) % 1000000
	return fmt.Sprintf("%06d", n), nil
}

// trackerCredentialCharset and trackerLicenseCharset exclude visually
// ambiguous characters (0/O, 1/I/l) since these are manually typed into a
// login form.
const trackerCredentialCharset = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
const trackerLicenseCharset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// generateRandomPassword produces the system-generated password issued to a
// company when its account is auto-activated on payment (see
// MarkTrackerPlanOrderPaid). The company can change it afterward via the
// existing change-password endpoint.
func generateRandomPassword() (string, error) {
	const length = 14
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, length)
	for i, b := range buf {
		out[i] = trackerCredentialCharset[int(b)%len(trackerCredentialCharset)]
	}
	return string(out), nil
}

// generateTrackerLicenseKey produces a BGT-XXXX-XXXX-XXXX license key,
// issued once when a company is auto-activated on payment.
func generateTrackerLicenseKey() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	chars := make([]byte, 12)
	for i, b := range buf {
		chars[i] = trackerLicenseCharset[int(b)%len(trackerLicenseCharset)]
	}
	return fmt.Sprintf("BGT-%s-%s-%s", chars[0:4], chars[4:8], chars[8:12]), nil
}

// POST /gogoo/tracker/signup
func TrackerCompanySignup(c *gin.Context) {
	var req struct {
		CompanyName  string `json:"company_name" binding:"required"`
		ContactPhone string `json:"contact_phone"`
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
	`, id, req.CompanyName, nullIfEmpty(req.ContactPhone), req.ContactEmail, string(hash), nullIfEmpty(req.GSTIN))
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

// POST /gogoo/tracker/verify-email
func VerifyTrackerCompanyEmail(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
		Code  string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var id, storedCode string
	var expiresAt *time.Time
	var alreadyVerified bool
	err := pool.QueryRow(ctx, `
		SELECT id, COALESCE(email_otp_code,''), email_otp_expires_at, email_verified
		FROM tracker_companies WHERE contact_email=$1
	`, req.Email).Scan(&id, &storedCode, &expiresAt, &alreadyVerified)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no account found with this email"})
		return
	}
	if alreadyVerified {
		c.JSON(http.StatusOK, gin.H{"message": "email already verified"})
		return
	}
	if storedCode == "" || expiresAt == nil || time.Now().After(*expiresAt) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code expired — request a new one"})
		return
	}
	if req.Code != storedCode {
		c.JSON(http.StatusBadRequest, gin.H{"error": "incorrect code"})
		return
	}

	_, err = pool.Exec(ctx, `
		UPDATE tracker_companies
		SET email_verified=true, email_otp_code=NULL, email_otp_expires_at=NULL
		WHERE id=$1
	`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "verification failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "email verified"})
}

// POST /gogoo/tracker/resend-otp
func ResendTrackerCompanyOTP(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var id, companyName string
	var expiresAt *time.Time
	var verified bool
	err := pool.QueryRow(ctx, `
		SELECT id, company_name, email_otp_expires_at, email_verified
		FROM tracker_companies WHERE contact_email=$1
	`, req.Email).Scan(&id, &companyName, &expiresAt, &verified)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no account found with this email"})
		return
	}
	if verified {
		c.JSON(http.StatusOK, gin.H{"message": "email already verified"})
		return
	}
	if expiresAt != nil && time.Until(*expiresAt) > otpTTL-otpResendCooldown {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "please wait a moment before requesting another code"})
		return
	}

	code, err := generateOTPCode()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate code"})
		return
	}
	if _, err := pool.Exec(ctx, `
		UPDATE tracker_companies SET email_otp_code=$1, email_otp_expires_at=$2 WHERE id=$3
	`, code, time.Now().Add(otpTTL), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resend code"})
		return
	}

	cfg := c.MustGet("config").(*config.Config)
	sendTrackerOTPEmail(cfg, companyName, req.Email, code)

	c.JSON(http.StatusOK, gin.H{"message": "verification code resent"})
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

	var id, companyID, companyName, passwordHash, status string
	isOwner := true
	err := pool.QueryRow(ctx, `
		SELECT id, company_name, password_hash, status
		FROM tracker_companies WHERE contact_email=$1
	`, req.Email).Scan(&id, &companyName, &passwordHash, &status)
	switch {
	case err == nil:
		companyID = id
	case errors.Is(err, pgx.ErrNoRows):
		// No owner account with this email — try staff. Emails are only
		// unique per-company (not globally), so two different companies
		// could each have a staff login with the same address; fetch every
		// matching row and bcrypt-check each rather than picking one before
		// knowing which password actually matches.
		rows, qErr := pool.Query(ctx, `
			SELECT s.id, s.company_id, s.password_hash, c.company_name, c.status
			FROM tracker_staff_users s
			JOIN tracker_companies c ON c.id = s.company_id
			WHERE s.email = $1
		`, req.Email)
		if qErr != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}
		defer rows.Close()

		matched := false
		for rows.Next() {
			var sID, sCompanyID, sHash, sCompanyName, sStatus string
			if scanErr := rows.Scan(&sID, &sCompanyID, &sHash, &sCompanyName, &sStatus); scanErr != nil {
				continue
			}
			if bcrypt.CompareHashAndPassword([]byte(sHash), []byte(req.Password)) == nil {
				id, companyID, companyName, status = sID, sCompanyID, sCompanyName, sStatus
				isOwner = false
				matched = true
				break
			}
		}
		if !matched {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}
	default:
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	if isOwner && bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)) != nil {
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
	token := signPanelToken(id, req.Email, "company", "tracker_company", cfg.JWTSecret, companyID, isOwner)
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"company": gin.H{
			"id":           companyID,
			"company_name": companyName,
			"status":       status,
			"is_owner":     isOwner,
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
		SELECT id, company_name, COALESCE(contact_phone,''), contact_email,
		       COALESCE(gstin,''), status, approved_by::text, approved_at, created_at,
		       notification_email, logo_url, current_plan, subscription_expires_at
		FROM tracker_companies WHERE id = $1
	`, companyID).Scan(
		&comp.ID, &comp.CompanyName, &comp.ContactPhone, &comp.ContactEmail,
		&comp.GSTIN, &comp.Status, &approvedBy, &comp.ApprovedAt, &comp.CreatedAt,
		&comp.NotificationEmail, &comp.LogoURL, &comp.CurrentPlan, &comp.SubscriptionExpiresAt,
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
		CompanyName       string `json:"company_name" binding:"required"`
		ContactPhone      string `json:"contact_phone" binding:"required"`
		GSTIN             string `json:"gstin"`
		NotificationEmail string `json:"notification_email" binding:"omitempty,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	_, err := pool.Exec(ctx, `
		UPDATE tracker_companies
		SET company_name=$1, contact_phone=$2, gstin=$3, notification_email=$4, updated_at=NOW()
		WHERE id=$5
	`, req.CompanyName, req.ContactPhone, nullIfEmpty(req.GSTIN), nullIfEmpty(req.NotificationEmail), companyID)
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

// ─── Rides ──────────────────────────────────────────────────────────────────

// POST /gogoo/tracker/companies/rides
//
// Lets a Bogie Tracker company book a ride through the same rider-booking
// pipeline used by the consumer app, without needing a real rider account —
// the company books "as" its own synthetic rider, provisioned lazily by
// trackerrider.EnsureTrackerCompanyRiderProfile on first use. Binds the
// exact same createBookingRequest CreateBooking does (gogoo.go) and hands
// off to the same createBookingCore, so validation, the server-side fare
// engine, promo handling, and dispatch never diverge between a normal rider
// booking and a tracker-company one — only how rider_id is resolved differs.
func CreateTrackerCompanyRide(c *gin.Context) {
	var req createBookingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("CreateTrackerCompanyRide bind error: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	companyID := c.GetString("company_id")
	ctx := context.Background()

	riderID, err := trackerrider.EnsureTrackerCompanyRiderProfile(ctx, companyID)
	if err != nil {
		log.Printf("CreateTrackerCompanyRide: ensure rider profile failed for company=%s: %v", companyID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to prepare booking profile"})
		return
	}

	pool := db.GetDB().GetPool()
	createBookingCore(c, ctx, pool, riderID, req)
}

// GET /gogoo/tracker/companies/rides
//
// "My Rides" for a tracker company — every booking made through
// CreateTrackerCompanyRide by this company's synthetic rider. Cannot reuse
// ListRiderBookings directly: that endpoint derives its rider from the
// caller's own JWT user_id, which for a tracker-company token is the
// staff/owner's user_id, not the synthetic rider's — so it would just come
// back empty. Uses trackerrider.GetTrackerCompanyRiderID (no auto-create) so
// a company that has never booked anything gets an empty list, not a
// freshly-provisioned rider as a side effect of viewing this page.
func ListTrackerCompanyRides(c *gin.Context) {
	companyID := c.GetString("company_id")
	ctx := context.Background()

	riderID, err := trackerrider.GetTrackerCompanyRiderID(ctx, companyID)
	if err != nil {
		log.Printf("ListTrackerCompanyRides: lookup failed for company=%s: %v", companyID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load rides"})
		return
	}
	if riderID == "" {
		c.JSON(http.StatusOK, []map[string]interface{}{})
		return
	}

	pool := db.GetDB().GetPool()
	bookings, err := listBookingsForRider(ctx, pool, riderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	c.JSON(http.StatusOK, bookings)
}

// GET /gogoo/tracker/companies/rides/:id
//
// Tracking/detail view for a single company-booked ride. Cannot reuse
// GetBooking directly for the same reason as above — its ownership fallback
// (bookingCallerRole) matches the caller's own user_id against the
// booking's rider/driver, which will never be the synthetic rider for a
// tracker-company caller. Instead this does its own ownership check —
// booking.rider_id must equal this company's synthetic_rider_id — then
// renders via the exact same writeBookingDetail (tracking.go) GetBooking
// uses, so the response shape (status, driver info, fare, live driver GPS
// once assigned) is identical either way.
func GetTrackerCompanyRide(c *gin.Context) {
	companyID := c.GetString("company_id")
	bookingID := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	riderID, err := trackerrider.GetTrackerCompanyRiderID(ctx, companyID)
	if err != nil {
		log.Printf("GetTrackerCompanyRide: lookup failed for company=%s: %v", companyID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load ride"})
		return
	}

	var ownerRiderID string
	if err := pool.QueryRow(ctx, `SELECT rider_id FROM bookings WHERE id = $1`, bookingID).Scan(&ownerRiderID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
		return
	}
	if riderID == "" || ownerRiderID != riderID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	writeBookingDetail(c, ctx, pool, bookingID)
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

	// Daily dispatch-limit enforcement, checked before anything else so a
	// company over its limit fails fast without wasted body-parsing/driver
	// lookups. current_plan IS NULL (never paid, or pre-dates migration 038)
	// blocks entirely — no plan means no service, same policy as an expired
	// subscription. See trackerbilling.DispatchLimit for the per-plan caps.
	preCtx := context.Background()
	prePool := db.GetDB().GetPool()
	var currentPlan *string
	if err := prePool.QueryRow(preCtx, `
		SELECT current_plan FROM tracker_companies WHERE id = $1
	`, companyID).Scan(&currentPlan); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load company plan"})
		return
	}
	if currentPlan == nil {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "no active plan — subscribe to a Bogie Tracker plan to create dispatches",
			"code":  "no_active_plan",
		})
		return
	}
	limit, unlimited, ok := trackerbilling.DispatchLimit(*currentPlan)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "unrecognized plan"})
		return
	}
	if !unlimited {
		year, month, day := time.Now().In(dateutil.ISTLocation).Date()
		startOfDay := time.Date(year, month, day, 0, 0, 0, 0, dateutil.ISTLocation)

		var todayCount int
		if err := prePool.QueryRow(preCtx, `
			SELECT COUNT(*) FROM tracker_orders WHERE company_id = $1 AND created_at >= $2
		`, companyID, startOfDay).Scan(&todayCount); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check dispatch count"})
			return
		}
		if todayCount >= limit {
			c.JSON(http.StatusForbidden, gin.H{
				"error":            fmt.Sprintf("You've reached your daily dispatch limit (%d). Upgrade your plan to create more.", limit),
				"code":             "dispatch_limit_reached",
				"daily_limit":      limit,
				"dispatches_today": todayCount,
			})
			return
		}
	}

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

		// Dispatch notification email recipients, all optional.
		BookedForEmail   string `json:"booked_for_email" binding:"omitempty,email"`
		ConsigneeEmail   string `json:"consignee_email" binding:"omitempty,email"`
		TransporterEmail string `json:"transporter_email" binding:"omitempty,email"`

		// GSTIN for the two other dispatch-sheet parties, all optional.
		// Format/checksum is validated client-side only (GSTInput component).
		ConsigneeGstin string `json:"consignee_gstin"`
		BookedForGstin string `json:"booked_for_gstin"`

		// State, auto-filled client-side from the GSTIN's state code but
		// always a plain editable field — manual entry/override always works.
		ConsigneeState string `json:"consignee_state"`
		BookedForState string `json:"booked_for_state"`

		// Saved recipient the form was pre-filled from, if any — usage
		// telemetry only (bumps use_count/last_used_at for most-used-first
		// ordering). The order itself stores the plain field values above; a
		// stale or foreign id is simply ignored, never an error.
		SavedRecipientID *string `json:"saved_recipient_id"`

		// Shipment-detail expansion (migration 043) — all optional.
		RegisteredAddress        string `json:"registered_address"`
		FactoryAddress           string `json:"factory_address"`
		ContactPersonName        string `json:"contact_person_name"`
		ContactPersonPhone       string `json:"contact_person_phone"`
		ContactPersonEmail       string `json:"contact_person_email" binding:"omitempty,email"`
		ContactPersonDesignation string `json:"contact_person_designation"`
		// Priority defaults to "normal" when omitted — validated below rather
		// than via binding so an empty string doesn't fail the bind.
		Priority             string     `json:"priority"`
		ExpectedDeliveryDate *time.Time `json:"expected_delivery_date"`
		DeclaredValue        *float64   `json:"declared_value"`
		SpecialHandling      []string   `json:"special_handling"`
		InternalReference    string     `json:"internal_reference"`

		// Additional dispatch-email recipients, variable count (migration
		// 043's tracker_order_cc_emails). Distinct from BookedFor/Consignee/
		// TransporterEmail above, which each map to a specific party.
		CCEmails  []string `json:"cc_emails" binding:"omitempty,dive,email"`
		BCCEmails []string `json:"bcc_emails" binding:"omitempty,dive,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	priority := req.Priority
	if priority == "" {
		priority = "normal"
	} else if !validOrderPriorities[priority] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid priority"})
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

	// The receipt-confirmation token is generated up front, same as the
	// public tracking token — the dispatch email always has a working link,
	// and the confirm ACTION itself (not the link) is what's gated on the
	// order actually reaching 'delivered' (see ConfirmTrackerReceipt).
	receiptToken, err := generateTrackingToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate receipt token"})
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
			 consignee_name, material, quantity, dispatch_datetime, documents_enclosed,
			 booked_for_email, consignee_email, transporter_email,
			 consignee_gstin, booked_for_gstin, consignee_state, booked_for_state,
			 received_confirmation_token,
			 registered_address, factory_address,
			 contact_person_name, contact_person_phone, contact_person_email, contact_person_designation,
			 priority, expected_delivery_date, declared_value, special_handling, internal_reference)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,'created',$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42)
	`, id, companyID, req.BookedForCompanyName, req.BookedForPhone,
		req.DispatchFrom, req.DispatchTo, req.DispatchFromLat, req.DispatchFromLng,
		req.DispatchToLat, req.DispatchToLng, nullIfEmpty(req.TransporterName), nullIfEmpty(req.TransporterPhone),
		req.DriverID, driverName, driverPhone, req.VehicleNumber,
		nullIfEmpty(req.EwayBillNumber), token,
		nullIfEmpty(req.ConsigneeName), nullIfEmpty(req.Material), nullIfEmpty(req.Quantity),
		req.DispatchDatetime, nullIfEmpty(req.DocumentsEnclosed),
		nullIfEmpty(req.BookedForEmail), nullIfEmpty(req.ConsigneeEmail), nullIfEmpty(req.TransporterEmail),
		nullIfEmpty(req.ConsigneeGstin), nullIfEmpty(req.BookedForGstin),
		nullIfEmpty(req.ConsigneeState), nullIfEmpty(req.BookedForState), receiptToken,
		nullIfEmpty(req.RegisteredAddress), nullIfEmpty(req.FactoryAddress),
		nullIfEmpty(req.ContactPersonName), nullIfEmpty(req.ContactPersonPhone),
		nullIfEmpty(req.ContactPersonEmail), nullIfEmpty(req.ContactPersonDesignation),
		priority, req.ExpectedDeliveryDate, req.DeclaredValue, req.SpecialHandling, nullIfEmpty(req.InternalReference))
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

	for _, email := range req.CCEmails {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tracker_order_cc_emails (order_id, email, kind) VALUES ($1,$2,'cc')
		`, id, email); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save cc email"})
			return
		}
	}
	for _, email := range req.BCCEmails {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tracker_order_cc_emails (order_id, email, kind) VALUES ($1,$2,'bcc')
		`, id, email); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save bcc email"})
			return
		}
	}

	// A malformed id would fail the UUID cast and abort the transaction, so
	// it's parsed first — same "ignore, don't error" treatment as a stale id.
	if req.SavedRecipientID != nil {
		if _, err := uuid.Parse(*req.SavedRecipientID); err == nil {
			if _, err := tx.Exec(ctx, `
			UPDATE tracker_saved_recipients
			SET use_count = use_count + 1, last_used_at = NOW()
			WHERE id = $1 AND company_id = $2
			`, *req.SavedRecipientID, companyID); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update recipient usage"})
				return
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit"})
		return
	}

	// Route caching is fire-and-forget: the order is already committed and
	// creation must not block on (or fail with) the Ola directions call.
	// The tracking pages just render without a planned route until it lands.
	if req.DispatchFromLat != nil && req.DispatchFromLng != nil && req.DispatchToLat != nil && req.DispatchToLng != nil {
		go cacheTrackerOrderRoute(id.String(),
			*req.DispatchFromLat, *req.DispatchFromLng, *req.DispatchToLat, *req.DispatchToLng)
	} else {
		// One or both endpoints arrived as plain text with no coordinates —
		// the client didn't send lat/lng because the user typed the address
		// instead of picking an autocomplete suggestion. Best-effort forward
		// geocode each missing endpoint in the background; on success the
		// existing route self-heal (GetTrackerCompanyOwnOrder et al.) picks
		// up the new coordinates on the next poll and fills in the route
		// itself — no extra plumbing needed here. On failure the order just
		// stays exactly as it is today: routeless, but otherwise unaffected.
		go geocodeMissingTrackerOrderEndpoints(id.String(),
			req.DispatchFrom, req.DispatchFromLat, req.DispatchFromLng,
			req.DispatchTo, req.DispatchToLat, req.DispatchToLng)
	}

	c.JSON(http.StatusCreated, gin.H{"id": id, "public_tracking_token": token, "message": "order created"})
}

// routeFetchInFlight dedupes concurrent cacheTrackerOrderRoute calls per
// order — the order detail page polls every 15s, and without this a
// persistently-failing route fetch (e.g. unroutable coords) would fire a
// fresh Ola call on every poll tick.
var routeFetchInFlight sync.Map

// cacheTrackerOrderRoute fetches the Ola driving route between the dispatch
// endpoints once and stores it on the order — the single directions call this
// order will ever cost. Runs detached from the create request; on failure the
// route columns just stay NULL and the maps skip the planned-route line.
// Also fired lazily from GetTrackerCompanyOwnOrder as self-healing when a
// create-time fetch failed.
func cacheTrackerOrderRoute(orderID string, fromLat, fromLng, toLat, toLng float64) {
	if _, alreadyRunning := routeFetchInFlight.LoadOrStore(orderID, true); alreadyRunning {
		return
	}
	defer routeFetchInFlight.Delete(orderID)

	from := fmt.Sprintf("%f,%f", fromLat, fromLng)
	to := fmt.Sprintf("%f,%f", toLat, toLng)
	polyline, distanceKm, durationMins, err := fetchOlaDirections(from, to)
	if err != nil || polyline == "" {
		log.Printf("cacheTrackerOrderRoute: directions fetch failed for order=%s: %v", orderID, err)
		return
	}

	pool := db.GetDB().GetPool()
	_, err = pool.Exec(context.Background(), `
		UPDATE tracker_orders
		SET route_polyline=$1, route_distance_km=$2, route_duration_mins=$3, updated_at=NOW()
		WHERE id=$4
	`, polyline, distanceKm, durationMins, orderID)
	if err != nil {
		log.Printf("cacheTrackerOrderRoute: store failed for order=%s: %v", orderID, err)
	}
}

// geocodeMissingTrackerOrderEndpoints best-effort forward-geocodes whichever
// of the two dispatch endpoints arrived without coordinates (typed address,
// not picked from autocomplete) and stores whatever succeeds. Runs detached
// from the create request, same fire-and-forget contract as
// cacheTrackerOrderRoute — on failure the column just stays NULL, exactly
// as if this fallback didn't exist.
func geocodeMissingTrackerOrderEndpoints(orderID string,
	fromText string, fromLat, fromLng *float64,
	toText string, toLat, toLng *float64) {
	pool := db.GetDB().GetPool()
	ctx := context.Background()

	if fromLat == nil || fromLng == nil {
		if lat, lng, err := fetchOlaForwardGeocode(fromText); err == nil {
			if _, err := pool.Exec(ctx, `
				UPDATE tracker_orders SET dispatch_from_lat=$1, dispatch_from_lng=$2, updated_at=NOW() WHERE id=$3
			`, lat, lng, orderID); err != nil {
				log.Printf("geocodeMissingTrackerOrderEndpoints: store failed for order=%s (from): %v", orderID, err)
			}
		} else {
			log.Printf("geocodeMissingTrackerOrderEndpoints: geocode failed for order=%s (from=%q): %v", orderID, fromText, err)
		}
	}

	if toLat == nil || toLng == nil {
		if lat, lng, err := fetchOlaForwardGeocode(toText); err == nil {
			if _, err := pool.Exec(ctx, `
				UPDATE tracker_orders SET dispatch_to_lat=$1, dispatch_to_lng=$2, updated_at=NOW() WHERE id=$3
			`, lat, lng, orderID); err != nil {
				log.Printf("geocodeMissingTrackerOrderEndpoints: store failed for order=%s (to): %v", orderID, err)
			}
		} else {
			log.Printf("geocodeMissingTrackerOrderEndpoints: geocode failed for order=%s (to=%q): %v", orderID, toText, err)
		}
	}
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
		       dispatch_from_lat, dispatch_from_lng, dispatch_to_lat, dispatch_to_lng,
		       route_polyline, route_distance_km, route_duration_mins,
		       signature_url, booked_for_email, consignee_email, transporter_email,
		       received_confirmation_token, received_confirmed_at,
		       consignee_gstin, booked_for_gstin, consignee_state, booked_for_state,
		       registered_address, factory_address,
		       contact_person_name, contact_person_phone, contact_person_email, contact_person_designation,
		       priority, expected_delivery_date, declared_value, special_handling, internal_reference
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
		&o.RoutePolyline, &o.RouteDistanceKm, &o.RouteDurationMins,
		&o.SignatureURL, &o.BookedForEmail, &o.ConsigneeEmail, &o.TransporterEmail,
		&o.ReceivedConfirmationToken, &o.ReceivedConfirmedAt,
		&o.ConsigneeGstin, &o.BookedForGstin, &o.ConsigneeState, &o.BookedForState,
		&o.RegisteredAddress, &o.FactoryAddress,
		&o.ContactPersonName, &o.ContactPersonPhone, &o.ContactPersonEmail, &o.ContactPersonDesignation,
		&o.Priority, &o.ExpectedDeliveryDate, &o.DeclaredValue, &o.SpecialHandling, &o.InternalReference,
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

	cc, bcc, err := fetchTrackerOrderCCEmails(ctx, pool, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	o.CCEmails = cc
	o.BCCEmails = bcc

	docs, err := fetchTrackerOrderDocuments(ctx, pool, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	o.Documents = docs

	// Lazy backfill: if the create-time route fetch failed (or the order
	// predates route caching) but we have both coordinate pairs, retry in the
	// background. This response returns without the route; it shows up on the
	// page's next poll once stored.
	if o.RoutePolyline == nil &&
		o.DispatchFromLat != nil && o.DispatchFromLng != nil &&
		o.DispatchToLat != nil && o.DispatchToLng != nil {
		go cacheTrackerOrderRoute(o.ID,
			*o.DispatchFromLat, *o.DispatchFromLng, *o.DispatchToLat, *o.DispatchToLng)
	}

	rows, err := pool.Query(ctx, `
		SELECT id, order_id, status, COALESCE(note,''), COALESCE(location,''), created_at,
		       reported_by, COALESCE(event_kind,'')
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
		if err := rows.Scan(&e.ID, &e.OrderID, &e.Status, &e.Note, &e.Location, &e.CreatedAt, &e.ReportedBy, &e.EventKind); err != nil {
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

// PATCH /gogoo/tracker/orders/:id/details — edits the dispatch-sheet fields
// (everything CreateTrackerCompanyOrder accepts except driver reassignment
// and coordinates, which have route-caching side effects best left to
// create-time). Status, tokens, and signature are untouched here — this is
// purely the "fix a typo on the dispatch sheet" endpoint, most importantly
// the notification email fields added after an order was already created.
func UpdateTrackerCompanyOrderDetails(c *gin.Context) {
	companyID := c.GetString("company_id")
	orderID := c.Param("id")
	var req struct {
		BookedForCompanyName string `json:"booked_for_company_name" binding:"required"`
		BookedForPhone       string `json:"booked_for_phone" binding:"required"`
		DispatchFrom         string `json:"dispatch_from" binding:"required"`
		DispatchTo           string `json:"dispatch_to" binding:"required"`
		TransporterName      string `json:"transporter_name"`
		TransporterPhone     string `json:"transporter_phone"`
		VehicleNumber        string `json:"vehicle_number" binding:"required"`
		EwayBillNumber       string `json:"eway_bill_number"`

		ConsigneeName     string     `json:"consignee_name"`
		Material          string     `json:"material"`
		Quantity          string     `json:"quantity"`
		DispatchDatetime  *time.Time `json:"dispatch_datetime"`
		DocumentsEnclosed string     `json:"documents_enclosed"`

		BookedForEmail   string `json:"booked_for_email" binding:"omitempty,email"`
		ConsigneeEmail   string `json:"consignee_email" binding:"omitempty,email"`
		TransporterEmail string `json:"transporter_email" binding:"omitempty,email"`

		ConsigneeGstin string `json:"consignee_gstin"`
		BookedForGstin string `json:"booked_for_gstin"`

		ConsigneeState string `json:"consignee_state"`
		BookedForState string `json:"booked_for_state"`

		// Shipment-detail expansion (migration 043) — same field set as
		// CreateTrackerCompanyOrder, minus what create-time locks in
		// (SavedRecipientID has no meaning on an edit).
		RegisteredAddress        string     `json:"registered_address"`
		FactoryAddress           string     `json:"factory_address"`
		ContactPersonName        string     `json:"contact_person_name"`
		ContactPersonPhone       string     `json:"contact_person_phone"`
		ContactPersonEmail       string     `json:"contact_person_email" binding:"omitempty,email"`
		ContactPersonDesignation string     `json:"contact_person_designation"`
		Priority                 string     `json:"priority"`
		ExpectedDeliveryDate     *time.Time `json:"expected_delivery_date"`
		DeclaredValue            *float64   `json:"declared_value"`
		SpecialHandling          []string   `json:"special_handling"`
		InternalReference        string     `json:"internal_reference"`
		CCEmails                 []string   `json:"cc_emails" binding:"omitempty,dive,email"`
		BCCEmails                []string   `json:"bcc_emails" binding:"omitempty,dive,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	priority := req.Priority
	if priority == "" {
		priority = "normal"
	} else if !validOrderPriorities[priority] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid priority"})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	tx, err := pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE tracker_orders SET
			booked_for_company_name=$1, booked_for_phone=$2,
			dispatch_from=$3, dispatch_to=$4,
			transporter_name=$5, transporter_phone=$6,
			vehicle_number=$7, eway_bill_number=$8,
			consignee_name=$9, material=$10, quantity=$11,
			dispatch_datetime=$12, documents_enclosed=$13,
			booked_for_email=$14, consignee_email=$15, transporter_email=$16,
			consignee_gstin=$17, booked_for_gstin=$18,
			consignee_state=$19, booked_for_state=$20,
			registered_address=$21, factory_address=$22,
			contact_person_name=$23, contact_person_phone=$24,
			contact_person_email=$25, contact_person_designation=$26,
			priority=$27, expected_delivery_date=$28, declared_value=$29,
			special_handling=$30, internal_reference=$31,
			updated_at=NOW()
		WHERE id=$32 AND company_id=$33
	`, req.BookedForCompanyName, req.BookedForPhone,
		req.DispatchFrom, req.DispatchTo,
		nullIfEmpty(req.TransporterName), nullIfEmpty(req.TransporterPhone),
		req.VehicleNumber, nullIfEmpty(req.EwayBillNumber),
		nullIfEmpty(req.ConsigneeName), nullIfEmpty(req.Material), nullIfEmpty(req.Quantity),
		req.DispatchDatetime, nullIfEmpty(req.DocumentsEnclosed),
		nullIfEmpty(req.BookedForEmail), nullIfEmpty(req.ConsigneeEmail), nullIfEmpty(req.TransporterEmail),
		nullIfEmpty(req.ConsigneeGstin), nullIfEmpty(req.BookedForGstin),
		nullIfEmpty(req.ConsigneeState), nullIfEmpty(req.BookedForState),
		nullIfEmpty(req.RegisteredAddress), nullIfEmpty(req.FactoryAddress),
		nullIfEmpty(req.ContactPersonName), nullIfEmpty(req.ContactPersonPhone),
		nullIfEmpty(req.ContactPersonEmail), nullIfEmpty(req.ContactPersonDesignation),
		priority, req.ExpectedDeliveryDate, req.DeclaredValue,
		req.SpecialHandling, nullIfEmpty(req.InternalReference),
		orderID, companyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed: " + err.Error()})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	// CC/BCC is a full replace, same "always sends every field" contract as
	// the rest of this endpoint — simplest correct approach for a variable-
	// length list edited from a form that always round-trips the whole set.
	if _, err := tx.Exec(ctx, `DELETE FROM tracker_order_cc_emails WHERE order_id=$1`, orderID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update cc/bcc emails"})
		return
	}
	for _, email := range req.CCEmails {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tracker_order_cc_emails (order_id, email, kind) VALUES ($1,$2,'cc')
		`, orderID, email); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save cc email"})
			return
		}
	}
	for _, email := range req.BCCEmails {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tracker_order_cc_emails (order_id, email, kind) VALUES ($1,$2,'bcc')
		`, orderID, email); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save bcc email"})
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "order details updated"})
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

	// Fire-and-forget — a status-change email failing must never surface as
	// an error on the status update itself. maybeSendTrackerOrderStatusEmail
	// is a no-op for statuses outside its own trigger allowlist (see
	// tracker_status_email.go), so it's safe to call unconditionally here.
	cfg := c.MustGet("config").(*config.Config)
	maybeSendTrackerOrderStatusEmail(cfg, companyID, orderID, req.Status)

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

// validTrackerDocTypes matches the CHECK constraint added in migration 044.
var validTrackerDocTypes = map[string]bool{
	"coa":       true,
	"invoice":   true,
	"lr":        true,
	"eway_bill": true,
	"other":     true,
}

// uploadTrackerOrderFile is the shared upload core for order documents —
// same Cloudinary-with-local-disk-fallback pattern as UploadTrackerOrderEwayBill,
// factored out so it can be reused by the multi-document endpoint below
// without duplicating the mime/size validation and dual storage branches.
func uploadTrackerOrderFile(ctx context.Context, c *gin.Context, companyID, orderID, docType string, file multipart.File, header *multipart.FileHeader) (string, bool) {
	mimeType := header.Header.Get("Content-Type")
	if idx := strings.Index(mimeType, ";"); idx > 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	ext, allowed := allowedMimeTypes[mimeType]
	if !allowed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only JPG, PNG and PDF files allowed"})
		return "", false
	}
	if header.Size > maxFileSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file must be under 10MB"})
		return "", false
	}

	if os.Getenv("CLOUDINARY_CLOUD_NAME") != "" {
		publicID := fmt.Sprintf("gogoo/tracker_orders/%s/%s_%s", orderID, docType, uuid.New().String()[:8])
		secureURL, err := uploadToCloudinaryWithPublicID(ctx, file, header.Filename, publicID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "cloud storage error: " + err.Error()})
			return "", false
		}
		return secureURL, true
	}

	uploadDir := filepath.Join("uploads", "tracker", companyID)
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage error"})
		return "", false
	}
	localName := docType + "_" + orderID + "_" + uuid.New().String()[:8] + ext
	filePath := filepath.Join(uploadDir, localName)
	dst, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save file"})
		return "", false
	}
	_, err = dst.ReadFrom(file)
	dst.Close()
	if err != nil {
		os.Remove(filePath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to write file"})
		return "", false
	}
	return "/uploads/tracker/" + companyID + "/" + localName, true
}

// POST /gogoo/tracker/orders/:id/documents (multipart/form-data: file,
// doc_type, custom_label (only meaningful/stored when doc_type='other'),
// expiry_date (YYYY-MM-DD, optional)). Every doc_type is always optional —
// there is no mandatory-document enforcement (COA/Invoice/LR/E-way
// Bill/Other are all equally optional, regardless of declared value).
func UploadTrackerOrderDocument(c *gin.Context) {
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

	docType := c.Request.FormValue("doc_type")
	if !validTrackerDocTypes[docType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid doc_type"})
		return
	}
	customLabel := c.Request.FormValue("custom_label")

	var expiryDate *time.Time
	if raw := c.Request.FormValue("expiry_date"); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expiry_date must be YYYY-MM-DD"})
			return
		}
		expiryDate = &parsed
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	defer file.Close()

	fileURL, ok := uploadTrackerOrderFile(ctx, c, companyID, orderID, docType, file, header)
	if !ok {
		return
	}

	var doc TrackerOrderDocument
	err = pool.QueryRow(ctx, `
		INSERT INTO tracker_order_documents (order_id, doc_type, custom_label, file_url, expiry_date)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, order_id, doc_type, custom_label, file_url, expiry_date, created_at
	`, orderID, docType, nullIfEmpty(customLabel), fileURL, expiryDate).Scan(
		&doc.ID, &doc.OrderID, &doc.DocType, &doc.CustomLabel, &doc.FileURL, &doc.ExpiryDate, &doc.CreatedAt,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusCreated, doc)
}

// GET /gogoo/tracker/orders/:id/documents
func ListTrackerOrderDocuments(c *gin.Context) {
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

	docs, err := fetchTrackerOrderDocuments(ctx, pool, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, docs)
}

// fetchTrackerOrderCCEmails is shared by GetTrackerCompanyOwnOrder and
// SendTrackerOrderCreationEmail (tracker_creation_email.go) — order
// ownership must already be verified by the caller.
func fetchTrackerOrderCCEmails(ctx context.Context, pool *pgxpool.Pool, orderID string) (cc, bcc []string, err error) {
	rows, err := pool.Query(ctx, `
		SELECT email, kind FROM tracker_order_cc_emails WHERE order_id = $1
	`, orderID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	cc = []string{}
	bcc = []string{}
	for rows.Next() {
		var email, kind string
		if err := rows.Scan(&email, &kind); err != nil {
			continue
		}
		if kind == "bcc" {
			bcc = append(bcc, email)
		} else {
			cc = append(cc, email)
		}
	}
	return cc, bcc, nil
}

// fetchTrackerOrderDocuments is shared by ListTrackerOrderDocuments and
// GetTrackerCompanyOwnOrder (which embeds the document list in the order
// detail response) — order ownership must already be verified by the caller.
func fetchTrackerOrderDocuments(ctx context.Context, pool *pgxpool.Pool, orderID string) ([]TrackerOrderDocument, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, order_id, doc_type, custom_label, file_url, expiry_date, created_at
		FROM tracker_order_documents
		WHERE order_id = $1
		ORDER BY created_at ASC
	`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	docs := []TrackerOrderDocument{}
	for rows.Next() {
		var d TrackerOrderDocument
		if err := rows.Scan(&d.ID, &d.OrderID, &d.DocType, &d.CustomLabel, &d.FileURL, &d.ExpiryDate, &d.CreatedAt); err != nil {
			continue
		}
		docs = append(docs, d)
	}
	return docs, nil
}

// DELETE /gogoo/tracker/orders/:id/documents/:docId
func DeleteTrackerOrderDocument(c *gin.Context) {
	companyID := c.GetString("company_id")
	orderID := c.Param("id")
	docID := c.Param("docId")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM tracker_orders WHERE id=$1 AND company_id=$2)
	`, orderID, companyID).Scan(&exists); err != nil || !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	var fileURL string
	err := pool.QueryRow(ctx, `
		DELETE FROM tracker_order_documents WHERE id=$1 AND order_id=$2 RETURNING file_url
	`, docID, orderID).Scan(&fileURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "document not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	if strings.HasPrefix(fileURL, "https://res.cloudinary.com") {
		go deleteFromCloudinary(fileURL)
	}

	c.JSON(http.StatusOK, gin.H{"message": "document removed"})
}

// allowedLogoMimeTypes restricts logo uploads to images only — unlike KYC
// documents and e-way bills, a PDF doesn't make sense as a company logo.
var allowedLogoMimeTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/jpg":  ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

const maxLogoFileSize = 2 * 1024 * 1024

// POST /gogoo/tracker/logo  (multipart/form-data, field "file")
func UploadTrackerCompanyLogo(c *gin.Context) {
	companyID := c.GetString("company_id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	if err := c.Request.ParseMultipartForm(maxLogoFileSize); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file too large — max 2MB allowed"})
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
	ext, allowed := allowedLogoMimeTypes[mimeType]
	if !allowed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only JPG, PNG and WEBP images allowed"})
		return
	}
	if header.Size > maxLogoFileSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file must be under 2MB"})
		return
	}

	var oldLogoURL *string
	if err := pool.QueryRow(ctx, `SELECT logo_url FROM tracker_companies WHERE id = $1`, companyID).Scan(&oldLogoURL); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "company not found"})
		return
	}

	var logoURL string
	if os.Getenv("CLOUDINARY_CLOUD_NAME") != "" {
		publicID := fmt.Sprintf("gogoo/tracker-companies/%s/logo_%s", companyID, uuid.New().String()[:8])
		secureURL, err := uploadToCloudinaryWithPublicID(ctx, file, header.Filename, publicID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "cloud storage error: " + err.Error()})
			return
		}
		logoURL = secureURL
	} else {
		uploadDir := filepath.Join("uploads", "tracker-companies", companyID)
		if err := os.MkdirAll(uploadDir, 0755); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "storage error"})
			return
		}
		localName := "logo_" + uuid.New().String()[:8] + ext
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
		logoURL = "/uploads/tracker-companies/" + companyID + "/" + localName
	}

	if _, err := pool.Exec(ctx, `
		UPDATE tracker_companies SET logo_url = $1, updated_at = NOW() WHERE id = $2
	`, logoURL, companyID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	if oldLogoURL != nil && *oldLogoURL != "" {
		deleteFromCloudinary(*oldLogoURL)
	}

	c.JSON(http.StatusOK, gin.H{"logo_url": logoURL, "message": "logo uploaded"})
}

// DELETE /gogoo/tracker/logo — clears the company's logo without uploading a
// replacement. Best-effort Cloudinary cleanup, same as the replace path in
// UploadTrackerCompanyLogo.
func DeleteTrackerCompanyLogo(c *gin.Context) {
	companyID := c.GetString("company_id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var oldLogoURL *string
	if err := pool.QueryRow(ctx, `SELECT logo_url FROM tracker_companies WHERE id = $1`, companyID).Scan(&oldLogoURL); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "company not found"})
		return
	}

	if _, err := pool.Exec(ctx, `
		UPDATE tracker_companies SET logo_url = NULL, updated_at = NOW() WHERE id = $1
	`, companyID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	if oldLogoURL != nil && *oldLogoURL != "" {
		deleteFromCloudinary(*oldLogoURL)
	}

	c.JSON(http.StatusOK, gin.H{"message": "logo removed"})
}

// TrackerPartner is the shape returned by GET /gogoo/public/tracker/partners —
// intentionally minimal (no contact/financial fields) since it's unauthenticated.
type TrackerPartner struct {
	CompanyName string `json:"company_name"`
	LogoURL     string `json:"logo_url"`
}

// GET /gogoo/public/tracker/partners — unauthenticated. Returns active
// companies that have uploaded a logo, for the marketing site's "Our
// Partners" section. Companies without a logo are omitted entirely rather
// than returned with a blank/placeholder logo_url.
func GetTrackerPartnersPublic(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT company_name, logo_url FROM tracker_companies
		WHERE status = 'active' AND logo_url IS NOT NULL
		ORDER BY approved_at ASC
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()

	partners := []TrackerPartner{}
	for rows.Next() {
		var p TrackerPartner
		if err := rows.Scan(&p.CompanyName, &p.LogoURL); err != nil {
			continue
		}
		partners = append(partners, p)
	}
	c.JSON(http.StatusOK, partners)
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
	var routePolyline *string
	var routeDistanceKm *float64
	var routeDurationMins *int
	var signatureURL *string
	var receivedConfirmedAt *time.Time
	var companyName string
	err := pool.QueryRow(ctx, `
		SELECT o.id, o.status, o.dispatch_from, o.dispatch_to, o.vehicle_number,
		       o.transporter_name, o.transporter_phone, o.driver_name, o.driver_phone,
		       o.consignee_name, o.material, o.quantity, o.dispatch_datetime,
		       o.last_lat, o.last_lng, o.last_location_at,
		       o.dispatch_from_lat, o.dispatch_from_lng, o.dispatch_to_lat, o.dispatch_to_lng,
		       o.route_polyline, o.route_distance_km, o.route_duration_mins,
		       o.signature_url, o.received_confirmed_at, c.company_name
		FROM tracker_orders o
		JOIN tracker_companies c ON c.id = o.company_id
		WHERE o.public_tracking_token = $1
	`, token).Scan(&orderID, &status, &dispatchFrom, &dispatchTo, &vehicleNumber,
		&transporterName, &transporterPhone, &driverName, &driverPhone,
		&consigneeName, &material, &quantity, &dispatchDatetime,
		&lastLat, &lastLng, &lastLocationAt,
		&dispatchFromLat, &dispatchFromLng, &dispatchToLat, &dispatchToLng,
		&routePolyline, &routeDistanceKm, &routeDurationMins,
		&signatureURL, &receivedConfirmedAt, &companyName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tracking link not found"})
		return
	}

	// Same self-heal as GetTrackerCompanyOwnOrder: retry a failed create-time
	// route fetch in the background so the public page picks it up on its
	// next poll instead of staying routeless forever.
	if routePolyline == nil &&
		dispatchFromLat != nil && dispatchFromLng != nil &&
		dispatchToLat != nil && dispatchToLng != nil {
		go cacheTrackerOrderRoute(orderID, *dispatchFromLat, *dispatchFromLng, *dispatchToLat, *dispatchToLng)
	}

	rows, err := pool.Query(ctx, `
		SELECT e.status, COALESCE(e.note,''), COALESCE(e.location,''), e.created_at,
		       e.reported_by, COALESCE(e.event_kind,'')
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

	// signature_url itself is intentionally never sent to the public page —
	// only whether the delivery is signed. The image stays panel/admin-only.
	type publicEvent struct {
		Status     string    `json:"status"`
		Note       string    `json:"note"`
		Location   string    `json:"location"`
		CreatedAt  time.Time `json:"created_at"`
		ReportedBy string    `json:"reported_by"`
		EventKind  string    `json:"event_kind"`
	}
	var events []publicEvent
	for rows.Next() {
		var e publicEvent
		if err := rows.Scan(&e.Status, &e.Note, &e.Location, &e.CreatedAt, &e.ReportedBy, &e.EventKind); err != nil {
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

	// Public-safe document subset — doc_type/custom_label/file_url only, no
	// expiry_date or internal ids. This is what makes the creation email's
	// "exceeded email size limits, view it here" fallback (see
	// tracker_creation_email.go) an actually-true statement instead of a
	// dead end: the recipient can open this same tracking link and get the
	// file directly from Cloudinary.
	type publicDocument struct {
		DocType     string  `json:"doc_type"`
		CustomLabel *string `json:"custom_label"`
		FileURL     string  `json:"file_url"`
	}
	docRows, err := pool.Query(ctx, `
		SELECT doc_type, custom_label, file_url FROM tracker_order_documents
		WHERE order_id = $1 ORDER BY created_at ASC
	`, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer docRows.Close()
	documents := []publicDocument{}
	for docRows.Next() {
		var d publicDocument
		if err := docRows.Scan(&d.DocType, &d.CustomLabel, &d.FileURL); err != nil {
			continue
		}
		documents = append(documents, d)
	}

	c.JSON(http.StatusOK, gin.H{
		"status":                status,
		"documents":             documents,
		"company_name":          companyName,
		"dispatch_from":         dispatchFrom,
		"dispatch_to":           dispatchTo,
		"vehicle_number":        vehicleNumber,
		"transporter_name":      transporterName,
		"transporter_phone":     transporterPhone,
		"driver_name":           driverName,
		"driver_phone":          driverPhone,
		"consignee_name":        consigneeName,
		"material":              material,
		"quantity":              quantity,
		"dispatch_datetime":     dispatchDatetime,
		"signed":                signatureURL != nil,
		"events":                events,
		"last_lat":              lastLat,
		"last_lng":              lastLng,
		"last_location_at":      lastLocationAt,
		"location_pings":        pings,
		"dispatch_from_lat":     dispatchFromLat,
		"dispatch_from_lng":     dispatchFromLng,
		"dispatch_to_lat":       dispatchToLat,
		"dispatch_to_lng":       dispatchToLng,
		"route_polyline":        routePolyline,
		"route_distance_km":     routeDistanceKm,
		"route_duration_mins":   routeDurationMins,
		"received_confirmed_at": receivedConfirmedAt,
	})
}

// ─── Driver share-link (public, token-gated) ───────────────────────────────

// GET /gogoo/public/tracker/driver/:driver_token — unauthenticated. Returns
// the route summary + status the driver's share page needs to render itself.
// No customer/company financial or contact fields — the driver already
// knows who they are, this is just enough context to confirm the right
// order and show the route while they share their location. company_name
// (only — no email/phone/GSTIN) is included so the page can be branded
// with who the driver is dispatched for.
func GetTrackerDriverOrder(c *gin.Context) {
	driverToken := c.Param("driver_token")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var orderID, status, dispatchFrom, dispatchTo, vehicleNumber, companyName, bookedForCompanyName string
	var fromLat, fromLng, toLat, toLng *float64
	var routePolyline *string
	var routeDistanceKm *float64
	var routeDurationMins *int
	var companyLogoURL *string
	err := pool.QueryRow(ctx, `
		SELECT o.id::text, o.status, o.dispatch_from, o.dispatch_to, o.vehicle_number,
		       o.dispatch_from_lat, o.dispatch_from_lng, o.dispatch_to_lat, o.dispatch_to_lng,
		       o.route_polyline, o.route_distance_km, o.route_duration_mins,
		       c.company_name, o.booked_for_company_name, c.logo_url
		FROM tracker_orders o
		JOIN tracker_companies c ON c.id = o.company_id
		WHERE o.driver_tracking_token = $1
	`, driverToken).Scan(&orderID, &status, &dispatchFrom, &dispatchTo, &vehicleNumber,
		&fromLat, &fromLng, &toLat, &toLng,
		&routePolyline, &routeDistanceKm, &routeDurationMins,
		&companyName, &bookedForCompanyName, &companyLogoURL)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tracking link not found"})
		return
	}

	// Same self-heal as GetTrackerCompanyOwnOrder: retry a failed create-time
	// route fetch in the background so the driver page picks it up on its
	// next poll instead of staying routeless forever.
	if routePolyline == nil && fromLat != nil && fromLng != nil && toLat != nil && toLng != nil {
		go cacheTrackerOrderRoute(orderID, *fromLat, *fromLng, *toLat, *toLng)
	}

	c.JSON(http.StatusOK, gin.H{
		"status":                  status,
		"dispatch_from":           dispatchFrom,
		"dispatch_to":             dispatchTo,
		"vehicle_number":          vehicleNumber,
		"company_name":            companyName,
		"company_logo_url":        companyLogoURL,
		"booked_for_company_name": bookedForCompanyName,
		"is_terminal":             terminalOrderStatuses[status],
		"dispatch_from_lat":       fromLat,
		"dispatch_from_lng":       fromLng,
		"dispatch_to_lat":         toLat,
		"dispatch_to_lng":         toLng,
		"route_polyline":          routePolyline,
		"route_distance_km":       routeDistanceKm,
		"route_duration_mins":     routeDurationMins,
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

// POST /gogoo/public/tracker/driver/:driver_token/event — unauthenticated,
// token-gated. Records a driver-reported quick-status tap (On Break / About
// to Reach / Reached / Unloading / Delivered) as an event at the order's
// CURRENT status — this is never a status transition. 'delivery_claimed' is
// the special kind for the Delivered tap: paired with the signature upload
// below, it's what prompts the company to run the real 'delivered'
// transition themselves via the existing status-update endpoint. The
// company remains the sole authority over tracker_orders.status; this
// handler only ever writes to tracker_order_events.
func PostTrackerDriverEvent(c *gin.Context) {
	driverToken := c.Param("driver_token")
	var req struct {
		Kind string `json:"kind" binding:"required"`
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validDriverEventKinds[req.Kind] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid kind"})
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

	_, err := pool.Exec(ctx, `
		INSERT INTO tracker_order_events (id, order_id, status, note, reported_by, event_kind)
		VALUES ($1,$2,$3,$4,'driver',$5)
	`, uuid.New(), orderID, status, nullIfEmpty(req.Note), req.Kind)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add event"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "event recorded"})
}

// POST /gogoo/public/tracker/driver/:driver_token/signature
// (multipart/form-data, field "file") — unauthenticated, token-gated.
// Uploads the builty signature captured on the drive page's canvas pad and
// stores it as the order's proof-of-delivery image. Reuses the same
// Cloudinary/local-disk pattern as UploadTrackerOrderEwayBill. Never flips
// the order's status — the company still confirms the 'delivered' transition
// in the panel, prompted once a 'delivery_claimed' event and this signature
// both exist on the order.
func UploadTrackerDriverSignature(c *gin.Context) {
	driverToken := c.Param("driver_token")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var orderID, companyID, status string
	if err := pool.QueryRow(ctx, `
		SELECT id, company_id, status FROM tracker_orders WHERE driver_tracking_token = $1
	`, driverToken).Scan(&orderID, &companyID, &status); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tracking link not found"})
		return
	}
	if terminalOrderStatuses[status] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order is in a terminal state (" + status + ") and is no longer tracked"})
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
		secureURL, err := uploadToCloudinary(ctx, file, header.Filename, "signature", orderID)
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
		localName := "signature_" + orderID + "_" + uuid.New().String()[:8] + ext
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
		UPDATE tracker_orders SET signature_url=$1, updated_at=NOW() WHERE id=$2
	`, fileURL, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"signature_url": fileURL, "message": "signature uploaded"})
}

// ─── Company → driver messages ─────────────────────────────────────────────

// POST /gogoo/tracker/orders/:id/messages — company sends a one-way message
// to the driver; the drive page picks it up on its next poll. The driver's
// reverse channel is the quick-status events above — there's no
// driver-to-company reply in v1.
func SendTrackerOrderMessage(c *gin.Context) {
	companyID := c.GetString("company_id")
	orderID := c.Param("id")
	var req struct {
		Body string `json:"body" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM tracker_orders WHERE id=$1 AND company_id=$2)
	`, orderID, companyID).Scan(&exists); err != nil || !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	id := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO tracker_driver_messages (id, order_id, body)
		VALUES ($1,$2,$3)
	`, id, orderID, req.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send message"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "message": "message sent"})
}

// GET /gogoo/public/tracker/driver/:driver_token/messages — unauthenticated,
// token-gated. Returns the full message feed and marks any currently-unread
// messages as read as a side effect of this fetch — the drive page's poll IS
// the read receipt, there's no separate driver tap to dismiss a message.
// is_new reflects whether a message was unread BEFORE this call, so the
// frontend can banner only what just arrived and fold the rest into the feed.
func GetTrackerDriverMessages(c *gin.Context) {
	driverToken := c.Param("driver_token")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var orderID string
	if err := pool.QueryRow(ctx, `
		SELECT id FROM tracker_orders WHERE driver_tracking_token = $1
	`, driverToken).Scan(&orderID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tracking link not found"})
		return
	}

	rows, err := pool.Query(ctx, `
		SELECT id, body, created_at, read_at
		FROM tracker_driver_messages
		WHERE order_id = $1
		ORDER BY created_at ASC
	`, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	type driverMessage struct {
		ID        string    `json:"id"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
		IsNew     bool      `json:"is_new"`
	}
	var messages []driverMessage
	for rows.Next() {
		var m driverMessage
		var readAt *time.Time
		if err := rows.Scan(&m.ID, &m.Body, &m.CreatedAt, &readAt); err != nil {
			continue
		}
		m.IsNew = readAt == nil
		messages = append(messages, m)
	}
	if messages == nil {
		messages = []driverMessage{}
	}

	if _, err := pool.Exec(ctx, `
		UPDATE tracker_driver_messages SET read_at = NOW()
		WHERE order_id = $1 AND read_at IS NULL
	`, orderID); err != nil {
		log.Printf("GetTrackerDriverMessages: mark-read failed for order=%s: %v", orderID, err)
	}

	c.JSON(http.StatusOK, gin.H{"messages": messages})
}
