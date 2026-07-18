package handlers

// Bogie Tracker — staff mark-paid for plan/subscription billing orders. See
// migration 032's comment: no payment gateway is wired up yet, so a staff
// member confirms payment was received out-of-band and this endpoint is the
// trigger — it stamps an invoice number, renders the PDF, and emails it to
// the company. When a gateway lands later, its webhook can drive the same
// core instead of a staff click, with no schema change needed.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/mail"
	"github.com/deploykit/backend/internal/services/trackerbilling"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// nextTrackerInvoiceNumber allocates the next sequential invoice number for
// the given year, formatted INV-<year>-00001. Must run inside tx — the
// advisory lock is held for the transaction's lifetime, serializing
// concurrent mark-paid calls so two staff clicking at once (or a retry race)
// can't land on the same number.
func nextTrackerInvoiceNumber(ctx context.Context, tx pgx.Tx, year int) (string, error) {
	lockKey := fmt.Sprintf("tracker_invoice_number_%d", year)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return "", err
	}

	prefix := fmt.Sprintf("INV-%d-", year)
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM tracker_plan_orders WHERE invoice_number LIKE $1
	`, prefix+"%").Scan(&count); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%05d", prefix, count+1), nil
}

// POST /gogoo/dashboard/tracker/plan-orders/:id/mark-paid
func MarkTrackerPlanOrderPaid(c *gin.Context) {
	orderID := c.Param("id")
	var req struct {
		PaymentGatewayRef string `json:"payment_gateway_ref"`
	}
	// Body is optional — a plain staff click with nothing to record is fine.
	_ = c.ShouldBindJSON(&req)

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var o TrackerPlanOrder
	err := pool.QueryRow(ctx, `
		SELECT id, company_id, plan, billing_duration, base_amount, gst_amount, total_amount,
		       billing_name, billing_address_line, billing_city, billing_state, billing_pincode, gstin,
		       invoice_number, status
		FROM tracker_plan_orders WHERE id = $1
	`, orderID).Scan(
		&o.ID, &o.CompanyID, &o.Plan, &o.BillingDuration, &o.BaseAmount, &o.GSTAmount, &o.TotalAmount,
		&o.BillingName, &o.BillingAddressLine, &o.BillingCity, &o.BillingState, &o.BillingPincode, &o.GSTIN,
		&o.InvoiceNumber, &o.Status,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		}
		return
	}

	// Idempotent — a staff member re-clicking (or a double-submit) just
	// returns the existing invoice rather than erroring or double-charging.
	if o.Status == "paid" {
		c.JSON(http.StatusOK, gin.H{"message": "order already marked paid", "invoice_number": o.InvoiceNumber})
		return
	}
	if o.Status == "cancelled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order is cancelled and cannot be marked paid"})
		return
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback(ctx)

	invoiceNumber, err := nextTrackerInvoiceNumber(ctx, tx, time.Now().Year())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate invoice number"})
		return
	}

	var paidAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE tracker_plan_orders
		SET status = 'paid', invoice_number = $1, payment_gateway_ref = $2, paid_at = NOW(), updated_at = NOW()
		WHERE id = $3
		RETURNING paid_at
	`, invoiceNumber, nullIfEmpty(req.PaymentGatewayRef), orderID).Scan(&paidAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark paid: " + err.Error()})
		return
	}

	// Payment is now the account-activation trigger: a company still sitting
	// in 'pending' gets its license key + a fresh system password on this
	// transition. Already-'active' companies are left untouched (no password
	// rotation on a repeat/renewal order), and 'suspended'/'rejected' stay
	// exactly that — payment does not override a staff decision.
	var companyStatus string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM tracker_companies WHERE id = $1
	`, o.CompanyID).Scan(&companyStatus); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load company: " + err.Error()})
		return
	}

	var licenseKey, newPassword string
	activated := companyStatus == "pending"
	if activated {
		licenseKey, err = generateTrackerLicenseKey()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate license key"})
			return
		}
		newPassword, err = generateRandomPassword()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate password"})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
			return
		}
		approvedBy := c.GetString("user_id")
		if _, err := tx.Exec(ctx, `
			UPDATE tracker_companies
			SET status = 'active', license_key = $1, password_hash = $2,
			    approved_by = $3, approved_at = NOW(), updated_at = NOW()
			WHERE id = $4
		`, licenseKey, string(hash), approvedBy, o.CompanyID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to activate company: " + err.Error()})
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit"})
		return
	}

	// Payment is committed at this point regardless of what happens below —
	// PDF/email failures are reported back but never undo the paid status.
	emailStatus := sendTrackerPlanInvoiceEmail(c, &o, invoiceNumber, paidAt)

	if activated {
		cfg := c.MustGet("config").(*config.Config)
		var companyName, contactEmail string
		if err := pool.QueryRow(ctx, `
			SELECT company_name, contact_email FROM tracker_companies WHERE id = $1
		`, o.CompanyID).Scan(&companyName, &contactEmail); err != nil {
			log.Printf("MarkTrackerPlanOrderPaid: company lookup for license email failed for order=%s: %v", o.ID, err)
		} else {
			sendTrackerLicenseEmail(cfg, companyName, contactEmail, licenseKey, contactEmail, newPassword)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":           "order marked paid",
		"invoice_number":    invoiceNumber,
		"paid_at":           paidAt,
		"email":             emailStatus,
		"company_activated": activated,
	})
}

// sendTrackerPlanInvoiceEmail renders the invoice PDF and emails it to the
// company synchronously (mirrors NotifyTrackerOrderStakeholders — this is a
// single staff-triggered send, worth a few hundred ms of Resend latency so
// the response can report whether it actually went out). Returns one of
// "sent" | "skipped" | "failed".
func sendTrackerPlanInvoiceEmail(c *gin.Context, o *TrackerPlanOrder, invoiceNumber string, paidAt time.Time) string {
	gstin := ""
	if o.GSTIN != nil {
		gstin = *o.GSTIN
	}
	pdfBytes, err := trackerbilling.GenerateInvoicePDF(&trackerbilling.Invoice{
		InvoiceNumber:      invoiceNumber,
		IssuedAt:           paidAt,
		OrderID:            o.ID,
		Plan:               o.Plan,
		BillingDuration:    o.BillingDuration,
		BaseAmount:         o.BaseAmount,
		GSTAmount:          o.GSTAmount,
		TotalAmount:        o.TotalAmount,
		BillingName:        o.BillingName,
		BillingAddressLine: o.BillingAddressLine,
		BillingCity:        o.BillingCity,
		BillingState:       o.BillingState,
		BillingPincode:     o.BillingPincode,
		GSTIN:              gstin,
	})
	if err != nil {
		log.Printf("sendTrackerPlanInvoiceEmail: pdf generation failed for order=%s: %v", o.ID, err)
		return "failed"
	}

	cfg := c.MustGet("config").(*config.Config)
	if !mail.IsConfigured(cfg) {
		return "skipped"
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	var companyName, contactEmail string
	var notificationEmail *string
	if err := pool.QueryRow(ctx, `
		SELECT company_name, contact_email, notification_email FROM tracker_companies WHERE id = $1
	`, o.CompanyID).Scan(&companyName, &contactEmail, &notificationEmail); err != nil {
		log.Printf("sendTrackerPlanInvoiceEmail: company lookup failed for order=%s: %v", o.ID, err)
		return "failed"
	}

	to := contactEmail
	if notificationEmail != nil && *notificationEmail != "" {
		to = *notificationEmail
	}

	body := fmt.Sprintf(
		"Hi %s,\n\n"+
			"We've received your payment for the Bogie Tracker %s plan (%s billing). Your invoice %s is attached.\n\n"+
			"Total paid: Rs.%.2f\n\n"+
			"Questions? Reply to this email or contact support@bogie.in.\n\n"+
			"— Team Bogie",
		companyName, trackerbilling.PlanLabel(o.Plan), o.BillingDuration, invoiceNumber, o.TotalAmount,
	)

	if err := mail.Send(cfg, mail.Message{
		To:      to,
		Subject: fmt.Sprintf("Your Bogie Tracker invoice %s", invoiceNumber),
		Body:    body,
		Attachments: []mail.Attachment{{
			Filename:    invoiceNumber + ".pdf",
			ContentType: "application/pdf",
			Data:        pdfBytes,
		}},
	}); err != nil {
		log.Printf("sendTrackerPlanInvoiceEmail: send failed for order=%s: %v", o.ID, err)
		return "failed"
	}

	return "sent"
}
