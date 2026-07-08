package handlers

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MigrateRideMessages creates the ride_messages table — rider<->driver chat
// scoped to a single booking, separate from the support_messages/tickets
// system. Safe to re-run on every boot alongside the other Migrate* funcs.
func MigrateRideMessages() error {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	steps := []string{
		`CREATE TABLE IF NOT EXISTS ride_messages (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			booking_id UUID NOT NULL REFERENCES bookings(id),
			sender_type TEXT NOT NULL,
			sender_id UUID NOT NULL,
			message TEXT NOT NULL,
			is_read BOOLEAN DEFAULT FALSE,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ride_messages_booking ON ride_messages(booking_id)`,
	}
	for _, sql := range steps {
		if _, err := pool.Exec(ctx, sql); err != nil {
			log.Printf("MigrateRideMessages step failed: %v\nSQL: %s", err, sql)
			return err
		}
	}
	return nil
}

// rideChatActiveStatuses — chatting is only allowed while the ride is
// actually underway, not while searching/completed/cancelled.
var rideChatActiveStatuses = map[string]bool{
	"accepted": true, "arriving": true, "in_progress": true,
}

type rideMessage struct {
	ID         string    `json:"id"`
	SenderType string    `json:"sender_type"`
	Message    string    `json:"message"`
	IsRead     bool      `json:"is_read"`
	CreatedAt  time.Time `json:"created_at"`
}

// bookingCallerRole verifies the caller (from JWT user_id) is either the
// rider or the driver on this specific booking — never trust booking_id
// alone, since it's a client-supplied path param. Returns "rider"/"driver"
// and true if the caller belongs to the booking, along with the booking's
// current status.
func bookingCallerRole(ctx context.Context, pool *pgxpool.Pool, bookingID, userID string) (role string, status string, ok bool) {
	err := pool.QueryRow(ctx, `
		SELECT
			CASE WHEN r.user_id = $2::uuid THEN 'rider'
			     WHEN d.user_id = $2::uuid THEN 'driver'
			     ELSE '' END,
			b.status
		FROM bookings b
		LEFT JOIN riders  r ON r.id = b.rider_id
		LEFT JOIN drivers d ON d.id = b.driver_id
		WHERE b.id = $1
	`, bookingID, userID).Scan(&role, &status)
	if err != nil || role == "" {
		return "", "", false
	}
	return role, status, true
}

func fetchRideMessages(ctx context.Context, bookingID string) []rideMessage {
	pool := db.GetDB().GetPool()
	rows, err := pool.Query(ctx, `
		SELECT id, sender_type, message, is_read, created_at
		FROM ride_messages
		WHERE booking_id = $1
		ORDER BY created_at ASC
	`, bookingID)
	if err != nil {
		return []rideMessage{}
	}
	defer rows.Close()

	var msgs []rideMessage
	for rows.Next() {
		var m rideMessage
		rows.Scan(&m.ID, &m.SenderType, &m.Message, &m.IsRead, &m.CreatedAt)
		msgs = append(msgs, m)
	}
	if msgs == nil {
		return []rideMessage{}
	}
	return msgs
}

// GET /gogoo/bookings/:id/messages
func GetRideMessages(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	bookingID := c.Param("id")
	userID := c.GetString("user_id")

	role, _, ok := bookingCallerRole(ctx, pool, bookingID, userID)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your booking"})
		return
	}

	msgs := fetchRideMessages(ctx, bookingID)

	// Mark the other party's messages as read (caller opened the thread).
	otherType := "driver"
	if role == "driver" {
		otherType = "rider"
	}
	pool.Exec(ctx, `
		UPDATE ride_messages SET is_read = TRUE
		WHERE booking_id = $1 AND sender_type = $2
	`, bookingID, otherType)

	c.JSON(http.StatusOK, gin.H{"messages": msgs})
}

// POST /gogoo/bookings/:id/messages
func SendRideMessage(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	bookingID := c.Param("id")
	userID := c.GetString("user_id")

	var req struct {
		Message string `json:"message"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message required"})
		return
	}

	role, status, ok := bookingCallerRole(ctx, pool, bookingID, userID)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your booking"})
		return
	}
	if !rideChatActiveStatuses[status] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chat is only available during an active ride"})
		return
	}

	_, err := pool.Exec(ctx, `
		INSERT INTO ride_messages (booking_id, sender_type, sender_id, message)
		VALUES ($1, $2, $3, $4)
	`, bookingID, role, userID, req.Message)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"ok": true})
}
