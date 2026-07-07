package handlers

import (
	"log"
	"net/http"
	"strings"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/mail"
	"github.com/gin-gonic/gin"
)

// POST /gogoo/admin/test-email — master-admin-only, TEMPORARY.
// Sends a one-off plain-text email through the configured mailer so we can
// confirm it actually works in production before trusting the monthly
// statement mailer with it. Remove this endpoint once verified.
// with_attachment lets us isolate whether a failure is specific to
// Resend's attachment handling vs. the plain-send path.
func TestSMTPEmail(c *gin.Context) {
	var req struct {
		To             string `json:"to"`
		WithAttachment bool   `json:"with_attachment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.To) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must include a non-empty \"to\" email address"})
		return
	}
	if !strings.Contains(req.To, "@") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "\"to\" does not look like an email address"})
		return
	}

	cfg := c.MustGet("config").(*config.Config)
	if !mail.IsConfigured(cfg) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "SMTP not configured (SMTP_HOST/SMTP_FROM_EMAIL missing)"})
		return
	}

	msg := mail.Message{
		To:      req.To,
		Subject: "gogoo SMTP Test",
		Body:    "If you're reading this, SMTP is configured correctly.",
	}
	if req.WithAttachment {
		msg.Attachments = []mail.Attachment{{
			Filename:    "test.pdf",
			ContentType: "application/pdf",
			Data:        []byte("%PDF-1.4\n%dummy test attachment, not a real PDF\n"),
		}}
	}

	err := mail.Send(cfg, msg)
	if err != nil {
		log.Printf("test-email: send to %s (attachment=%v) failed: %v", req.To, req.WithAttachment, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	log.Printf("test-email: sent to %s (attachment=%v)", req.To, req.WithAttachment)
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "test email sent"})
}
