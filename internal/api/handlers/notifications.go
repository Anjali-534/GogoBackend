package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/dateutil"
	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// nullableTime returns t as a *time.Time when the range filter is active, or
// nil otherwise — used so the query's `$2::timestamptz IS NULL` branch can
// pass through unfiltered when no ?range= was requested.
func nullableTime(active bool, t time.Time) *time.Time {
	if !active {
		return nil
	}
	return &t
}

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
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS target_category TEXT`,
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS target_user_ids UUID[]`,
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS target_hospital_id UUID`,
		`ALTER TABLE notifications ADD COLUMN IF NOT EXISTS target_hospital_ids UUID[]`,
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

// allDriversInCategory reports whether every one of the given driver user_ids
// belongs to the given vehicle category. Used to stop a cab/truck/ambulance
// panel from hand-targeting a driver outside its own lane via the
// "select specific people" multi-select — the UI already filters its search
// by category, but the server can't trust a client-supplied id list.
func allDriversInCategory(ctx context.Context, userIDs []string, category string) (bool, error) {
	pool := db.GetDB().GetPool()
	rows, err := pool.Query(ctx, `SELECT user_id, vehicle_type FROM drivers WHERE user_id = ANY($1::uuid[])`, userIDs)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	matched := 0
	for rows.Next() {
		var userID, vType string
		if err := rows.Scan(&userID, &vType); err != nil {
			return false, err
		}
		if vehicleCategoryFromType(vType) != category {
			return false, nil
		}
		matched++
	}
	return matched == len(userIDs), nil
}

// allHospitalsExist reports whether every one of the given ids is a real
// hospital. Same job as allDriversInCategory but for the ambulance panel's
// hospitals lane — the UI's hospital picker only offers real hospitals, but
// the server can't trust a client-supplied id list.
func allHospitalsExist(ctx context.Context, hospitalIDs []string) (bool, error) {
	pool := db.GetDB().GetPool()
	var matched int
	err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ambulance_hospitals WHERE id = ANY($1::uuid[])`,
		hospitalIDs).Scan(&matched)
	if err != nil {
		return false, err
	}
	return matched == len(hospitalIDs), nil
}

// dispatchExpoPush fires a batch of Expo push notifications for the given
// tokens. Shared by every push code path so the payload/HTTP handling lives
// in one place. extraData is merged into the push "data" payload alongside
// "type" — e.g. a ticket_id so the client can deep-link on tap — and may be
// nil.
func dispatchExpoPush(tokens []string, title, body, notifType string, extraData map[string]string) {
	if len(tokens) == 0 {
		return
	}
	type pushMsg struct {
		To        string            `json:"to"`
		Title     string            `json:"title"`
		Body      string            `json:"body"`
		Data      map[string]string `json:"data"`
		Sound     string            `json:"sound"`
		ChannelID string            `json:"channelId"`
	}
	data := map[string]string{"type": notifType}
	for k, v := range extraData {
		data[k] = v
	}
	msgs := make([]pushMsg, 0, len(tokens))
	for _, token := range tokens {
		if token == "" {
			continue
		}
		msgs = append(msgs, pushMsg{
			To:        token,
			Title:     title,
			Body:      body,
			Data:      data,
			Sound:     "default",
			ChannelID: "general",
		})
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
		log.Printf("dispatchExpoPush HTTP error: %v", err)
		return
	}
	defer resp.Body.Close()
	log.Printf("Push sent to %d device(s), Expo status: %s", len(msgs), resp.Status)
}

