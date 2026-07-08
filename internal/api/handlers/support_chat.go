package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

const faqPromptReply = "Thanks for reaching out! Please choose the topic that best matches your issue below, or tap 'Still need help' to talk to our team directly."

const botSenderName = "bogie Assistant 🤖"

func determinePriority(subject string) string {
	lower := strings.ToLower(subject)
	if strings.Contains(lower, "emergency") || strings.Contains(lower, "accident") || strings.Contains(lower, "urgent") {
		return "urgent"
	}
	if strings.Contains(lower, "payment") || strings.Contains(lower, "refund") || strings.Contains(lower, "charged") {
		return "high"
	}
	return "medium"
}

// escalationPriority maps an FAQ category to the priority a ticket should
// carry once a rider/driver taps "Still need help" — driver behavior
// reports jump the queue, everything else is a normal follow-up.
func escalationPriority(category string) string {
	if category == "driver" {
		return "high"
	}
	return "medium"
}

type chatMessage struct {
	ID         string    `json:"id"`
	SenderType string    `json:"sender_type"`
	SenderName string    `json:"sender_name"`
	Message    string    `json:"message"`
	IsRead     bool      `json:"is_read"`
	CreatedAt  time.Time `json:"created_at"`
}

func fetchChatMessages(ctx context.Context, ticketID string) []chatMessage {
	pool := db.GetDB().GetPool()
	rows, err := pool.Query(ctx, `
		SELECT id, sender_type, sender_name, message, is_read, created_at
		FROM support_messages
		WHERE ticket_id = $1
		ORDER BY created_at ASC
	`, ticketID)
	if err != nil {
		return []chatMessage{}
	}
	defer rows.Close()

	var msgs []chatMessage
	for rows.Next() {
		var m chatMessage
		rows.Scan(&m.ID, &m.SenderType, &m.SenderName, &m.Message, &m.IsRead, &m.CreatedAt)
		msgs = append(msgs, m)
	}
	if msgs == nil {
		return []chatMessage{}
	}
	return msgs
}

// POST /gogoo/support/chat/start
func StartSupportChat(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var req struct {
		RaisedBy     string  `json:"raised_by"`
		Subject      string  `json:"subject"`
		FirstMessage string  `json:"first_message"`
		BookingID    *string `json:"booking_id"`
		FAQID        string  `json:"faq_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Subject == "" || req.FirstMessage == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "subject and first_message required"})
		return
	}
	if req.RaisedBy != "rider" && req.RaisedBy != "driver" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "raised_by must be rider or driver"})
		return
	}

	userID := c.GetString("user_id")
	senderName := c.GetString("user_name")

	// Look up rider_id or driver_id from user_id
	var riderID, driverID *string
	if req.RaisedBy == "rider" {
		var rid string
		if err := pool.QueryRow(ctx, `SELECT id::text FROM riders WHERE user_id = $1`, userID).Scan(&rid); err == nil {
			riderID = &rid
		}
		if senderName == "" {
			pool.QueryRow(ctx, `SELECT u.name FROM users u JOIN riders r ON r.user_id = u.id WHERE u.id = $1`, userID).Scan(&senderName)
		}
	} else {
		var did string
		if err := pool.QueryRow(ctx, `SELECT id::text FROM drivers WHERE user_id = $1`, userID).Scan(&did); err == nil {
			driverID = &did
		}
		if senderName == "" {
			pool.QueryRow(ctx, `SELECT u.name FROM users u JOIN drivers d ON d.user_id = u.id WHERE u.id = $1`, userID).Scan(&senderName)
		}
	}
	if senderName == "" {
		senderName = req.RaisedBy
	}

	priority := determinePriority(req.Subject)

	var ticketID, ticketNumber string
	err := pool.QueryRow(ctx, `
		INSERT INTO support_tickets
			(type, priority, subject, description, raised_by, rider_id, driver_id, booking_id)
		VALUES ('in_app_chat', $1, $2, $3, $4, $5, $6, $7)
		RETURNING id, ticket_number
	`, priority, req.Subject, req.FirstMessage, req.RaisedBy,
		riderID, driverID, req.BookingID,
	).Scan(&ticketID, &ticketNumber)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create ticket"})
		return
	}

	// Save first user message
	pool.Exec(ctx, `
		INSERT INTO support_messages
			(ticket_id, sender_type, sender_id, sender_name, message)
		VALUES ($1, $2, $3, $4, $5)
	`, ticketID, req.RaisedBy, userID, senderName, req.FirstMessage)

	// Instant fixed reply — no AI call, no network round-trip. If the rider
	// tapped a known FAQ question, give its fixed answer; otherwise (they
	// typed something freeform) nudge them toward the FAQ list or escalation.
	botReply := faqPromptReply
	if item, ok := faqByID[req.FAQID]; ok {
		botReply = item.Answer
	}
	pool.Exec(ctx, `
		INSERT INTO support_messages
			(ticket_id, sender_type, sender_id, sender_name, message)
		VALUES ($1, 'bot', 'faq', $2, $3)
	`, ticketID, botSenderName, botReply)

	c.JSON(http.StatusCreated, gin.H{
		"ticket_id":     ticketID,
		"ticket_number": ticketNumber,
		"messages":      fetchChatMessages(ctx, ticketID),
	})
}

// ticketBelongsToCaller verifies the ticket's rider_id/driver_id matches the
// caller's own rider/driver record (looked up from the JWT user_id) — never
// trust ticket_id alone, since it's a client-supplied path param.
func ticketBelongsToCaller(ctx context.Context, pool *pgxpool.Pool, ticketID, userID string) bool {
	var ok bool
	pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM support_tickets t
			LEFT JOIN riders  r ON r.id = t.rider_id
			LEFT JOIN drivers d ON d.id = t.driver_id
			WHERE t.id = $1
			  AND (r.user_id = $2::uuid OR d.user_id = $2::uuid)
		)
	`, ticketID, userID).Scan(&ok)
	return ok
}

