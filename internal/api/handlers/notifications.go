package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// MigrateNotifications creates or upgrades the notifications tables.
func MigrateNotifications() error {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	steps := []string{
		`CREATE TABLE IF NOT EXISTS notifications (
			id               UUID        PRIMARY KEY,
			title            TEXT        NOT NULL DEFAULT '',
			body             TEXT        NOT NULL DEFAULT '',
			type             TEXT        NOT NULL DEFAULT 'general',
			target_audience  TEXT        NOT NULL DEFAULT 'all',
			coupon_code      TEXT,
			link_url         TEXT,
			is_active        BOOLEAN     NOT NULL DEFAULT true,
			created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS notification_reads (
			notification_id UUID REFERENCES notifications(id) ON DELETE CASCADE,
			user_id         UUID NOT NULL,
			read_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (notification_id, user_id)
		)`,
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS body TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS type TEXT NOT NULL DEFAULT 'general'`,
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS target_audience TEXT NOT NULL DEFAULT 'all'`,
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS coupon_code TEXT`,
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS link_url TEXT`,
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT true`,
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`,
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS target_user_id UUID`,
		`DO $$ BEGIN ALTER TABLE notifications ALTER COLUMN user_id DROP NOT NULL; EXCEPTION WHEN undefined_column THEN NULL; END $$`,
		`CREATE TABLE IF NOT EXISTS push_tokens (
			user_id    UUID PRIMARY KEY,
			token      TEXT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
	}

	for _, sql := range steps {
		if _, err := pool.Exec(ctx, sql); err != nil {
			log.Printf("MigrateNotifications step failed: %v\nSQL: %s", err, sql)
			return err
		}
	}

	// Log actual columns so we can verify the schema
	rows, err := pool.Query(ctx, `
		SELECT column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_name = 'notifications'
		ORDER BY ordinal_position
	`)
	if err == nil {
		defer rows.Close()
		log.Println("notifications table columns:")
		for rows.Next() {
			var col, dtype, nullable string
			rows.Scan(&col, &dtype, &nullable)
			log.Printf("  %-20s %s (nullable: %s)", col, dtype, nullable)
		}
	}
	return nil
}

// POST /gogoo/push-token  — register or refresh a device push token
func RegisterPushToken(c *gin.Context) {
	userID := c.GetString("user_id")
	var req struct {
		Token string `json:"token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	_, err := pool.Exec(ctx, `
		INSERT INTO push_tokens (user_id, token, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (user_id) DO UPDATE SET token = $2, updated_at = NOW()
	`, userID, req.Token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// sendPushNotifications fires Expo push notifications in the background.
// When targetUserID is set, the push goes ONLY to that one person's device —
// audience is ignored in that case. Audience-wide queries only run for
// explicit broadcasts (targetUserID == nil).
func sendPushNotifications(audience, title, body, notifType string, targetUserID *string) {
	go func() {
		ctx := context.Background()
		pool := db.GetDB().GetPool()

		var query string
		var args []interface{}
		if targetUserID != nil {
			query = `SELECT token FROM push_tokens WHERE user_id = $1::uuid`
			args = []interface{}{*targetUserID}
		} else {
			switch audience {
			case "drivers":
				query = `SELECT pt.token FROM push_tokens pt
				          JOIN drivers d ON d.user_id = pt.user_id::uuid`
			case "riders":
				query = `SELECT pt.token FROM push_tokens pt
				          JOIN riders r ON r.user_id = pt.user_id::uuid`
			default:
				query = `SELECT token FROM push_tokens`
			}
		}

		rows, err := pool.Query(ctx, query, args...)
		if err != nil {
			log.Printf("sendPushNotifications query error: %v", err)
			return
		}
		defer rows.Close()

		type pushMsg struct {
			To    string            `json:"to"`
			Title string            `json:"title"`
			Body  string            `json:"body"`
			Data  map[string]string `json:"data"`
			Sound string            `json:"sound"`
		}
		var msgs []pushMsg
		for rows.Next() {
			var token string
			if rows.Scan(&token) == nil && token != "" {
				msgs = append(msgs, pushMsg{
					To:    token,
					Title: title,
					Body:  body,
					Data:  map[string]string{"type": notifType},
					Sound: "default",
				})
			}
		}
		if len(msgs) == 0 {
			return
		}

		payload, _ := json.Marshal(msgs)
		resp, err := http.Post(
			"https://exp.host/api/v2/push/send",
			"application/json",
			bytes.NewBuffer(payload),
		)
		if err != nil {
			log.Printf("sendPushNotifications HTTP error: %v", err)
			return
		}
		defer resp.Body.Close()
		log.Printf("Push sent to %d device(s), Expo status: %s", len(msgs), resp.Status)
	}()
}

// notifyDriversOfNewRide fires a high-priority push (with the ride_request
// ringtone) to every online driver whose vehicle_category matches the
// booking's service category, so their notification actually rings even
// when the app is backgrounded or closed.
func notifyDriversOfNewRide(bookingID, category, pickupAddress string, fare float64) {
	go func() {
		ctx := context.Background()
		pool := db.GetDB().GetPool()

		rows, err := pool.Query(ctx, `
			SELECT pt.token FROM push_tokens pt
			JOIN drivers d ON d.user_id = pt.user_id::uuid
			WHERE d.is_online = true AND pt.token <> ''
			  AND ($1 = '' OR d.vehicle_category = $1)
		`, category)
		if err != nil {
			log.Printf("notifyDriversOfNewRide query error: %v", err)
			return
		}
		defer rows.Close()

		type pushMsg struct {
			To        string            `json:"to"`
			Title     string            `json:"title"`
			Body      string            `json:"body"`
			Data      map[string]string `json:"data"`
			Sound     string            `json:"sound"`
			Priority  string            `json:"priority"`
			ChannelID string            `json:"channelId"`
		}
		var msgs []pushMsg
		for rows.Next() {
			var token string
			if rows.Scan(&token) == nil && token != "" {
				msgs = append(msgs, pushMsg{
					To:        token,
					Title:     "New Ride Request!",
					Body:      fmt.Sprintf("Pickup: %s • ₹%.0f", pickupAddress, fare),
					Data:      map[string]string{"type": "ride_request", "booking_id": bookingID},
					Sound:     "ride_request.wav",
					Priority:  "high",
					ChannelID: "ride-requests",
				})
			}
		}
		if len(msgs) == 0 {
			return
		}

		payload, _ := json.Marshal(msgs)
		resp, err := http.Post(
			"https://exp.host/api/v2/push/send",
			"application/json",
			bytes.NewBuffer(payload),
		)
		if err != nil {
			log.Printf("notifyDriversOfNewRide HTTP error: %v", err)
			return
		}
		defer resp.Body.Close()
		log.Printf("Ride-request push sent to %d driver(s), Expo status: %s", len(msgs), resp.Status)
	}()
}

// POST /gogoo/admin/notifications
func CreateNotification(c *gin.Context) {
	var req struct {
		Title          string `json:"title"           binding:"required"`
		Body           string `json:"body"            binding:"required"`
		Type           string `json:"type"`
		TargetAudience string `json:"target_audience"`
		TargetUserID   string `json:"target_user_id"`
		CouponCode     string `json:"coupon_code"`
		LinkURL        string `json:"link_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Type == ""           { req.Type = "general" }
	if req.TargetAudience == "" { req.TargetAudience = "all" }

	var targetUserID *string
	if req.TargetUserID != "" {
		targetUserID = &req.TargetUserID
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	id := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO notifications (id, title, body, type, target_audience, target_user_id, coupon_code, link_url)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7,''), NULLIF($8,''))
	`, id, req.Title, req.Body, req.Type, req.TargetAudience, targetUserID, req.CouponCode, req.LinkURL)
	if err != nil {
		log.Printf("CreateNotification DB error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	sendPushNotifications(req.TargetAudience, req.Title, req.Body, req.Type, targetUserID)

	msg := "broadcast sent"
	if targetUserID != nil {
		msg = "message sent"
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "message": msg})
}

// scanNotifications is shared between riders and drivers list endpoints.
func scanNotifications(c *gin.Context, audience string) {
	userID := c.GetString("user_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT n.id, n.title, n.body, n.type,
		       COALESCE(n.coupon_code, '') AS coupon_code,
		       COALESCE(n.link_url, '')    AS link_url,
		       n.created_at,
		       (nr.user_id IS NOT NULL) AS is_read
		FROM notifications n
		LEFT JOIN notification_reads nr
		       ON nr.notification_id = n.id AND nr.user_id = $1
		WHERE n.is_active = true
		  AND (
		        n.target_user_id = $1::uuid
		    OR (n.target_user_id IS NULL AND n.target_audience IN ($2, 'all'))
		  )
		ORDER BY n.created_at DESC
		LIMIT 100
	`, userID, audience)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query failed"})
		return
	}
	defer rows.Close()

	result := []map[string]interface{}{}
	for rows.Next() {
		var id, title, body, ntype, couponCode, linkURL string
		var createdAt time.Time
		var isRead bool
		if err := rows.Scan(&id, &title, &body, &ntype, &couponCode, &linkURL, &createdAt, &isRead); err != nil {
			continue
		}
		item := map[string]interface{}{
			"id":         id,
			"title":      title,
			"body":       body,
			"type":       ntype,
			"created_at": createdAt,
			"is_read":    isRead,
		}
		if couponCode != "" { item["coupon_code"] = couponCode }
		if linkURL    != "" { item["link_url"]    = linkURL    }
		result = append(result, item)
	}
	c.JSON(http.StatusOK, result)
}

