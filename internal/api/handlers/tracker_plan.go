package handlers

// Bogie Tracker — plan/subscription billing orders (company-facing).
//
// Same defense-in-depth scoping as tracker.go: every query is WHERE
// company_id = $N off the JWT-derived company id (middleware.RequireTrackerCompany),
// never a client-supplied param. Pricing is always looked up server-side via
// trackerbilling.Lookup, never trusted from the client — see migration 032's
// comment on why (a tampered request can't set its own price, and a later
// price change doesn't alter historical orders).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/services/trackerbilling"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var validTrackerPlans = map[string]bool{
	"single": true, "2users": true, "5users": true, "mega": true, "lifetime": true,
}

var validTrackerBillingDurations = map[string]bool{
	"monthly": true, "quarterly": true, "halfYearly": true, "yearly": true, "onetime": true,
}

// trackerSubscriptionExtension maps a paid order's billing_duration to the
// number of months its subscription_expires_at should be extended by.
// 'onetime' (lifetime plans only, enforced by trackerbilling.Lookup) has no
// entry — those never expire, so MarkTrackerPlanOrderPaid's `ok` check skips
// expiry stacking entirely for them.
var trackerSubscriptionExtension = map[string]int{
	"monthly":    1,
	"quarterly":  3,
	"halfYearly": 6,
	"yearly":     12,
}

// expiresSuffix formats an optional expiry date for the downgrade-blocked
// error message — omitted entirely for lifetime plans, which have none.
func expiresSuffix(expiresAt *time.Time) string {
	if expiresAt == nil {
		return ""
	}
	return " (expires " + expiresAt.Format("2 Jan 2006") + ")"
}

// TrackerPlanOrder mirrors the tracker_plan_orders row shape returned to the
// company panel (list/detail) — payment_gateway_ref and invoice_number stay
// nil until MarkTrackerPlanOrderPaid stamps them in.
type TrackerPlanOrder struct {
	ID              string  `json:"id"`
	CompanyID       string  `json:"company_id"`
	Plan            string  `json:"plan"`
	BillingDuration string  `json:"billing_duration"`
	BaseAmount      float64 `json:"base_amount"`
	GSTAmount       float64 `json:"gst_amount"`
	TotalAmount     float64 `json:"total_amount"`

	BillingName        string  `json:"billing_name"`
	BillingAddressLine string  `json:"billing_address_line"`
	BillingCity        string  `json:"billing_city"`
	BillingState       string  `json:"billing_state"`
	BillingPincode     string  `json:"billing_pincode"`
	GSTIN              *string `json:"gstin"`

	InvoiceNumber     *string    `json:"invoice_number"`
	Status            string     `json:"status"`
	PaymentGatewayRef *string    `json:"payment_gateway_ref"`
	CreatedAt         time.Time  `json:"created_at"`
	PaidAt            *time.Time `json:"paid_at"`
}

