// Package mail sends transactional email (currently: monthly driver
// earnings statements) via Resend's HTTP API. Railway blocks outbound SMTP
// port 587 (confirmed: "dial tcp ...:587: connect: connection timed out"
// from production) and 465 wasn't worth betting on either — Resend's API
// runs over plain HTTPS (443), which is never blocked, so it replaces the
// raw net/smtp client entirely.
package mail

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/config"
)

const resendEndpoint = "https://api.resend.com/emails"

// Attachment is a single file to attach to an outgoing email.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Message is a plain-text transactional email, optionally with attachments.
type Message struct {
	To          string
	Subject     string
	Body        string
	Attachments []Attachment
}

// IsConfigured reports whether enough settings are present to attempt
// sending. Callers should treat a false return as "skip silently" rather
// than an error — email is optional deploy-time configuration.
func IsConfigured(cfg *config.Config) bool {
	return cfg.ResendAPIKey != "" && cfg.SMTPFromEmail != ""
}

type resendAttachment struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

type resendPayload struct {
	From        string             `json:"from"`
	To          []string           `json:"to"`
	Subject     string             `json:"subject"`
	Text        string             `json:"text"`
	Attachments []resendAttachment `json:"attachments,omitempty"`
}

// Send delivers msg via the Resend HTTP API. Reuses SMTP_FROM_EMAIL/
// SMTP_FROM_NAME as the sender identity — those describe who the email is
// from, not how it's transported, so there's no reason to duplicate them
// under a new name just because the transport changed.
func Send(cfg *config.Config, msg Message) error {
	if !IsConfigured(cfg) {
		return fmt.Errorf("resend not configured (RESEND_API_KEY/SMTP_FROM_EMAIL missing)")
	}

	from := cfg.SMTPFromEmail
	if cfg.SMTPFromName != "" {
		from = fmt.Sprintf("%s <%s>", cfg.SMTPFromName, cfg.SMTPFromEmail)
	}

	payload := resendPayload{
		From:    from,
		To:      strings.Split(msg.To, ","),
		Subject: msg.Subject,
		Text:    msg.Body,
	}
	for _, a := range msg.Attachments {
		payload.Attachments = append(payload.Attachments, resendAttachment{
			Filename: a.Filename,
			Content:  base64.StdEncoding.EncodeToString(a.Data),
		})
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal resend payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, resendEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build resend request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ResendAPIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("resend request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("resend API error (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}