// GET /gogoo/support/chat/:ticket_id/messages
func GetChatMessages(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	ticketID := c.Param("ticket_id")
	userID := c.GetString("user_id")

	if !ticketBelongsToCaller(ctx, pool, ticketID, userID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your ticket"})
		return
	}

	var ticket struct {
		ID           string `json:"id"`
		TicketNumber string `json:"ticket_number"`
		Status       string `json:"status"`
		Subject      string `json:"subject"`
	}
	pool.QueryRow(ctx, `
		SELECT id, ticket_number, status, subject
		FROM support_tickets WHERE id = $1
	`, ticketID).Scan(&ticket.ID, &ticket.TicketNumber, &ticket.Status, &ticket.Subject)

	msgs := fetchChatMessages(ctx, ticketID)

	// Mark support + bot messages as read (user opened chat)
	pool.Exec(ctx, `
		UPDATE support_messages
		SET is_read = TRUE
		WHERE ticket_id = $1 AND sender_type IN ('support', 'bot')
	`, ticketID)

	c.JSON(http.StatusOK, gin.H{
		"ticket":   ticket,
		"messages": msgs,
	})
}

// POST /gogoo/support/chat/:ticket_id/messages
func SendChatMessage(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	ticketID := c.Param("ticket_id")

	var req struct {
		Message    string `json:"message"`
		SenderType string `json:"sender_type"`
		SenderName string `json:"sender_name"`
	}
	c.ShouldBindJSON(&req)

	if req.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message required"})
		return
	}

	userID := c.GetString("user_id")

	if !ticketBelongsToCaller(ctx, pool, ticketID, userID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your ticket"})
		return
	}
	if req.SenderName == "" {
		req.SenderName = c.GetString("user_name")
	}

	_, err := pool.Exec(ctx, `
		INSERT INTO support_messages
			(ticket_id, sender_type, sender_id, sender_name, message)
		VALUES ($1, $2, $3, $4, $5)
	`, ticketID, req.SenderType, userID, req.SenderName, req.Message)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send"})
		return
	}

	pool.Exec(ctx, `UPDATE support_tickets SET updated_at = NOW() WHERE id = $1`, ticketID)

	// No auto-reply here anymore — freeform follow-up messages just wait for
	// a human agent (or the rider re-opens the FAQ list / escalates).
	c.JSON(http.StatusCreated, gin.H{"message": "sent"})
}