// POST /gogoo/tracker/plan-orders
func CreateTrackerPlanOrder(c *gin.Context) {
	companyID := c.GetString("company_id")
	var req struct {
		Plan               string `json:"plan" binding:"required"`
		BillingDuration    string `json:"billing_duration" binding:"required"`
		BillingName        string `json:"billing_name" binding:"required"`
		BillingAddressLine string `json:"billing_address_line" binding:"required"`
		BillingCity        string `json:"billing_city" binding:"required"`
		BillingState       string `json:"billing_state" binding:"required"`
		BillingPincode     string `json:"billing_pincode" binding:"required"`
		GSTIN              string `json:"gstin"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validTrackerPlans[req.Plan] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid plan"})
		return
	}
	if !validTrackerBillingDurations[req.BillingDuration] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid billing_duration"})
		return
	}

	base, gst, total, err := trackerbilling.Lookup(req.Plan, req.BillingDuration)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Block a downgrade while the company's current tier is still active —
	// otherwise MarkTrackerPlanOrderPaid's unconditional current_plan
	// overwrite would drop them to the lower tier's dispatch limit
	// immediately, even though they already paid for and are entitled to the
	// higher tier until it expires. Same-tier renewals and upgrades are
	// unaffected (rank comparison is strictly less-than). A current plan of
	// "lifetime" never expires and outranks everything, so it blocks any new
	// order outright.
	var currentPlan *string
	var subscriptionExpiresAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT current_plan, subscription_expires_at FROM tracker_companies WHERE id = $1
	`, companyID).Scan(&currentPlan, &subscriptionExpiresAt); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	if currentPlan != nil {
		currentActive := *currentPlan == "lifetime" || (subscriptionExpiresAt != nil && subscriptionExpiresAt.After(time.Now()))
		if currentActive {
			currentRank, currentOk := trackerbilling.TierRank(*currentPlan)
			requestedRank, requestedOk := trackerbilling.TierRank(req.Plan)
			if currentOk && requestedOk && requestedRank < currentRank {
				msg := fmt.Sprintf(
					"You have an active %s plan — downgrades can be placed once it expires%s.",
					trackerbilling.PlanLabel(*currentPlan),
					expiresSuffix(subscriptionExpiresAt),
				)
				c.JSON(http.StatusConflict, gin.H{"error": msg, "code": "downgrade_blocked_active_plan"})
				return
			}
		}
	}

	// Reject an exact-duplicate pending order rather than letting the same
	// plan/duration pile up unconfirmed — staff otherwise has to sift
	// through repeat rows for what's really one order (see screenshot: the
	// same plan ordered 6 times). Any plan/duration combo can only have one
	// pending_payment order in flight at a time.
	var dupID string
	err = pool.QueryRow(ctx, `
		SELECT id FROM tracker_plan_orders
		WHERE company_id = $1 AND plan = $2 AND billing_duration = $3 AND status = 'pending_payment'
		LIMIT 1
	`, companyID, req.Plan, req.BillingDuration).Scan(&dupID)
	if err == nil {
		c.JSON(http.StatusConflict, gin.H{
			"error": "You already have a pending order for this plan. Please complete payment or wait for staff confirmation before placing another.",
			"code":  "duplicate_pending_order",
		})
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}

	id := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO tracker_plan_orders
			(id, company_id, plan, billing_duration, base_amount, gst_amount, total_amount,
			 billing_name, billing_address_line, billing_city, billing_state, billing_pincode, gstin)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`, id, companyID, req.Plan, req.BillingDuration, base, gst, total,
		req.BillingName, req.BillingAddressLine, req.BillingCity, req.BillingState, req.BillingPincode,
		nullIfEmpty(req.GSTIN))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order: " + err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":           id,
		"base_amount":  base,
		"gst_amount":   gst,
		"total_amount": total,
		"status":       "pending_payment",
		"message":      "order created — pending payment",
	})
}

// GET /gogoo/tracker/plan-orders
func ListTrackerPlanOrders(c *gin.Context) {
	companyID := c.GetString("company_id")
	status := c.Query("status")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	query := `
		SELECT id, company_id, plan, billing_duration, base_amount, gst_amount, total_amount,
		       billing_name, billing_address_line, billing_city, billing_state, billing_pincode, gstin,
		       invoice_number, status, payment_gateway_ref, created_at, paid_at
		FROM tracker_plan_orders
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

	var orders []TrackerPlanOrder
	for rows.Next() {
		var o TrackerPlanOrder
		if err := rows.Scan(
			&o.ID, &o.CompanyID, &o.Plan, &o.BillingDuration, &o.BaseAmount, &o.GSTAmount, &o.TotalAmount,
			&o.BillingName, &o.BillingAddressLine, &o.BillingCity, &o.BillingState, &o.BillingPincode, &o.GSTIN,
			&o.InvoiceNumber, &o.Status, &o.PaymentGatewayRef, &o.CreatedAt, &o.PaidAt,
		); err != nil {
			continue
		}
		orders = append(orders, o)
	}
	if orders == nil {
		orders = []TrackerPlanOrder{}
	}
	c.JSON(http.StatusOK, orders)
}