// sendPushNotifications fires Expo push notifications in the background.
// When targetUserID is set, the push goes ONLY to that one person's device —
// audience is ignored in that case. Audience-wide queries only run for
// explicit broadcasts (targetUserID == nil). category further narrows a
// "drivers" broadcast to one vehicle_category (cab/truck/ambulance); empty
// means every category.
func sendPushNotifications(audience, title, body, notifType string, targetUserID *string, category string) {
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
				          JOIN drivers d ON d.user_id = pt.user_id::uuid
				          WHERE ($1 = '' OR d.vehicle_category = $1)`
				args = []interface{}{category}
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

		var tokens []string
		for rows.Next() {
			var token string
			if rows.Scan(&token) == nil && token != "" {
				tokens = append(tokens, token)
			}
		}
		dispatchExpoPush(tokens, title, body, notifType, nil)
	}()
}

// sendPushToUserIDs fires pushes to an explicit, hand-picked list of
// recipients (riders and/or drivers) — the multi-select "send to exactly
// these people" path, as opposed to a category/audience broadcast.
func sendPushToUserIDs(userIDs []string, title, body, notifType string) {
	if len(userIDs) == 0 {
		return
	}
	go func() {
		ctx := context.Background()
		pool := db.GetDB().GetPool()

		rows, err := pool.Query(ctx, `
			SELECT token FROM push_tokens WHERE user_id = ANY($1::uuid[])
		`, userIDs)
		if err != nil {
			log.Printf("sendPushToUserIDs query error: %v", err)
			return
		}
		defer rows.Close()

		var tokens []string
		for rows.Next() {
			var token string
			if rows.Scan(&token) == nil && token != "" {
				tokens = append(tokens, token)
			}
		}
		dispatchExpoPush(tokens, title, body, notifType, nil)
	}()
}

// pushToTicketOwner fires a general-channel push to whichever rider or
// driver owns the given support ticket. Shared by agent replies and
// agent-driven status changes — lost-item updates and SOS acknowledgment
// are just tickets under the hood, so this one helper covers all of them.
func pushToTicketOwner(ticketID, title, body, notifType string) {
	go func() {
		ctx := context.Background()
		pool := db.GetDB().GetPool()

		var riderUserID, driverUserID string
		pool.QueryRow(ctx, `
			SELECT COALESCE(r.user_id::text,''), COALESCE(d.user_id::text,'')
			FROM support_tickets t
			LEFT JOIN riders  r ON r.id = t.rider_id
			LEFT JOIN drivers d ON d.id = t.driver_id
			WHERE t.id = $1
		`, ticketID).Scan(&riderUserID, &driverUserID)

		userID := riderUserID
		if userID == "" {
			userID = driverUserID
		}
		if userID == "" {
			return
		}

		var token string
		pool.QueryRow(ctx, `SELECT token FROM push_tokens WHERE user_id = $1::uuid`, userID).Scan(&token)
		if token == "" {
			return
		}
		dispatchExpoPush([]string{token}, title, body, notifType, map[string]string{"ticket_id": ticketID})
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
		Title             string   `json:"title"           binding:"required"`
		Body              string   `json:"body"            binding:"required"`
		Type              string   `json:"type"`
		TargetAudience    string   `json:"target_audience"`
		TargetCategory    string   `json:"target_category"`
		TargetUserID      string   `json:"target_user_id"`
		TargetUserIDs     []string `json:"target_user_ids"`
		TargetHospitalID  string   `json:"target_hospital_id"`
		TargetHospitalIDs []string `json:"target_hospital_ids"`
		CouponCode        string   `json:"coupon_code"`
		LinkURL           string   `json:"link_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Type == ""           { req.Type = "general" }
	if req.TargetAudience == "" { req.TargetAudience = "all" }

	// Lock every non-master panel to its own lane: cab/truck can only blast
	// their own driver category (never riders, never another category, never
	// hospitals); ambulance gets its drivers plus the hospitals lane (hospitals
	// are web-portal-only — in-portal inbox, no push); support can target
	// riders or drivers of any category but not hospitals.
	if c.GetString("role") != "master_admin" {
		switch panel := c.GetString("panel"); panel {
		case "cab", "truck":
			req.TargetAudience = "drivers"
			req.TargetCategory = panel
			req.TargetHospitalID = ""
			req.TargetHospitalIDs = nil
			if len(req.TargetUserIDs) > 0 {
				ok, err := allDriversInCategory(context.Background(), req.TargetUserIDs, panel)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate recipients"})
					return
				}
				if !ok {
					c.JSON(http.StatusForbidden, gin.H{"error": "one or more selected recipients are outside your panel's category"})
					return
				}
			}
		case "ambulance":
			// Two lanes: ambulance drivers or hospitals, chosen by
			// target_audience. "Send to both" is two separate creates from the
			// panel UI, one per lane — never mixed in a single notification.
			if req.TargetAudience == "hospitals" {
				req.TargetCategory = ""
				req.TargetUserID = ""
				req.TargetUserIDs = nil
				hospitalIDs := req.TargetHospitalIDs
				if req.TargetHospitalID != "" {
					hospitalIDs = append(hospitalIDs, req.TargetHospitalID)
				}
				if len(hospitalIDs) > 0 {
					ok, err := allHospitalsExist(context.Background(), hospitalIDs)
					if err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate recipients"})
						return
					}
					if !ok {
						c.JSON(http.StatusForbidden, gin.H{"error": "one or more selected hospitals were not found"})
						return
					}
				}
			} else {
				req.TargetAudience = "drivers"
				req.TargetCategory = "ambulance"
				req.TargetHospitalID = ""
				req.TargetHospitalIDs = nil
				if len(req.TargetUserIDs) > 0 {
					ok, err := allDriversInCategory(context.Background(), req.TargetUserIDs, "ambulance")
					if err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate recipients"})
						return
					}
					if !ok {
						c.JSON(http.StatusForbidden, gin.H{"error": "one or more selected recipients are outside your panel's category"})
						return
					}
				}
			}
		case "support":
			if req.TargetAudience != "riders" && req.TargetAudience != "drivers" {
				req.TargetAudience = "drivers"
			}
			req.TargetHospitalID = ""
			req.TargetHospitalIDs = nil
		default:
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
	}

	var targetUserID *string
	if req.TargetUserID != "" {
		targetUserID = &req.TargetUserID
	}
	var targetHospitalID *string
	if req.TargetHospitalID != "" {
		targetHospitalID = &req.TargetHospitalID
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	id := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO notifications
			(id, title, body, type, target_audience, target_category, target_user_id, target_user_ids,
			 target_hospital_id, target_hospital_ids, coupon_code, link_url)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), $7, $8::uuid[], $9, $10::uuid[], NULLIF($11,''), NULLIF($12,''))
	`, id, req.Title, req.Body, req.Type, req.TargetAudience, req.TargetCategory,
		targetUserID, req.TargetUserIDs, targetHospitalID, req.TargetHospitalIDs,
		req.CouponCode, req.LinkURL)
	if err != nil {
		log.Printf("CreateNotification DB error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Hand-picked recipients override the broadcast entirely — this is an
	// explicit "send to exactly these people" action, not a category/audience blast.
	msg := "broadcast sent"
	switch {
	case len(req.TargetUserIDs) > 0:
		sendPushToUserIDs(req.TargetUserIDs, req.Title, req.Body, req.Type)
		msg = fmt.Sprintf("sent to %d selected recipient(s)", len(req.TargetUserIDs))
	case len(req.TargetHospitalIDs) > 0:
		// Hospitals are a web-portal-only entity with no push token —
		// this lands in their in-portal inbox only (see ListHospitalNotifications).
		msg = fmt.Sprintf("sent to %d selected hospital(s)", len(req.TargetHospitalIDs))
	case targetHospitalID != nil:
		msg = "message sent"
	case targetUserID != nil:
		sendPushNotifications(req.TargetAudience, req.Title, req.Body, req.Type, targetUserID, req.TargetCategory)
		msg = "message sent"
	case req.TargetAudience == "hospitals":
		// broadcast to all hospitals — inbox only, no push mechanism exists for them.
	default:
		sendPushNotifications(req.TargetAudience, req.Title, req.Body, req.Type, nil, req.TargetCategory)
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
		LEFT JOIN drivers d ON d.user_id = $1::uuid
		WHERE n.is_active = true
		  AND (
		        n.target_user_id = $1::uuid
		    OR  $1::uuid = ANY(n.target_user_ids)
		    OR (
		         n.target_user_id IS NULL
		     AND (n.target_user_ids IS NULL OR array_length(n.target_user_ids,1) IS NULL)
		     AND n.target_audience IN ($2, 'all')
		     AND (n.target_category IS NULL OR n.target_category = d.vehicle_category)
		    )
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
		LEFT JOIN drivers d ON d.user_id = $1::uuid
		WHERE n.is_active = true
		  AND (
		        n.target_user_id = $1::uuid
		    OR  $1::uuid = ANY(n.target_user_ids)
		    OR (
		         n.target_user_id IS NULL
		     AND (n.target_user_ids IS NULL OR array_length(n.target_user_ids,1) IS NULL)
		     AND n.target_audience IN ($2, 'all')
		     AND (n.target_category IS NULL OR n.target_category = d.vehicle_category)
		    )
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

// GET /gogoo/ambulance/hospital/notifications — hospital portal inbox.
// Hospitals are web-portal-only (no mobile app, no push token), so this is
// an in-portal list rather than a push feed — same read-tracking table as
// riders/drivers, just keyed by hospital id instead of a user id.
func ListHospitalNotifications(c *gin.Context) {
	hospitalID := c.GetString("user_id")
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
		        n.target_hospital_id = $1::uuid
		    OR  $1::uuid = ANY(n.target_hospital_ids)
		    OR (
		         n.target_hospital_id IS NULL
		     AND (n.target_hospital_ids IS NULL OR array_length(n.target_hospital_ids,1) IS NULL)
		     AND n.target_audience = 'hospitals'
		    )
		  )
		ORDER BY n.created_at DESC
		LIMIT 100
	`, hospitalID)
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

