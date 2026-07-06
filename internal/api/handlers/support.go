package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
)

// GET /gogoo/support/tickets
func GetSupportTickets(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	status := c.Query("status")
	priority := c.Query("priority")
	ticketType := c.Query("type")

	query := `
		SELECT
			t.id, t.ticket_number, t.type, t.status,
			t.priority, t.subject, t.description,
			t.raised_by, t.assigned_to,
			t.refund_requested, t.refund_amount,
			t.refund_status, t.created_at, t.updated_at,
			t.resolved_at, t.resolution,
			COALESCE(u.name, '') as rider_name,
			COALESCE(u.phone, '') as rider_phone,
			COALESCE(du.name, '') as driver_name,
			COALESCE(du.phone, '') as driver_phone,
			t.booking_id
		FROM support_tickets t
		LEFT JOIN riders r ON r.id = t.rider_id
		LEFT JOIN users u ON u.id = r.user_id
		LEFT JOIN drivers d ON d.id = t.driver_id
		LEFT JOIN users du ON du.id = d.user_id
		WHERE 1=1
	`
	args := []interface{}{}
	argIdx := 1

	if status != "" {
		query += ` AND t.status = $` + fmt.Sprintf("%d", argIdx)
		args = append(args, status)
		argIdx++
	}
	if priority != "" {
		query += ` AND t.priority = $` + fmt.Sprintf("%d", argIdx)
		args = append(args, priority)
		argIdx++
	}
	if ticketType != "" {
		query += ` AND t.type = $` + fmt.Sprintf("%d", argIdx)
		args = append(args, ticketType)
		argIdx++
	}

	query += ` ORDER BY
		CASE t.priority
			WHEN 'urgent' THEN 1
			WHEN 'high' THEN 2
			WHEN 'medium' THEN 3
			ELSE 4
		END, t.created_at DESC`

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch tickets"})
		return
	}
	defer rows.Close()

	type Ticket struct {
		ID              string     `json:"id"`
		TicketNumber    string     `json:"ticket_number"`
		Type            string     `json:"type"`
		Status          string     `json:"status"`
		Priority        string     `json:"priority"`
		Subject         string     `json:"subject"`
		Description     string     `json:"description"`
		RaisedBy        string     `json:"raised_by"`
		AssignedTo      *string    `json:"assigned_to"`
		RefundRequested bool       `json:"refund_requested"`
		RefundAmount    *float64   `json:"refund_amount"`
		RefundStatus    *string    `json:"refund_status"`
		CreatedAt       time.Time  `json:"created_at"`
		UpdatedAt       time.Time  `json:"updated_at"`
		ResolvedAt      *time.Time `json:"resolved_at"`
		Resolution      *string    `json:"resolution"`
		RiderName       string     `json:"rider_name"`
		RiderPhone      string     `json:"rider_phone"`
		DriverName      string     `json:"driver_name"`
		DriverPhone     string     `json:"driver_phone"`
		BookingID       *string    `json:"booking_id"`
	}

	var tickets []Ticket
	for rows.Next() {
		var t Ticket
		rows.Scan(
			&t.ID, &t.TicketNumber, &t.Type, &t.Status,
			&t.Priority, &t.Subject, &t.Description,
			&t.RaisedBy, &t.AssignedTo,
			&t.RefundRequested, &t.RefundAmount,
			&t.RefundStatus, &t.CreatedAt, &t.UpdatedAt,
			&t.ResolvedAt, &t.Resolution,
			&t.RiderName, &t.RiderPhone,
			&t.DriverName, &t.DriverPhone,
			&t.BookingID,
		)
		tickets = append(tickets, t)
	}
	if tickets == nil {
		tickets = []Ticket{}
	}
	c.JSON(http.StatusOK, gin.H{"tickets": tickets})
}