// GET /gogoo/dashboard/tracker/companies/:id/plan-orders — staff view of a
// single company's plan orders (unscoped by JWT, :id comes from the URL since
// staff can look up any company).
func GetTrackerCompanyPlanOrders(c *gin.Context) {
	companyID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT id, company_id, plan, billing_duration, base_amount, gst_amount, total_amount,
		       billing_name, billing_address_line, billing_city, billing_state, billing_pincode, gstin,
		       invoice_number, status, payment_gateway_ref, created_at, paid_at
		FROM tracker_plan_orders
		WHERE company_id = $1
		ORDER BY created_at DESC
	`, companyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()

	var orders []TrackerPlanOrder
	for rows.Next() {
		var o TrackerPlanOrder
		if err := rows.Scan(
			&o.ID, &o.CompanyID, &o.Plan, &o.BillingDuration, &o.BaseAmount, &o.GSTAmount, &o.TotalAmount,
			&o.BillingName, &o.BillingAddressLine, &o.BillingCity, &o.BillingState, &o.BillingPincode, &o.GSTIN,
			&o.InvoiceNumber, &o.Status, &o.PaymentGatewayRef, &o.CreatedAt, &o.PaidAt,
		); err != nil {
			continue
		}
		orders = append(orders, o)
	}
	if orders == nil {
		orders = []TrackerPlanOrder{}
	}
	c.JSON(http.StatusOK, orders)
}

// GET /gogoo/tracker/plan-orders/:id/invoice — regenerates the invoice PDF
// on demand from the order's stored columns rather than persisting a blob.
// Only available once paid: invoice_number is nil until MarkTrackerPlanOrderPaid
// stamps it in, so that doubles as the "is it paid" gate.
func GetTrackerPlanOrderInvoice(c *gin.Context) {
	companyID := c.GetString("company_id")
	orderID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var o TrackerPlanOrder
	var paidAt *time.Time
	err := pool.QueryRow(ctx, `
		SELECT id, company_id, plan, billing_duration, base_amount, gst_amount, total_amount,
		       billing_name, billing_address_line, billing_city, billing_state, billing_pincode, gstin,
		       invoice_number, status, paid_at
		FROM tracker_plan_orders
		WHERE id = $1 AND company_id = $2
	`, orderID, companyID).Scan(
		&o.ID, &o.CompanyID, &o.Plan, &o.BillingDuration, &o.BaseAmount, &o.GSTAmount, &o.TotalAmount,
		&o.BillingName, &o.BillingAddressLine, &o.BillingCity, &o.BillingState, &o.BillingPincode, &o.GSTIN,
		&o.InvoiceNumber, &o.Status, &paidAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		}
		return
	}

	if o.Status != "paid" || o.InvoiceNumber == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invoice is only available once the order is paid"})
		return
	}

	issuedAt := time.Now()
	if paidAt != nil {
		issuedAt = *paidAt
	}
	gstin := ""
	if o.GSTIN != nil {
		gstin = *o.GSTIN
	}

	pdfBytes, err := trackerbilling.GenerateInvoicePDF(&trackerbilling.Invoice{
		InvoiceNumber:      *o.InvoiceNumber,
		IssuedAt:           issuedAt,
		OrderID:            o.ID,
		Plan:               o.Plan,
		BillingDuration:    o.BillingDuration,
		BaseAmount:         o.BaseAmount,
		GSTAmount:          o.GSTAmount,
		TotalAmount:        o.TotalAmount,
		BillingName:        o.BillingName,
		BillingAddressLine: o.BillingAddressLine,
		BillingCity:        o.BillingCity,
		BillingState:       o.BillingState,
		BillingPincode:     o.BillingPincode,
		GSTIN:              gstin,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate invoice"})
		return
	}

	filename := fmt.Sprintf("%s.pdf", *o.InvoiceNumber)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}