// GET /gogoo/support/chat/my-tickets
func GetMyTickets(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	userID := c.GetString("user_id")

	var riderID, driverID *string
	var rid, did string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM riders WHERE user_id = $1`, userID).Scan(&rid); err == nil {
		riderID = &rid
	}
	if err := pool.QueryRow(ctx, `SELECT id::text FROM drivers WHERE user_id = $1`, userID).Scan(&did); err == nil {
		driverID = &did
	}

	type Ticket struct {
		ID            string    `json:"id"`
		TicketNumber  string    `json:"ticket_number"`
		Subject       string    `json:"subject"`
		Status        string    `json:"status"`
		LastMessage   string    `json:"last_message"`
		LastMessageAt time.Time `json:"last_message_at"`
		UnreadCount   int       `json:"unread_count"`
	}

	rows, err := pool.Query(ctx, `
		SELECT
			t.id,
			t.ticket_number,
			t.subject,
			t.status,
			COALESCE(
				(SELECT message FROM support_messages
				 WHERE ticket_id = t.id ORDER BY created_at DESC LIMIT 1),
				''
			) AS last_message,
			COALESCE(
				(SELECT created_at FROM support_messages
				 WHERE ticket_id = t.id ORDER BY created_at DESC LIMIT 1),
				t.created_at
			) AS last_message_at,
			(SELECT COUNT(*) FROM support_messages
			 WHERE ticket_id = t.id
			   AND sender_type IN ('support', 'bot')
			   AND is_read = FALSE
			) AS unread_count
		FROM support_tickets t
		WHERE ($1::text IS NOT NULL AND t.rider_id = $1::uuid)
		   OR ($2::text IS NOT NULL AND t.driver_id = $2::uuid)
		ORDER BY last_message_at DESC
		LIMIT 50
	`, riderID, driverID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"tickets": []Ticket{}})
		return
	}
	defer rows.Close()

	var tickets []Ticket
	for rows.Next() {
		var t Ticket
		rows.Scan(&t.ID, &t.TicketNumber, &t.Subject, &t.Status,
			&t.LastMessage, &t.LastMessageAt, &t.UnreadCount)
		tickets = append(tickets, t)
	}
	if tickets == nil {
		tickets = []Ticket{}
	}
	c.JSON(http.StatusOK, gin.H{"tickets": tickets})
}

// GET /gogoo/support/unread-count
func GetUnreadCount(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var count int
	pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM support_messages
		WHERE sender_type IN ('rider', 'driver') AND is_read = FALSE
	`).Scan(&count)

	c.JSON(http.StatusOK, gin.H{"unread": count})
}

// POST /gogoo/support/chat/:ticket_id/escalate — the rider/driver tapped
// "Still need help" after a fixed FAQ answer didn't resolve things.
// Deterministic, not AI-dependent: bumps priority based on category and
// drops a system message so the ticket surfaces prominently in the support
// panel's existing priority-sorted queue.
func EscalateSupportChat(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	ticketID := c.Param("ticket_id")
	userID := c.GetString("user_id")

	if !ticketBelongsToCaller(ctx, pool, ticketID, userID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your ticket"})
		return
	}

	var req struct {
		Category string `json:"category"`
	}
	c.ShouldBindJSON(&req)

	priority := escalationPriority(req.Category)
	pool.Exec(ctx, `UPDATE support_tickets SET priority = $1, updated_at = NOW() WHERE id = $2`, priority, ticketID)
	pool.Exec(ctx, `
		INSERT INTO support_messages
			(ticket_id, sender_type, sender_id, sender_name, message)
		VALUES ($1, 'system', 'system', 'System', 'Escalated to human support team')
	`, ticketID)

	c.JSON(http.StatusOK, gin.H{"ok": true})
}
