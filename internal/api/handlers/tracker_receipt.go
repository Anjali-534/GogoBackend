package handlers

// Bogie Tracker — consignee goods-received confirmation. Public, token-gated
// (same unguessable-token security model as the other public tracker
// endpoints — no middleware, each handler does its own token lookup). The
// receipt token is deliberately separate from public_tracking_token: that
// link may be forwarded widely once shared, but receipt confirmation should
// stay with whoever the dispatch email was actually sent to (see
// tracker_notify.go, generated the moment an order first reaches
// 'delivered' in UpdateTrackerCompanyOrderStatus).

import (
	"context"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// GET /gogoo/public/tracker/receipt/:token — minimal order summary for the
// confirmation page. No customer contact/financial fields — this is just
// enough to reassure the recipient they're confirming the right shipment.
func GetTrackerReceiptOrder(c *gin.Context) {
	token := c.Param("token")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var status, dispatchFrom, dispatchTo, vehicleNumber string
	var material, quantity *string
	var receivedConfirmedAt *time.Time
	var deliveredAt *time.Time
	err := pool.QueryRow(ctx, `
		SELECT o.status, o.dispatch_from, o.dispatch_to, o.vehicle_number,
		       o.material, o.quantity, o.received_confirmed_at,
		       (SELECT e.created_at FROM tracker_order_events e
		        WHERE e.order_id = o.id AND e.status = 'delivered'
		        ORDER BY e.created_at DESC LIMIT 1)
		FROM tracker_orders o
		WHERE o.received_confirmation_token = $1
	`, token).Scan(&status, &dispatchFrom, &dispatchTo, &vehicleNumber,
		&material, &quantity, &receivedConfirmedAt, &deliveredAt)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "receipt link not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":                status,
		"dispatch_from":         dispatchFrom,
		"dispatch_to":           dispatchTo,
		"vehicle_number":        vehicleNumber,
		"material":              material,
		"quantity":              quantity,
		"delivered_at":          deliveredAt,
		"received_confirmed_at": receivedConfirmedAt,
	})
}

// POST /gogoo/public/tracker/receipt/:token/confirm — one-way, idempotent.
// Only allowed once the order has actually reached 'delivered'. A second
// call after confirmation just returns the already-confirmed state rather
// than erroring — the consignee may tap the button twice, or reload the
// confirmed page and tap it again.
func ConfirmTrackerReceipt(c *gin.Context) {
	token := c.Param("token")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var orderID, status string
	var receivedConfirmedAt *time.Time
	err := pool.QueryRow(ctx, `
		SELECT id, status, received_confirmed_at FROM tracker_orders WHERE received_confirmation_token = $1
	`, token).Scan(&orderID, &status, &receivedConfirmedAt)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "receipt link not found"})
		return
	}

	if receivedConfirmedAt != nil {
		c.JSON(http.StatusOK, gin.H{"received_confirmed_at": receivedConfirmedAt})
		return
	}

	if status != "delivered" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order has not been marked delivered yet"})
		return
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback(ctx)

	var confirmedAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE tracker_orders SET received_confirmed_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND received_confirmed_at IS NULL
		RETURNING received_confirmed_at
	`, orderID).Scan(&confirmedAt)
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

	_, err = tx.Exec(ctx, `
		INSERT INTO tracker_order_events (id, order_id, status, note, reported_by)
		VALUES ($1,$2,$3,$4,'consignee')
	`, uuid.New(), orderID, status, "Goods received in proper condition — confirmed by consignee")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to log event"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"received_confirmed_at": confirmedAt})
}