// POST /gogoo/support/tickets
func CreateSupportTicket(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var req struct {
		Type        string  `json:"type"`
		Priority    string  `json:"priority"`
		Subject     string  `json:"subject"`
		Description string  `json:"description"`
		RaisedBy    string  `json:"raised_by"`
		RiderID     *string `json:"rider_id"`
		DriverID    *string `json:"driver_id"`
		BookingID   *string `json:"booking_id"`
	}
	c.ShouldBindJSON(&req)

	var ticketID string
	var ticketNumber string
	err := pool.QueryRow(ctx, `
		INSERT INTO support_tickets
			(type, priority, subject, description,
			 raised_by, rider_id, driver_id, booking_id,
			 assigned_to)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id, ticket_number
	`, req.Type, req.Priority, req.Subject,
		req.Description, req.RaisedBy,
		req.RiderID, req.DriverID, req.BookingID,
		c.GetString("user_email"),
	).Scan(&ticketID, &ticketNumber)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create ticket"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id":            ticketID,
		"ticket_number": ticketNumber,
	})
}

// PATCH /gogoo/support/tickets/:id
func UpdateSupportTicket(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	id := c.Param("id")

	var req struct {
		Status     *string `json:"status"`
		Priority   *string `json:"priority"`
		AssignedTo *string `json:"assigned_to"`
		Resolution *string `json:"resolution"`
	}
	c.ShouldBindJSON(&req)

	agentEmail := c.GetString("user_email")

	if req.Status != nil && *req.Status == "resolved" {
		now := time.Now()
		pool.Exec(ctx, `
			UPDATE support_tickets
			SET status=$1, resolution=$2,
				resolved_at=$3, resolved_by=$4,
				updated_at=NOW()
			WHERE id=$5
		`, *req.Status, req.Resolution, now, agentEmail, id)
	} else {
		if req.Status != nil {
			pool.Exec(ctx,
				`UPDATE support_tickets SET status=$1, updated_at=NOW() WHERE id=$2`,
				*req.Status, id)
		}
		if req.Priority != nil {
			pool.Exec(ctx,
				`UPDATE support_tickets SET priority=$1, updated_at=NOW() WHERE id=$2`,
				*req.Priority, id)
		}
		if req.AssignedTo != nil {
			pool.Exec(ctx,
				`UPDATE support_tickets SET assigned_to=$1, status='in_progress', updated_at=NOW() WHERE id=$2`,
				*req.AssignedTo, id)
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}

// POST /gogoo/support/tickets/:id/refund
func ProcessRefund(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	id := c.Param("id")

	var req struct {
		Action string  `json:"action"`
		Amount float64 `json:"amount"`
		Reason string  `json:"reason"`
	}
	c.ShouldBindJSON(&req)

	agentEmail := c.GetString("user_email")
	status := "approved"
	if req.Action == "reject" {
		status = "rejected"
	}

	pool.Exec(ctx, `
		UPDATE support_tickets
		SET refund_status=$1,
			refund_amount=$2,
			refund_processed_at=NOW(),
			refund_processed_by=$3,
			updated_at=NOW()
		WHERE id=$4
	`, status, req.Amount, agentEmail, id)

	c.JSON(http.StatusOK, gin.H{
		"message": "refund " + status,
		"status":  status,
	})
}

// GET /gogoo/support/tickets/:id/messages
func GetTicketMessages(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	id := c.Param("id")

	rows, _ := pool.Query(ctx, `
		SELECT id, ticket_id, sender_type,
			   sender_id, sender_name, message,
			   is_read, created_at
		FROM support_messages
		WHERE ticket_id = $1
		ORDER BY created_at ASC
	`, id)
	defer rows.Close()

	type Message struct {
		ID         string    `json:"id"`
		TicketID   string    `json:"ticket_id"`
		SenderType string    `json:"sender_type"`
		SenderID   string    `json:"sender_id"`
		SenderName string    `json:"sender_name"`
		Message    string    `json:"message"`
		IsRead     bool      `json:"is_read"`
		CreatedAt  time.Time `json:"created_at"`
	}

	var messages []Message
	for rows.Next() {
		var m Message
		rows.Scan(&m.ID, &m.TicketID, &m.SenderType,
			&m.SenderID, &m.SenderName, &m.Message,
			&m.IsRead, &m.CreatedAt)
		messages = append(messages, m)
	}
	if messages == nil {
		messages = []Message{}
	}

	pool.Exec(ctx, `
		UPDATE support_messages
		SET is_read=TRUE
		WHERE ticket_id=$1 AND sender_type != 'support'
	`, id)

	c.JSON(http.StatusOK, gin.H{"messages": messages})
}

// POST /gogoo/support/tickets/:id/messages
func SendTicketMessage(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	id := c.Param("id")

	var req struct {
		Message    string `json:"message"`
		SenderType string `json:"sender_type"`
		SenderName string `json:"sender_name"`
	}
	c.ShouldBindJSON(&req)

	agentEmail := c.GetString("user_email")

	_, err := pool.Exec(ctx, `
		INSERT INTO support_messages
			(ticket_id, sender_type, sender_id, sender_name, message)
		VALUES ($1, $2, $3, $4, $5)
	`, id, req.SenderType, agentEmail, req.SenderName, req.Message)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send"})
		return
	}

	pool.Exec(ctx, `UPDATE support_tickets SET updated_at=NOW() WHERE id=$1`, id)

	c.JSON(http.StatusCreated, gin.H{"message": "sent"})
}

// GET /gogoo/support/stats
func GetSupportStats(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	type Stats struct {
		TotalOpen      int     `json:"total_open"`
		TotalUrgent    int     `json:"total_urgent"`
		TotalResolved  int     `json:"total_resolved"`
		TotalToday     int     `json:"total_today"`
		PendingRefunds int     `json:"pending_refunds"`
		RefundAmount   float64 `json:"pending_refund_amount"`
		AvgResolveMins float64 `json:"avg_resolve_minutes"`
	}

	var s Stats
	pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status='open'),
			COUNT(*) FILTER (WHERE priority='urgent' AND status='open'),
			COUNT(*) FILTER (WHERE status='resolved'),
			COUNT(*) FILTER (WHERE DATE(created_at)=CURRENT_DATE),
			COUNT(*) FILTER (WHERE refund_status='pending'),
			COALESCE(SUM(refund_amount) FILTER (WHERE refund_status='pending'), 0),
			COALESCE(AVG(
				EXTRACT(EPOCH FROM (resolved_at - created_at))/60
			) FILTER (WHERE resolved_at IS NOT NULL), 0)
		FROM support_tickets
	`).Scan(
		&s.TotalOpen, &s.TotalUrgent,
		&s.TotalResolved, &s.TotalToday,
		&s.PendingRefunds, &s.RefundAmount,
		&s.AvgResolveMins,
	)
	c.JSON(http.StatusOK, s)
}

// POST /gogoo/support/cancel-booking/:id
func SupportCancelBooking(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	id := c.Param("id")

	var req struct {
		Reason   string `json:"reason"`
		TicketID string `json:"ticket_id"`
	}
	c.ShouldBindJSON(&req)

	// Support-initiated cancellations are always free for the rider —
	// cancellation_fee stays at its default 0.
	_, err := pool.Exec(ctx, `
		UPDATE bookings
		SET status='cancelled',
			cancel_reason=$1,
			cancelled_by='support',
			cancelled_at=NOW()
		WHERE id=$2
		AND status NOT IN ('completed','cancelled')
	`, req.Reason, id)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cancel failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "booking cancelled"})
}

// POST /gogoo/support/block-rider/:id
// :id is the ticket_id — we look up the rider_id from the ticket
func SupportBlockRider(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	ticketID := c.Param("id")

	var req struct {
		Reason   string `json:"reason"`
		TicketID string `json:"ticket_id"`
	}
	c.ShouldBindJSON(&req)

	// Look up the rider's id from the ticket
	var riderID *string
	pool.QueryRow(ctx, `SELECT rider_id FROM support_tickets WHERE id=$1`, ticketID).Scan(&riderID)
	if riderID == nil || *riderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no rider associated with this ticket"})
		return
	}

	pool.Exec(ctx, `
		UPDATE riders SET is_blocked=TRUE, block_reason=$1 WHERE id=$2
	`, req.Reason, *riderID)

	c.JSON(http.StatusOK, gin.H{"message": "rider blocked"})
}
