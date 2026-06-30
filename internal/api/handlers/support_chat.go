package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
)

const aiSystemPrompt = `You are a helpful customer support assistant for gogoo, a ride-hailing and logistics platform in Delhi NCR, India.
Services: cab (2W/3W/4W/SUV), truck (city/outstation), ambulance (free NGO + paid hospital).

Answer common questions helpfully and briefly in 2-3 sentences max.

If you cannot resolve the issue say exactly:
"ESCALATE: I am connecting you with a support agent who will assist you shortly."

Common issues you CAN resolve:
- How to track a ride
- How to cancel a booking
- Fare calculation questions
- How ambulance service works
- Wallet balance questions
- App usage help
- Booking flow questions

Issues to ESCALATE (say ESCALATE):
- Refund requests
- Driver complaints
- Payment disputes
- Accidents or emergencies
- Account blocked
- Overcharging

Always be polite and brief.
Reply in same language as user (Hindi or English).`

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

func generateAIReply(subject, message string) (string, bool) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return "", false
	}

	fullPrompt := aiSystemPrompt + "\n\nUser message about: " + subject + "\nMessage: " + message

	reqBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": fullPrompt},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 300,
			"temperature":     0.7,
		},
	}

	bodyBytes, _ := json.Marshal(reqBody)

	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key=" + apiKey

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", false
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", false
	}
	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return "", false
	}

	aiReply := result.Candidates[0].Content.Parts[0].Text

	shouldEscalate := strings.Contains(strings.ToUpper(aiReply), "ESCALATE")

	// Strip the ESCALATE prefix before sending reply to user
	aiReply = strings.Replace(aiReply, "ESCALATE: ", "", 1)
	aiReply = strings.Replace(aiReply, "ESCALATE:", "", 1)

	return aiReply, shouldEscalate
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

	// Trigger AI reply — wait up to 2.5s then return anyway
	replyCh := make(chan string, 1)
	go func() {
		reply, _ := generateAIReply(req.Subject, req.FirstMessage)
		replyCh <- reply
	}()

	select {
	case reply := <-replyCh:
		if reply != "" {
			pool.Exec(ctx, `
				INSERT INTO support_messages
					(ticket_id, sender_type, sender_id, sender_name, message)
				VALUES ($1, 'bot', 'ai', 'gogoo Assistant 🤖', $2)
			`, ticketID, reply)
		}
	case <-time.After(2500 * time.Millisecond):
		// AI is slow; save reply in background when it arrives
		go func() {
			reply := <-replyCh
			if reply != "" {
				pool.Exec(context.Background(), `
					INSERT INTO support_messages
						(ticket_id, sender_type, sender_id, sender_name, message)
					VALUES ($1, 'bot', 'ai', 'gogoo Assistant 🤖', $2)
				`, ticketID, reply)
			}
		}()
	}

	c.JSON(http.StatusCreated, gin.H{
		"ticket_id":     ticketID,
		"ticket_number": ticketNumber,
		"messages":      fetchChatMessages(ctx, ticketID),
	})
}

// GET /gogoo/support/chat/:ticket_id/messages
func GetChatMessages(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	ticketID := c.Param("ticket_id")

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

	// Trigger AI reply if no human agent has responded yet
	var subject string
	pool.QueryRow(ctx, `SELECT subject FROM support_tickets WHERE id = $1`, ticketID).Scan(&subject)

	var userMsgCount int
	pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM support_messages
		WHERE ticket_id = $1 AND sender_type IN ('rider', 'driver')
	`, ticketID).Scan(&userMsgCount)

	if userMsgCount <= 3 {
		go func() {
			var humanReplied bool
			pool.QueryRow(context.Background(), `
				SELECT EXISTS(
					SELECT 1 FROM support_messages
					WHERE ticket_id = $1 AND sender_type = 'support'
				)
			`, ticketID).Scan(&humanReplied)

			if !humanReplied {
				reply, _ := generateAIReply(subject, req.Message)
				if reply != "" {
					pool.Exec(context.Background(), `
						INSERT INTO support_messages
							(ticket_id, sender_type, sender_id, sender_name, message)
						VALUES ($1, 'bot', 'ai', 'gogoo Assistant 🤖', $2)
					`, ticketID, reply)
				}
			}
		}()
	}

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
