package handlers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
)

// POST /gogoo/support/lost-item/photo — optional photo upload, done BEFORE
// the main report submission; returns the Cloudinary URL to pass as
// photo_url in POST /gogoo/support/lost-item. Reuses the exact same
// upload helper, size cap, and mime-type allowlist as driver document
// uploads (documents.go) — no separate upload path to maintain.
func UploadLostItemPhoto(c *gin.Context) {
	ctx := context.Background()
	userID := c.GetString("user_id")

	if err := c.Request.ParseMultipartForm(maxFileSize); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file too large — max 10MB allowed"})
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	defer file.Close()

	mimeType := header.Header.Get("Content-Type")
	if idx := strings.Index(mimeType, ";"); idx > 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	if _, allowed := allowedMimeTypes[mimeType]; !allowed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only JPG, PNG and PDF files allowed"})
		return
	}
	if header.Size > maxFileSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file must be under 10MB"})
		return
	}
	if os.Getenv("CLOUDINARY_CLOUD_NAME") == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "photo upload not available right now"})
		return
	}

	url, err := uploadToCloudinary(ctx, file, header.Filename, "lost_item", userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cloud storage error: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": url})
}

// POST /gogoo/support/lost-item — creates a support ticket pre-filled with
// the ride's route/service/driver details, reusing the exact same ticket +
// chat system as every other support conversation. Identity and the
// booking's ownership both come from the JWT/DB, never from client-supplied
// IDs alone.
func ReportLostItem(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	userID := c.GetString("user_id")

	var req struct {
		BookingID         string `json:"booking_id"`
		ItemDescription   string `json:"item_description"`
		AdditionalDetails string `json:"additional_details"`
		PhotoURL          string `json:"photo_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.BookingID == "" || strings.TrimSpace(req.ItemDescription) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "booking_id and item_description are required"})
		return
	}

	// Ownership check + everything needed for a readable ticket, in one
	// query — a booking_id is a client-supplied path value, never trusted
	// without confirming it's this caller's own ride.
	var riderID, pickup, drop, serviceName string
	var driverID, driverName, driverPhone *string
	var bookedAt time.Time
	err := pool.QueryRow(ctx, `
		SELECT r.id, b.pickup_address, b.drop_address, st.name,
		       b.driver_id, du.name, d.phone, b.created_at
		FROM bookings b
		JOIN riders r      ON r.id = b.rider_id
		JOIN users u_r     ON u_r.id = r.user_id
		JOIN service_types st ON st.id = b.service_type_id
		LEFT JOIN drivers d ON d.id = b.driver_id
		LEFT JOIN users du  ON du.id = d.user_id
		WHERE b.id = $1 AND u_r.id = $2
	`, req.BookingID, userID).Scan(&riderID, &pickup, &drop, &serviceName, &driverID, &driverName, &driverPhone, &bookedAt)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "booking not found or not yours"})
		return
	}

	shortRef := req.BookingID
	if len(shortRef) > 8 {
		shortRef = shortRef[:8]
	}

	var desc strings.Builder
	fmt.Fprintf(&desc, "Item: %s\n", req.ItemDescription)
	if strings.TrimSpace(req.AdditionalDetails) != "" {
		fmt.Fprintf(&desc, "\nAdditional details: %s\n", req.AdditionalDetails)
	}
	fmt.Fprintf(&desc, "\nRide: %s → %s (%s, %s)\n", pickup, drop, serviceName, bookedAt.Format("2 Jan 2006, 3:04 PM"))
	if driverName != nil && *driverName != "" {
		fmt.Fprintf(&desc, "Driver: %s", *driverName)
		if driverPhone != nil && *driverPhone != "" {
			fmt.Fprintf(&desc, " (%s)", *driverPhone)
		}
		desc.WriteString("\n")
	}
	if req.PhotoURL != "" {
		fmt.Fprintf(&desc, "Photo: %s\n", req.PhotoURL)
	}

	subject := fmt.Sprintf("Lost item — Ride #%s", shortRef)

	// Setting driver_id here (not just booking_id) means the support panel's
	// existing ticket queries — which already LEFT JOIN drivers via
	// t.driver_id — surface this ticket's driver name/phone for free, with
	// no panel-side changes needed for that part.
	var ticketID, ticketNumber string
	err = pool.QueryRow(ctx, `
		INSERT INTO support_tickets
			(type, priority, subject, description, raised_by, rider_id, driver_id, booking_id)
		VALUES ('booking_issue', 'medium', $1, $2, 'rider', $3, $4, $5)
		RETURNING id, ticket_number
	`, subject, desc.String(), riderID, driverID, req.BookingID).Scan(&ticketID, &ticketNumber)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create ticket"})
		return
	}

	senderName := c.GetString("user_name")
	if senderName == "" {
		senderName = "Rider"
	}
	pool.Exec(ctx, `
		INSERT INTO support_messages (ticket_id, sender_type, sender_id, sender_name, message)
		VALUES ($1, 'rider', $2, $3, $4)
	`, ticketID, userID, senderName, req.ItemDescription)

	pool.Exec(ctx, `
		INSERT INTO support_messages (ticket_id, sender_type, sender_id, sender_name, message)
		VALUES ($1, 'bot', 'faq', $2, $3)
	`, ticketID, botSenderName,
		"We've reported your lost item to our team along with your ride details. We'll try to reach your driver and update you here.")

	c.JSON(http.StatusCreated, gin.H{"ticket_id": ticketID, "ticket_number": ticketNumber})
}
