package handlers

// Bogie Tracker — dashboard (master admin) endpoints.
//
// Every handler in this file is staff-authenticated (RequirePanel()) and
// deliberately sees ACROSS ALL companies — that's the point, this is the
// admin oversight surface. This is the opposite scoping rule from the
// company-facing /tracker/* endpoints (not yet built), which must always be
// hard-scoped to company_id from the tracker company's own JWT, the same way
// GetHospitalBookings scopes to the hospital's JWT-derived id. Do not copy
// the unscoped queries in this file into a company-facing handler.

import (
	"context"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// ─── Shared types ─────────────────────────────────────────────────────────────

type TrackerCompany struct {
	ID           string     `json:"id"`
	CompanyName  string     `json:"company_name"`
	ContactPhone string     `json:"contact_phone"`
	ContactEmail string     `json:"contact_email"`
	GSTIN        string     `json:"gstin"`
	Status       string     `json:"status"`
	ApprovedBy   *string    `json:"approved_by"`
	ApprovedAt   *time.Time `json:"approved_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

type TrackerCompanyListItem struct {
	TrackerCompany
	DriverCount int `json:"driver_count"`
	OrderCount  int `json:"order_count"`
}

type TrackerDriver struct {
	ID               string    `json:"id"`
	CompanyID        string    `json:"company_id"`
	DriverName       string    `json:"driver_name"`
	Phone            string    `json:"phone"`
	VehicleNumber    string    `json:"vehicle_number"`
	TransporterName  string    `json:"transporter_name"`
	TransporterPhone string    `json:"transporter_phone"`
	IsActive         bool      `json:"is_active"`
	CreatedAt        time.Time `json:"created_at"`
}

type TrackerOrder struct {
	ID                   string    `json:"id"`
	CompanyID            string    `json:"company_id"`
	BookedForCompanyName string    `json:"booked_for_company_name"`
	BookedForPhone       string    `json:"booked_for_phone"`
	DispatchFrom         string    `json:"dispatch_from"`
	DispatchTo           string    `json:"dispatch_to"`
	TransporterName      string    `json:"transporter_name"`
	TransporterPhone     string    `json:"transporter_phone"`
	DriverID             *string   `json:"driver_id"`
	DriverName           string    `json:"driver_name"`
	DriverPhone          string    `json:"driver_phone"`
	VehicleNumber        string    `json:"vehicle_number"`
	EwayBillNumber       string    `json:"eway_bill_number"`
	EwayBillFileURL      string    `json:"eway_bill_file_url"`
	Status               string    `json:"status"`
	PublicTrackingToken  string    `json:"public_tracking_token"`
	CreatedAt            time.Time `json:"created_at"`

	// Dispatch details — from the real dispatch sheet, all optional. Existing
	// orders predate these columns, so they come back null; render "—" on
	// the frontend rather than coalescing to empty string here.
	ConsigneeName     *string    `json:"consignee_name"`
	Material          *string    `json:"material"`
	Quantity          *string    `json:"quantity"`
	DispatchDatetime  *time.Time `json:"dispatch_datetime"`
	DocumentsEnclosed *string    `json:"documents_enclosed"`
}

type TrackerOrderEvent struct {
	ID        string    `json:"id"`
	OrderID   string    `json:"order_id"`
	Status    string    `json:"status"`
	Note      string    `json:"note"`
	Location  string    `json:"location"`
	CreatedAt time.Time `json:"created_at"`
}

// ─── Companies ────────────────────────────────────────────────────────────────

// GET /gogoo/dashboard/tracker/companies
func ListTrackerCompanies(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT tc.id, tc.company_name, tc.contact_phone, tc.contact_email,
		       COALESCE(tc.gstin,''), tc.status, tc.approved_by::text, tc.approved_at,
		       tc.created_at,
		       (SELECT COUNT(*) FROM tracker_drivers td WHERE td.company_id = tc.id) AS driver_count,
		       (SELECT COUNT(*) FROM tracker_orders o WHERE o.company_id = tc.id) AS order_count
		FROM tracker_companies tc
		ORDER BY tc.created_at DESC
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()

	var companies []TrackerCompanyListItem
	for rows.Next() {
		var comp TrackerCompanyListItem
		var approvedBy *string
		if err := rows.Scan(
			&comp.ID, &comp.CompanyName, &comp.ContactPhone, &comp.ContactEmail,
			&comp.GSTIN, &comp.Status, &approvedBy, &comp.ApprovedAt,
			&comp.CreatedAt, &comp.DriverCount, &comp.OrderCount,
		); err != nil {
			continue
		}
		comp.ApprovedBy = approvedBy
		companies = append(companies, comp)
	}
	if companies == nil {
		companies = []TrackerCompanyListItem{}
	}
	c.JSON(http.StatusOK, companies)
}

// GET /gogoo/dashboard/tracker/companies/:id
func GetTrackerCompany(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var comp TrackerCompany
	var approvedBy *string
	err := pool.QueryRow(ctx, `
		SELECT id, company_name, contact_phone, contact_email,
		       COALESCE(gstin,''), status, approved_by::text, approved_at, created_at
		FROM tracker_companies WHERE id = $1
	`, id).Scan(
		&comp.ID, &comp.CompanyName, &comp.ContactPhone, &comp.ContactEmail,
		&comp.GSTIN, &comp.Status, &approvedBy, &comp.ApprovedAt, &comp.CreatedAt,
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tracker company not found"})
		return
	}
	comp.ApprovedBy = approvedBy
	c.JSON(http.StatusOK, comp)
}

// setTrackerCompanyStatus is shared by approve/reject/suspend. No status
// precondition on the WHERE clause — a rejected or suspended company can
// always be reconsidered later (e.g. approve after reject), so we don't gate
// on the current status, only on the row existing.
func setTrackerCompanyStatus(c *gin.Context, status string, stampApproval bool) {
	id := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var companyName, contactEmail string
	var err error
	if stampApproval {
		approvedBy := c.GetString("user_id")
		err = pool.QueryRow(ctx, `
			UPDATE tracker_companies
			SET status = $1, approved_by = $2, approved_at = NOW(), updated_at = NOW()
			WHERE id = $3
			RETURNING company_name, contact_email
		`, status, approvedBy, id).Scan(&companyName, &contactEmail)
	} else {
		err = pool.QueryRow(ctx, `
			UPDATE tracker_companies
			SET status = $1, updated_at = NOW()
			WHERE id = $2
			RETURNING company_name, contact_email
		`, status, id).Scan(&companyName, &contactEmail)
	}
	if err != nil {
		if err == pgx.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "tracker company not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed: " + err.Error()})
		return
	}

	cfg := c.MustGet("config").(*config.Config)
	switch status {
	case "active":
		sendTrackerApprovedEmail(cfg, companyName, contactEmail)
	case "rejected":
		sendTrackerRejectedEmail(cfg, companyName, contactEmail)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Company " + status})
}

// POST /gogoo/dashboard/tracker/companies/:id/approve
func ApproveTrackerCompany(c *gin.Context) {
	setTrackerCompanyStatus(c, "active", true)
}

// POST /gogoo/dashboard/tracker/companies/:id/reject
func RejectTrackerCompany(c *gin.Context) {
	setTrackerCompanyStatus(c, "rejected", false)
}

// POST /gogoo/dashboard/tracker/companies/:id/suspend
func SuspendTrackerCompany(c *gin.Context) {
	setTrackerCompanyStatus(c, "suspended", false)
}

// ─── Drivers (read-only, admin oversight) ──────────────────────────────────────

// GET /gogoo/dashboard/tracker/companies/:id/drivers
func GetTrackerCompanyDrivers(c *gin.Context) {
	companyID := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT id, company_id, driver_name, phone,
		       COALESCE(vehicle_number,''), COALESCE(transporter_name,''),
		       COALESCE(transporter_phone,''), is_active, created_at
		FROM tracker_drivers
		WHERE company_id = $1
		ORDER BY created_at DESC
	`, companyID)
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

// ─── Orders (read-only, admin oversight) ───────────────────────────────────────

// GET /gogoo/dashboard/tracker/companies/:id/orders
func GetTrackerCompanyOrders(c *gin.Context) {
	companyID := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT id, company_id, booked_for_company_name, booked_for_phone,
		       dispatch_from, dispatch_to,
		       COALESCE(transporter_name,''), COALESCE(transporter_phone,''),
		       driver_id::text, COALESCE(driver_name,''), COALESCE(driver_phone,''),
		       vehicle_number, COALESCE(eway_bill_number,''), COALESCE(eway_bill_file_url,''),
		       status, public_tracking_token, created_at,
		       consignee_name, material, quantity, dispatch_datetime, documents_enclosed
		FROM tracker_orders
		WHERE company_id = $1
		ORDER BY created_at DESC
	`, companyID)
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

// GET /gogoo/dashboard/tracker/companies/:id/orders/:orderId
func GetTrackerCompanyOrderDetail(c *gin.Context) {
	companyID := c.Param("id")
	orderID := c.Param("orderId")
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
		       consignee_name, material, quantity, dispatch_datetime, documents_enclosed
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
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
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

	c.JSON(http.StatusOK, gin.H{
		"order":  o,
		"events": events,
	})
}
