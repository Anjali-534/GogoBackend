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
	LicenseKey   string     `json:"license_key"`

	// NotificationEmail is the "send as" reply-to address for dispatch
	// emails. Nullable — falls back to ContactEmail when unset.
	NotificationEmail *string `json:"notification_email"`

	// LogoURL is nil when the company hasn't uploaded one — the panel/
	// marketing site both fall back to the generic Bogie logo in that case.
	LogoURL *string `json:"logo_url"`

	// CurrentPlan is nil when the company has never paid (or pre-dates
	// migration 038). SubscriptionExpiresAt is nil for that same case AND
	// for 'lifetime' plans, which never expire — see migration 036.
	CurrentPlan           *string    `json:"current_plan"`
	SubscriptionExpiresAt *time.Time `json:"subscription_expires_at"`
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
	DispatchFromLat      *float64  `json:"dispatch_from_lat"`
	DispatchFromLng      *float64  `json:"dispatch_from_lng"`
	DispatchToLat        *float64  `json:"dispatch_to_lat"`
	DispatchToLng        *float64  `json:"dispatch_to_lng"`
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

	// Dispatch notification email recipients — all optional, nullable. The
	// driver deliberately has no email field (they get the WhatsApp
	// tracking link instead); see notify.go.
	BookedForEmail   *string `json:"booked_for_email"`
	ConsigneeEmail   *string `json:"consignee_email"`
	TransporterEmail *string `json:"transporter_email"`

	// Live driver location tracking — null until the order first moves to
	// 'dispatched' (that's when DriverTrackingToken is generated) and until
	// the driver's share page sends its first location ping.
	DriverTrackingToken *string    `json:"driver_tracking_token"`
	LastLat             *float64   `json:"last_lat"`
	LastLng             *float64   `json:"last_lng"`
	LastLocationAt      *time.Time `json:"last_location_at"`

	// Planned route (Ola Directions), computed once at order creation when
	// both dispatch coordinate pairs exist — see cacheTrackerOrderRoute.
	RoutePolyline     *string  `json:"route_polyline"`
	RouteDistanceKm   *float64 `json:"route_distance_km"`
	RouteDurationMins *int     `json:"route_duration_mins"`

	// Proof-of-delivery signature — set once by the driver-token-gated
	// signature upload after a 'delivery_claimed' event; the company still
	// confirms the actual 'delivered' status transition in the panel.
	SignatureURL *string `json:"signature_url"`

	// Goods-received confirmation — ReceivedConfirmationToken is generated
	// the first time the order reaches 'delivered' (see
	// UpdateTrackerCompanyOrderStatus); ReceivedConfirmedAt is set once by
	// the consignee via the public receipt page and never cleared.
	ReceivedConfirmationToken *string    `json:"received_confirmation_token"`
	ReceivedConfirmedAt       *time.Time `json:"received_confirmed_at"`

	// GSTIN for the two other dispatch-sheet parties — optional, format/
	// checksum validated client-side only, never enforced server-side.
	ConsigneeGstin *string `json:"consignee_gstin"`
	BookedForGstin *string `json:"booked_for_gstin"`

	// State — auto-filled client-side from the GSTIN's state code, but a
	// normal editable field; manual entry/override always works.
	ConsigneeState *string `json:"consignee_state"`
	BookedForState *string `json:"booked_for_state"`

	// Shipment-detail expansion (migration 043) — all optional, nullable.
	// Existing orders predate these columns and come back null except
	// Priority, which is NOT NULL DEFAULT 'normal' at the DB level.
	RegisteredAddress        *string    `json:"registered_address"`
	FactoryAddress           *string    `json:"factory_address"`
	ContactPersonName        *string    `json:"contact_person_name"`
	ContactPersonPhone       *string    `json:"contact_person_phone"`
	ContactPersonEmail       *string    `json:"contact_person_email"`
	ContactPersonDesignation *string    `json:"contact_person_designation"`
	Priority                 string     `json:"priority"`
	ExpectedDeliveryDate     *time.Time `json:"expected_delivery_date"`
	DeclaredValue            *float64   `json:"declared_value"`
	SpecialHandling          []string   `json:"special_handling"`
	InternalReference        *string    `json:"internal_reference"`

	// CC/BCC dispatch-email recipients — populated by a separate query
	// against tracker_order_cc_emails, not part of the main SELECT below.
	CCEmails  []string `json:"cc_emails"`
	BCCEmails []string `json:"bcc_emails"`
}

