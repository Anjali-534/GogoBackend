package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
)

// MigrateSOS extends the support_tickets type CHECK constraint to allow
// 'sos', mirroring the guarded drop/recreate pattern from migration 014 so
// it's safe to re-run on every boot alongside MigrateNotifications/MigrateReferrals.
func MigrateSOS() error {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	steps := []string{
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'support_tickets_type_check'
				AND conrelid = 'support_tickets'::regclass
			) THEN
				ALTER TABLE support_tickets DROP CONSTRAINT support_tickets_type_check;
			END IF;
		EXCEPTION WHEN OTHERS THEN
			NULL;
		END $$`,
		`ALTER TABLE support_tickets
			ADD CONSTRAINT support_tickets_type_check
			CHECK (type IN (
				'payment_issue', 'booking_issue', 'rider_complaint',
				'driver_complaint', 'refund_request', 'other', 'in_app_chat', 'sos'
			))`,
	}
	for _, sql := range steps {
		if _, err := pool.Exec(ctx, sql); err != nil {
			log.Printf("MigrateSOS step failed: %v\nSQL: %s", err, sql)
			return err
		}
	}
	return nil
}

// POST /gogoo/sos — files an urgent support ticket for an SOS trigger.
// Called for every SOS action (police/ambulance/support) so support always
// has a record, even if the rider/driver only tapped "Call Police". Must
// return fast and never block or fail the caller's flow — the tel: call or
// location share has already fired client-side regardless of this result.
func TriggerSOS(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var req struct {
		BookingID   *string `json:"booking_id"`
		Lat         float64 `json:"lat"`
		Lng         float64 `json:"lng"`
		TriggeredBy string  `json:"triggered_by"`
		Action      string  `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false})
		return
	}
	if req.TriggeredBy != "driver" {
		req.TriggeredBy = "rider"
	}
	if req.Action == "" {
		req.Action = "support"
	}
	if req.BookingID != nil && *req.BookingID == "" {
		req.BookingID = nil
	}

	userID := c.GetString("user_id")
	var actorID, name, phone string
	if req.TriggeredBy == "rider" {
		pool.QueryRow(ctx, `SELECT id::text FROM riders WHERE user_id=$1`, userID).Scan(&actorID)
		pool.QueryRow(ctx, `
			SELECT COALESCE(u.name,''), COALESCE(r.phone,'')
			FROM riders r JOIN users u ON u.id = r.user_id
			WHERE r.user_id = $1
		`, userID).Scan(&name, &phone)
	} else {
		pool.QueryRow(ctx, `SELECT id::text FROM drivers WHERE user_id=$1`, userID).Scan(&actorID)
		pool.QueryRow(ctx, `
			SELECT COALESCE(u.name,''), COALESCE(d.phone,'')
			FROM drivers d JOIN users u ON u.id = d.user_id
			WHERE d.user_id = $1
		`, userID).Scan(&name, &phone)
	}
	if name == "" {
		name = req.TriggeredBy
	}

	description := fmt.Sprintf("Action taken: %s\nPhone: %s", req.Action, phone)
	if req.Lat != 0 && req.Lng != 0 {
		description += fmt.Sprintf("\nLive location: https://maps.google.com/?q=%f,%f", req.Lat, req.Lng)
	}
	if req.BookingID != nil {
		description += fmt.Sprintf("\nBooking ID: %s", *req.BookingID)
	}

	var riderID, driverID *string
	if actorID != "" {
		if req.TriggeredBy == "rider" {
			riderID = &actorID
		} else {
			driverID = &actorID
		}
	}

	var ticketID, ticketNumber string
	err := pool.QueryRow(ctx, `
		INSERT INTO support_tickets
			(type, priority, subject, description, raised_by, rider_id, driver_id, booking_id)
		VALUES ('sos', 'urgent', $1, $2, $3, $4, $5, $6)
		RETURNING id, ticket_number
	`, fmt.Sprintf("🚨 SOS EMERGENCY - %s", name), description, req.TriggeredBy,
		riderID, driverID, req.BookingID,
	).Scan(&ticketID, &ticketNumber)

	if err != nil {
		log.Printf("TriggerSOS insert error: %v", err)
		// Never fail the SOS flow for the caller — the real emergency action
		// (call/share) already happened client-side before this request.
		c.JSON(http.StatusOK, gin.H{"ok": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "ticket_id": ticketID, "ticket_number": ticketNumber})
}
