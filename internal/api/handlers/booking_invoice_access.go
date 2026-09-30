package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/services/invoicemail"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Rider-facing invoice access (post-ride invoices, phase 3).

// invoiceOwnerRiderID returns the booking's rider id when the caller is that
// rider. Owner only — no driver, panel or admin access — and ok is false for
// a booking that doesn't exist (or an ID that isn't a UUID), so both routes
// answer a bare 403 and can't be used to probe which booking IDs are real.
func invoiceOwnerRiderID(ctx context.Context, pool *pgxpool.Pool, bookingID, userID string) (string, bool) {
	var riderID string
	err := pool.QueryRow(ctx, `
		SELECT b.rider_id::text
		FROM bookings b
		JOIN riders r ON r.id = b.rider_id
		WHERE b.id = $1 AND r.user_id = $2::uuid
	`, bookingID, userID).Scan(&riderID)
	if err != nil {
		return "", false
	}
	return riderID, true
}

func appConfig(c *gin.Context) *config.Config {
	v, ok := c.Get("config")
	if !ok {
		return nil
	}
	cfg, _ := v.(*config.Config)
	return cfg
}

// GET /gogoo/bookings/:id/invoice — the invoice PDF, regenerated on demand.
func GetBookingInvoice(c *gin.Context) {
	bookingID := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	if _, ok := invoiceOwnerRiderID(ctx, pool, bookingID, c.GetString("user_id")); !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	cfg := appConfig(c)
	if cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server misconfigured"})
		return
	}

	pdf, filename, err := invoicemail.RenderPDF(ctx, cfg, bookingID)
	if errors.Is(err, invoicemail.ErrNoInvoice) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no invoice for this booking", "code": "invoice_not_available"})
		return
	}
	if err != nil {
		log.Printf("GetBookingInvoice: booking %s render failed: %v", bookingID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not generate invoice"})
		return
	}

	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Header("Cache-Control", "private, no-store")
	c.Data(http.StatusOK, "application/pdf", pdf)
}

// POST /gogoo/bookings/:id/invoice/resend — email the invoice to the rider
// again. Rate limited per booking and per rider (see invoicemail.Resend).
func ResendBookingInvoice(c *gin.Context) {
	bookingID := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	riderID, ok := invoiceOwnerRiderID(ctx, pool, bookingID, c.GetString("user_id"))
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	sentTo, err := invoicemail.Resend(ctx, appConfig(c), bookingID, riderID)
	var limitErr *invoicemail.RateLimitError
	var notSendable *invoicemail.NotSendableError
	switch {
	case err == nil:
		c.JSON(http.StatusOK, gin.H{"sent_to": sentTo})
	case errors.Is(err, invoicemail.ErrEmailDisabled):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "invoice email is not available", "code": "email_disabled"})
	case errors.Is(err, invoicemail.ErrNoInvoice):
		c.JSON(http.StatusNotFound, gin.H{"error": "no invoice for this booking", "code": "invoice_not_available"})
	case errors.As(err, &notSendable):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "this invoice can't be emailed", "code": "not_sendable"})
	case errors.As(err, &limitErr):
		resp := gin.H{"error": "resend limit reached", "code": "rate_limited", "scope": limitErr.Scope}
		if !limitErr.RetryAfter.IsZero() {
			resp["retry_after"] = limitErr.RetryAfter.Format(time.RFC3339)
			if secs := int(time.Until(limitErr.RetryAfter).Seconds()) + 1; secs > 0 {
				c.Header("Retry-After", strconv.Itoa(secs))
			}
		}
		c.JSON(http.StatusTooManyRequests, resp)
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not send invoice", "code": "send_failed"})
	}
}