// TrackerLocationPing is one point on an order's route trail.
type TrackerLocationPing struct {
	Lat       float64   `json:"lat"`
	Lng       float64   `json:"lng"`
	CreatedAt time.Time `json:"created_at"`
}

type TrackerOrderEvent struct {
	ID        string    `json:"id"`
	OrderID   string    `json:"order_id"`
	Status    string    `json:"status"`
	Note      string    `json:"note"`
	Location  string    `json:"location"`
	CreatedAt time.Time `json:"created_at"`

	// ReportedBy is 'company' (default, ordinary status-change events) or
	// 'driver' (a quick-status tap from the drive page — not a status
	// transition, just a note at the order's current status). EventKind
	// carries which quick-status button was pressed; empty for company events.
	ReportedBy string `json:"reported_by"`
	EventKind  string `json:"event_kind"`
}

// ─── Companies ────────────────────────────────────────────────────────────────

// GET /gogoo/dashboard/tracker/companies
func ListTrackerCompanies(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT tc.id, tc.company_name, COALESCE(tc.contact_phone,''), tc.contact_email,
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

// TrackerOverview is the aggregate business-health summary shown on the
// master dashboard's overview page. plan_breakdown always includes every
// sellable plan key (0 if no active companies are on it) so the frontend
// never has to guard against a missing key.
type TrackerOverview struct {
	ActiveCompanies      int            `json:"active_companies"`
	ExpiringSoon7d       int            `json:"expiring_soon_7d"`
	RevenueThisMonth     float64        `json:"revenue_this_month"`
	PendingPaymentOrders int            `json:"pending_payment_orders"`
	PlanBreakdown        map[string]int `json:"plan_breakdown"`
}

// GET /gogoo/dashboard/tracker/overview — aggregate counts for the master
// dashboard's Bogie Tracker summary row. Cheap enough (a handful of
// COUNT/SUM subqueries plus one GROUP BY) to run fresh on every dashboard
// poll rather than caching.
func GetTrackerOverview(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var ov TrackerOverview
	err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM tracker_companies WHERE status = 'active') AS active_companies,
			(SELECT COUNT(*) FROM tracker_companies
			 WHERE status = 'active' AND subscription_expires_at IS NOT NULL
			   AND subscription_expires_at BETWEEN NOW() AND NOW() + INTERVAL '7 days') AS expiring_soon_7d,
			(SELECT COALESCE(SUM(total_amount), 0) FROM tracker_plan_orders
			 WHERE status = 'paid' AND paid_at >= date_trunc('month', NOW())) AS revenue_this_month,
			(SELECT COUNT(*) FROM tracker_plan_orders WHERE status = 'pending_payment') AS pending_payment_orders
	`).Scan(&ov.ActiveCompanies, &ov.ExpiringSoon7d, &ov.RevenueThisMonth, &ov.PendingPaymentOrders)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}

	ov.PlanBreakdown = map[string]int{"single": 0, "2users": 0, "5users": 0, "mega": 0, "lifetime": 0}
	rows, err := pool.Query(ctx, `
		SELECT current_plan, COUNT(*) FROM tracker_companies
		WHERE status = 'active' AND current_plan IS NOT NULL
		GROUP BY current_plan
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var plan string
		var count int
		if err := rows.Scan(&plan, &count); err != nil {
			continue
		}
		ov.PlanBreakdown[plan] = count
	}

	c.JSON(http.StatusOK, ov)
}

// GET /gogoo/dashboard/tracker/companies/:id
func GetTrackerCompany(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var comp TrackerCompany
	var approvedBy *string
	err := pool.QueryRow(ctx, `
		SELECT id, company_name, COALESCE(contact_phone,''), contact_email,
		       COALESCE(gstin,''), status, approved_by::text, approved_at, created_at,
		       COALESCE(license_key,'')
		FROM tracker_companies WHERE id = $1
	`, id).Scan(
		&comp.ID, &comp.CompanyName, &comp.ContactPhone, &comp.ContactEmail,
		&comp.GSTIN, &comp.Status, &approvedBy, &comp.ApprovedAt, &comp.CreatedAt,
		&comp.LicenseKey,
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
		       consignee_name, material, quantity, dispatch_datetime, documents_enclosed,
		       signature_url
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
		&o.SignatureURL,
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}
	o.DriverID = driverID

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

	c.JSON(http.StatusOK, gin.H{
		"order":  o,
		"events": events,
	})
}