// GET /gogoo/ambulance/hospital/notifications/unread-count — hospital portal
// unread badge; same targeting rules as ListHospitalNotifications. Reads are
// tracked in notification_reads keyed by the hospital id (hospital tokens
// carry it as user_id), so the generic MarkNotificationRead works for marking.
func GetHospitalNotificationUnreadCount(c *gin.Context) {
	hospitalID := c.GetString("user_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var count int
	pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notifications n
		WHERE n.is_active = true
		  AND (
		        n.target_hospital_id = $1::uuid
		    OR  $1::uuid = ANY(n.target_hospital_ids)
		    OR (
		         n.target_hospital_id IS NULL
		     AND (n.target_hospital_ids IS NULL OR array_length(n.target_hospital_ids,1) IS NULL)
		     AND n.target_audience = 'hospitals'
		    )
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM notification_reads nr
			WHERE nr.notification_id = n.id AND nr.user_id = $1
		)
	`, hospitalID).Scan(&count)
	c.JSON(http.StatusOK, gin.H{"count": count})
}

// GET /gogoo/admin/notifications  — admin sees all broadcasts; cab/truck/
// ambulance panels only see broadcasts sent within their own category
// (every broadcast they create is stamped with that category server-side,
// so this filter is precise); support sees everything, same as master.
func AdminListNotifications(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	categoryFilter := ""
	if c.GetString("role") != "master_admin" {
		if panel := c.GetString("panel"); panel == "cab" || panel == "truck" || panel == "ambulance" {
			categoryFilter = panel
		}
	}

	var dr dateutil.Range
	hasRange := c.Query("range") != ""
	if hasRange {
		_, dr = dateutil.Resolve(c.Query("range"), time.Time{}, c.Query("from"), c.Query("to"))
	}

	rows, err := pool.Query(ctx, `
		SELECT n.id, n.title, n.body, n.type, n.target_audience,
		       COALESCE(n.target_category,'')        AS target_category,
		       COALESCE(n.target_user_id::text, '')   AS target_user_id,
		       COALESCE(n.target_user_ids, '{}')      AS target_user_ids,
		       COALESCE(n.target_hospital_id::text,'') AS target_hospital_id,
		       COALESCE(n.target_hospital_ids, '{}')  AS target_hospital_ids,
		       COALESCE(n.coupon_code,'') AS coupon_code,
		       COALESCE(n.link_url,'')    AS link_url,
		       n.is_active, n.created_at,
		       (SELECT COUNT(*) FROM notification_reads nr WHERE nr.notification_id = n.id) AS read_count
		FROM notifications n
		WHERE ($1 = '' OR n.target_category = $1)
		  AND ($2::timestamptz IS NULL OR n.created_at >= $2)
		  AND ($3::timestamptz IS NULL OR n.created_at <= $3)
		ORDER BY n.created_at `+dateutil.ParseSort(c.Query("sort"))+`
		LIMIT 500
	`, categoryFilter, nullableTime(hasRange, dr.Start), nullableTime(hasRange, dr.End))
	if err != nil {
		log.Printf("AdminListNotifications DB error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	result := []map[string]interface{}{}
	for rows.Next() {
		var id, title, body, ntype, audience, category, targetUserID, targetHospitalID, couponCode, linkURL string
		var targetUserIDs, targetHospitalIDs []string
		var isActive bool
		var createdAt time.Time
		var readCount int
		if err := rows.Scan(
			&id, &title, &body, &ntype, &audience, &category, &targetUserID, &targetUserIDs,
			&targetHospitalID, &targetHospitalIDs, &couponCode, &linkURL, &isActive, &createdAt, &readCount,
		); err != nil {
			continue
		}
		item := map[string]interface{}{
			"id":                  id,
			"title":               title,
			"body":                body,
			"type":                ntype,
			"target_audience":     audience,
			"target_category":     category,
			"target_user_id":      targetUserID,
			"target_user_ids":     targetUserIDs,
			"target_hospital_id":  targetHospitalID,
			"target_hospital_ids": targetHospitalIDs,
			"is_active":           isActive,
			"created_at":          createdAt,
			"read_count":          readCount,
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

	// cab/truck/ambulance may only discontinue broadcasts in their own
	// category; support and master can discontinue anything.
	if c.GetString("role") != "master_admin" {
		if panel := c.GetString("panel"); panel == "cab" || panel == "truck" || panel == "ambulance" {
			var category string
			err := pool.QueryRow(ctx, `SELECT COALESCE(target_category,'') FROM notifications WHERE id = $1`, id).Scan(&category)
			if err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "notification not found"})
				return
			}
			if category != panel {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
		}
	}

	_, err := pool.Exec(ctx, `UPDATE notifications SET is_active = false WHERE id = $1`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not discontinue"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
