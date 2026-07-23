package handlers

// Bogie Tracker — consignee goods-received confirmation. Public, token-gated
// (same unguessable-token security model as the other public tracker
// endpoints — no middleware, each handler does its own token lookup). The
// receipt token is deliberately separate from public_tracking_token: that
// link may be forwarded widely once shared, but receipt confirmation should
// stay with whoever the dispatch email was actually sent to (see
// tracker_notify.go — generated at order creation, or lazily backfilled on
// first notify send for pre-existing orders).

import (
	"context"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// validDeliveryConditions gates the two receipt-page buttons — "good" for
// the perfect-condition confirm, "bad" for the damaged-goods report
// (reason required, enforced in ConfirmTrackerReceipt itself since it's
// only meaningful for 'bad', not worth a DB-level CHECK across columns).
var validDeliveryConditions = map[string]bool{
	"good": true,
	"bad":  true,
}

// GET /gogoo/public/tracker/receipt/:token — minimal order summary for the
// confirmation page. No customer contact/financial fields — this is just
// enough to reassure the recipient they're confirming the right shipment.
func GetTrackerReceiptOrder(c *gin.Context) {
	token := c.Param("token")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var status, dispatchFrom, dispatchTo, vehicleNumber, companyName string
	var material, quantity *string
	var receivedConfirmedAt *time.Time
	var deliveredAt *time.Time
	var driverClaimed bool
	var deliveryCondition, deliveryConditionReason *string
	err := pool.QueryRow(ctx, `
		SELECT o.status, o.dispatch_from, o.dispatch_to, o.vehicle_number,
		       o.material, o.quantity, o.received_confirmed_at,
		       (SELECT e.created_at FROM tracker_order_events e
		        WHERE e.order_id = o.id AND e.status = 'delivered'
		        ORDER BY e.created_at DESC LIMIT 1),
		       EXISTS (
		         SELECT 1 FROM tracker_order_events e
		         WHERE e.order_id = o.id AND e.reported_by = 'driver' AND e.event_kind = 'delivery_claimed'
		       ) AND o.signature_url IS NOT NULL,
		       o.delivery_condition, o.delivery_condition_reason,
		       c.company_name
		FROM tracker_orders o
		JOIN tracker_companies c ON c.id = o.company_id
		WHERE o.received_confirmation_token = $1
	`, token).Scan(&status, &dispatchFrom, &dispatchTo, &vehicleNumber,
		&material, &quantity, &receivedConfirmedAt, &deliveredAt, &driverClaimed,
		&deliveryCondition, &deliveryConditionReason, &companyName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "receipt link not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":                    status,
		"company_name":              companyName,
		"dispatch_from":             dispatchFrom,
		"dispatch_to":               dispatchTo,
		"vehicle_number":            vehicleNumber,
		"material":                  material,
		"quantity":                  quantity,
		"delivered_at":              deliveredAt,
		"received_confirmed_at":     receivedConfirmedAt,
		"driver_claimed":            driverClaimed,
		"delivery_condition":        deliveryCondition,
		"delivery_condition_reason": deliveryConditionReason,
	})
}

// POST /gogoo/public/tracker/receipt/:token/confirm — one-way, idempotent.
// Body: {"condition": "good"|"bad", "reason": "..."} — reason required (and
// only meaningful) for "bad". received_confirmed_at is set for either
// button; condition (good/bad) is a separate flag that never blocks the
// status transition itself — see tryAutoCompleteDelivery.
//
// Gated on the driver having claimed delivery (a 'delivery_claimed' event
// plus an uploaded signature), NOT on status already being 'delivered' —
// under the auto-completion model the consignee's response is one of the
// two signals that PRODUCES the 'delivered' status, so it can legitimately
// arrive first. A second call after confirmation just returns the
// already-confirmed state rather than erroring — the consignee may tap the
// button twice, or reload the confirmed page and tap it again.
func ConfirmTrackerReceipt(c *gin.Context) {
	token := c.Param("token")
	var req struct {
		Condition string `json:"condition" binding:"required"`
		Reason    string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validDeliveryConditions[req.Condition] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid condition"})
		return
	}
	if req.Condition == "bad" && req.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reason is required for bad condition"})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var orderID, status string
	var receivedConfirmedAt *time.Time
	var driverClaimed bool
	err := pool.QueryRow(ctx, `
		SELECT o.id, o.status, o.received_confirmed_at,
		       EXISTS (
		         SELECT 1 FROM tracker_order_events e
		         WHERE e.order_id = o.id AND e.reported_by = 'driver' AND e.event_kind = 'delivery_claimed'
		       ) AND o.signature_url IS NOT NULL
		FROM tracker_orders o WHERE o.received_confirmation_token = $1
	`, token).Scan(&orderID, &status, &receivedConfirmedAt, &driverClaimed)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "receipt link not found"})
		return
	}

	if receivedConfirmedAt != nil {
		c.JSON(http.StatusOK, gin.H{"received_confirmed_at": receivedConfirmedAt})
		return
	}

	if status == "cancelled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order was cancelled and is no longer tracked"})
		return
	}
	if !driverClaimed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "driver has not claimed delivery yet"})
		return
	}

	var reasonVal *string
	if req.Condition == "bad" {
		reasonVal = &req.Reason
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback(ctx)

	var confirmedAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE tracker_orders
		SET received_confirmed_at = NOW(), delivery_condition = $2,
		    delivery_condition_reason = $3, updated_at = NOW()
		WHERE id = $1 AND received_confirmed_at IS NULL
		RETURNING received_confirmed_at
	`, orderID, req.Condition, reasonVal).Scan(&confirmedAt)
	if err != nil {
		// Lost a race with a concurrent confirm — re-read and return that
		// result instead of erroring, keeping this endpoint idempotent.
		var existing time.Time
		if reErr := pool.QueryRow(ctx, `
			SELECT received_confirmed_at FROM tracker_orders WHERE id = $1
		`, orderID).Scan(&existing); reErr == nil {
			c.JSON(http.StatusOK, gin.H{"received_confirmed_at": existing})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "confirm failed"})
		return
	}

	note := "Goods received in proper condition — confirmed by consignee"
	if req.Condition == "bad" {
		note = "Goods received in BAD condition — reported by consignee: " + req.Reason
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO tracker_order_events (id, order_id, status, note, reported_by)
		VALUES ($1,$2,$3,$4,'consignee')
	`, uuid.New(), orderID, status, note)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to log event"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit"})
		return
	}

	cfg := c.MustGet("config").(*config.Config)
	tryAutoCompleteDelivery(ctx, cfg, orderID, "consignee")

	c.JSON(http.StatusOK, gin.H{"received_confirmed_at": confirmedAt})
}