// GET /gogoo/notifications  — rider inbox
func ListNotifications(c *gin.Context) { scanNotifications(c, "riders") }

// GET /gogoo/driver/notifications  — driver inbox
func ListDriverNotifications(c *gin.Context) { scanNotifications(c, "drivers") }

// unreadCount shared helper
func unreadCount(c *gin.Context, audience string) {
	userID := c.GetString("user_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var count int
	pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notifications n
		WHERE n.is_active = true
		  AND (
		        n.target_user_id = $1::uuid
		    OR (n.target_user_id IS NULL AND n.target_audience IN ($2, 'all'))
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM notification_reads nr
			WHERE nr.notification_id = n.id AND nr.user_id = $1
		)
	`, userID, audience).Scan(&count)
	c.JSON(http.StatusOK, gin.H{"count": count})
}

// GET /gogoo/notifications/unread-count
func GetNotificationUnreadCount(c *gin.Context) { unreadCount(c, "riders") }

// GET /gogoo/driver/notifications/unread-count
func GetDriverNotificationUnreadCount(c *gin.Context) { unreadCount(c, "drivers") }

// POST /gogoo/notifications/:id/read  — mark read (works for both riders & drivers)
func MarkNotificationRead(c *gin.Context) {
	userID  := c.GetString("user_id")
	notifID := c.Param("id")
	ctx  := context.Background()
	pool := db.GetDB().GetPool()

	_, err := pool.Exec(ctx,
		`INSERT INTO notification_reads (notification_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		notifID, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not mark read"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GET /gogoo/admin/notifications  — admin sees all broadcasts
func AdminListNotifications(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT n.id, n.title, n.body, n.type, n.target_audience,
		       COALESCE(n.target_user_id::text, '') AS target_user_id,
		       COALESCE(n.coupon_code,'') AS coupon_code,
		       COALESCE(n.link_url,'')    AS link_url,
		       n.is_active, n.created_at,
		       (SELECT COUNT(*) FROM notification_reads nr WHERE nr.notification_id = n.id) AS read_count
		FROM notifications n
		ORDER BY n.created_at DESC
	`)
	if err != nil {
		log.Printf("AdminListNotifications DB error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	result := []map[string]interface{}{}
	for rows.Next() {
		var id, title, body, ntype, audience, targetUserID, couponCode, linkURL string
		var isActive bool
		var createdAt time.Time
		var readCount int
		if err := rows.Scan(&id, &title, &body, &ntype, &audience, &targetUserID, &couponCode, &linkURL, &isActive, &createdAt, &readCount); err != nil {
			continue
		}
		item := map[string]interface{}{
			"id":              id,
			"title":           title,
			"body":            body,
			"type":            ntype,
			"target_audience": audience,
			"target_user_id":  targetUserID,
			"is_active":       isActive,
			"created_at":      createdAt,
			"read_count":      readCount,
		}
		if couponCode != "" { item["coupon_code"] = couponCode }
		if linkURL    != "" { item["link_url"]    = linkURL    }
		result = append(result, item)
	}
	c.JSON(http.StatusOK, result)
}

// DELETE /gogoo/admin/notifications/:id  — discontinue a broadcast
func DeleteNotification(c *gin.Context) {
	id   := c.Param("id")
	ctx  := context.Background()
	pool := db.GetDB().GetPool()

	_, err := pool.Exec(ctx, `UPDATE notifications SET is_active = false WHERE id = $1`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not discontinue"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
